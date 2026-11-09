//go:build linux || darwin

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sockConn is an ICMP socket: an unprivileged datagram "ping socket" when
// possible, or a raw socket when running as root / with CAP_NET_RAW.
type sockConn struct {
	fd        int
	v6        bool
	raw       bool
	id        uint16
	filterID  bool
	dst       unix.Sockaddr
	connected bool
	recvErr   bool
	buf, oob  []byte
}

func openICMP(cfg sockConfig) (icmpConn, error) {
	domain, proto := unix.AF_INET, unix.IPPROTO_ICMP
	if cfg.V6 {
		domain, proto = unix.AF_INET6, unix.IPPROTO_ICMPV6
	}
	order := []int{unix.SOCK_DGRAM, unix.SOCK_RAW}
	if unix.Geteuid() == 0 || cfg.PreferRaw {
		order = []int{unix.SOCK_RAW, unix.SOCK_DGRAM}
	}
	var firstErr error
	for _, typ := range order {
		fd, err := unix.Socket(domain, typ, proto)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c := &sockConn{fd: fd, v6: cfg.V6, raw: typ == unix.SOCK_RAW,
			buf: make([]byte, 65536+512), oob: make([]byte, 512)}
		if err := c.setup(&cfg); err != nil {
			unix.Close(fd)
			return nil, err
		}
		return c, nil
	}
	if errors.Is(firstErr, unix.EACCES) || errors.Is(firstErr, unix.EPERM) {
		return nil, fmt.Errorf("socket: %v (unprivileged ICMP sockets are not allowed; check net.ipv4.ping_group_range or run as root)", firstErr)
	}
	return nil, fmt.Errorf("socket: %v", firstErr)
}

func (c *sockConn) setup(cfg *sockConfig) error {
	fd := c.fd
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		return err
	}
	c.dst = toSockaddr(cfg.Dst, cfg.Zone)

	setInt := func(level, opt, v int, name string, fatal bool) error {
		if err := unix.SetsockoptInt(fd, level, opt, v); err != nil {
			if fatal {
				return fmt.Errorf("%s: %v", name, err)
			}
			cfg.warn(fmt.Sprintf("%s: %v", name, err))
		}
		return nil
	}

	if cfg.TTL >= 0 {
		if cfg.V6 {
			if err := setInt(unix.IPPROTO_IPV6, unix.IPV6_UNICAST_HOPS, cfg.TTL, "can't set unicast hop limit", true); err != nil {
				return err
			}
			_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, cfg.TTL)
		} else {
			if err := setInt(unix.IPPROTO_IP, unix.IP_TTL, cfg.TTL, "can't set unicast time-to-live", true); err != nil {
				return err
			}
			_ = unix.SetsockoptByte(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_TTL, byte(cfg.TTL))
		}
	}
	if cfg.TOS >= 0 {
		if cfg.V6 {
			if err := setInt(unix.IPPROTO_IPV6, unix.IPV6_TCLASS, cfg.TOS, "setsockopt(IPV6_TCLASS)", true); err != nil {
				return err
			}
		} else if err := setInt(unix.IPPROTO_IP, unix.IP_TOS, cfg.TOS, "error setting QOS sockopts", true); err != nil {
			return err
		}
	}
	if cfg.Broadcast {
		if err := setInt(unix.SOL_SOCKET, unix.SO_BROADCAST, 1, "can't set broadcasting", true); err != nil {
			return err
		}
	}
	if cfg.DontRoute {
		_ = setInt(unix.SOL_SOCKET, unix.SO_DONTROUTE, 1, "SO_DONTROUTE", false)
	}
	if cfg.Debug {
		_ = setInt(unix.SOL_SOCKET, unix.SO_DEBUG, 1, "can't set SO_DEBUG", false)
	}
	if cfg.SndBuf > 0 {
		_ = setInt(unix.SOL_SOCKET, unix.SO_SNDBUF, cfg.SndBuf, "SO_SNDBUF", false)
	}
	if cfg.NoMcastLoop {
		if cfg.V6 {
			_ = setInt(unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_LOOP, 0, "can't disable multicast loopback", false)
		} else if err := unix.SetsockoptByte(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_LOOP, 0); err != nil {
			cfg.warn("can't disable multicast loopback: " + err.Error())
		}
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMP, 1)
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 256*1024)
	if cfg.V6 {
		_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_RECVHOPLIMIT, 1)
	} else {
		_ = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVTTL, 1)
	}
	if cfg.RecordRoute && !cfg.V6 {
		if err := unix.SetsockoptString(fd, unix.IPPROTO_IP, unix.IP_OPTIONS, string(recordRouteOption())); err != nil {
			cfg.warn("record route: " + err.Error())
		}
	}
	if err := platformSetup(c, cfg); err != nil {
		return err
	}

	kernelID := !c.raw && platformKernelAssignsID()
	if cfg.Src != nil || (kernelID && cfg.Ident >= 0) {
		port := 0
		if kernelID && cfg.Ident >= 0 {
			port = cfg.Ident
		}
		var sa unix.Sockaddr
		if cfg.V6 {
			s := &unix.SockaddrInet6{Port: port}
			if cfg.Src != nil {
				copy(s.Addr[:], cfg.Src.To16())
			}
			sa = s
		} else {
			s := &unix.SockaddrInet4{Port: port}
			if cfg.Src != nil {
				copy(s.Addr[:], cfg.Src.To4())
			}
			sa = s
		}
		if err := unix.Bind(fd, sa); err != nil {
			return fmt.Errorf("bind: %v", err)
		}
	}

	if kernelID {
		// Linux ping sockets rewrite the identifier with the socket "port" and
		// demultiplex replies themselves.
		if sa, err := unix.Getsockname(fd); err == nil {
			switch s := sa.(type) {
			case *unix.SockaddrInet4:
				c.id = uint16(s.Port)
			case *unix.SockaddrInet6:
				c.id = uint16(s.Port)
			}
		}
	} else {
		c.filterID = true
		if cfg.Ident >= 0 {
			c.id = uint16(cfg.Ident)
		} else {
			c.id = uint16(rand.IntN(0xffff) + 1)
		}
	}

	if cfg.Connect {
		if err := unix.Connect(fd, c.dst); err != nil {
			return fmt.Errorf("connect: %v", err)
		}
		c.connected = true
	}
	return nil
}

func toSockaddr(ip net.IP, zone string) unix.Sockaddr {
	if ip4 := ip.To4(); ip4 != nil {
		s := &unix.SockaddrInet4{}
		copy(s.Addr[:], ip4)
		return s
	}
	s := &unix.SockaddrInet6{}
	copy(s.Addr[:], ip.To16())
	if zone != "" {
		if ifi, err := net.InterfaceByName(zone); err == nil {
			s.ZoneId = uint32(ifi.Index)
		}
	}
	return s
}

func sockaddrIP(sa unix.Sockaddr) net.IP {
	switch s := sa.(type) {
	case *unix.SockaddrInet4:
		return net.IP(append([]byte(nil), s.Addr[:]...))
	case *unix.SockaddrInet6:
		return net.IP(append([]byte(nil), s.Addr[:]...))
	}
	return nil
}

func (c *sockConn) ID() uint16     { return c.id }
func (c *sockConn) FilterID() bool { return c.filterID }
func (c *sockConn) Raw() bool      { return c.raw }
func (c *sockConn) Close() error   { return unix.Close(c.fd) }

func (c *sockConn) Send(pkt []byte) (time.Time, error) {
	for {
		t := time.Now()
		var err error
		if c.connected {
			_, err = unix.Write(c.fd, pkt)
		} else {
			err = unix.Sendto(c.fd, pkt, 0, c.dst)
		}
		if err == unix.EINTR {
			continue
		}
		return t, err
	}
}

// Read waits up to timeout for one packet. ok=false with a nil error means
// nothing relevant was received.
func (c *sockConn) Read(timeout time.Duration) (Event, bool, error) {
	fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if err != nil {
		if err == unix.EINTR {
			return Event{}, false, nil
		}
		return Event{}, false, err
	}
	if n == 0 {
		return Event{}, false, nil
	}
	if c.recvErr && fds[0].Revents&unix.POLLERR != 0 {
		if ev, ok, err := readErrQueue(c); ok || err != nil {
			return ev, ok, err
		}
	}
	nr, oobn, _, from, err := unix.Recvmsg(c.fd, c.buf, c.oob, unix.MSG_DONTWAIT)
	if err != nil {
		if err == unix.EAGAIN || err == unix.EINTR {
			if c.recvErr {
				return readErrQueue(c)
			}
			return Event{}, false, nil
		}
		if c.recvErr && err != unix.EBADF {
			// A pending socket error (EHOSTUNREACH...) mirrors an entry
			// already reported through the error queue.
			return Event{}, false, nil
		}
		return Event{}, false, err
	}
	at := time.Now()
	data := c.buf[:nr]
	ttl := -1
	var route []net.IP
	if !c.v6 {
		var ok bool
		if data, ttl, route, ok = stripIPv4(data); !ok {
			return Event{}, false, nil
		}
	}
	cmTTL, ts := parseCmsgs(c.oob[:oobn])
	if ttl < 0 {
		ttl = cmTTL
	}
	if !ts.IsZero() && !ts.After(at) && at.Sub(ts) < time.Minute {
		at = ts
	}
	ev, ok := parseICMP(c.v6, data)
	if !ok {
		return Event{}, false, nil
	}
	ev.From, ev.TTL, ev.At, ev.Route = sockaddrIP(from), ttl, at, route
	if c.filterID && ev.Kind != EvOther && ev.ID != c.id {
		return Event{}, false, nil
	}
	return ev, true, nil
}

func parseCmsgs(oob []byte) (ttl int, ts time.Time) {
	ttl = -1
	if len(oob) == 0 {
		return
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		switch {
		case m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_TIMESTAMP:
			if len(m.Data) >= int(unsafe.Sizeof(unix.Timeval{})) {
				tv := (*unix.Timeval)(unsafe.Pointer(&m.Data[0]))
				ts = time.Unix(int64(tv.Sec), int64(tv.Usec)*1000)
			}
		case m.Header.Level == unix.IPPROTO_IP && m.Header.Type == ipTTLCmsgType:
			ttl = cmsgInt(m.Data)
		case m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_HOPLIMIT:
			ttl = cmsgInt(m.Data)
		}
	}
	return
}

func cmsgInt(b []byte) int {
	switch {
	case len(b) >= 4:
		return int(binary.NativeEndian.Uint32(b))
	case len(b) >= 1:
		return int(b[0])
	}
	return -1
}
