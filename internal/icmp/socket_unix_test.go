//go:build linux || darwin

package icmp

import (
	"errors"
	"net"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// skipNoICMP skips when the environment forbids ICMP sockets, unless
// ADVP_REQUIRE_ICMP=1 (set in CI) asks for a hard failure.
func skipNoICMP(t *testing.T, err error) {
	t.Helper()
	if os.Getenv("ADVP_REQUIRE_ICMP") == "1" {
		t.Fatalf("ICMP required but unavailable: %v", err)
	}
	t.Skipf("ICMP unavailable here: %v", err)
}

func cmsg(level, typ int32, data []byte) []byte {
	b := make([]byte, unix.CmsgSpace(len(data)))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&b[0]))
	h.Level, h.Type = level, typ
	h.SetLen(unix.CmsgLen(len(data)))
	copy(b[unix.CmsgLen(0):], data)
	return b
}

func TestParseCmsgs(t *testing.T) {
	tv := unix.NsecToTimeval(time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC).UnixNano())
	tvb := unsafe.Slice((*byte)(unsafe.Pointer(&tv)), unsafe.Sizeof(tv))
	ttl := []byte{57, 0, 0, 0}
	if runtime.GOOS == "darwin" {
		ttl = []byte{57} // IP_RECVTTL delivers a single byte on Darwin
	}
	var oob []byte
	oob = append(oob, cmsg(unix.SOL_SOCKET, unix.SCM_TIMESTAMP, tvb)...)
	oob = append(oob, cmsg(unix.IPPROTO_IP, ipTTLCmsgType, ttl)...)
	gotTTL, ts := parseCmsgs(oob)
	if gotTTL != 57 || ts.UnixNano() != tv.Nano() {
		t.Fatalf("ttl=%d ts=%v", gotTTL, ts)
	}
	hop := []byte{64, 0, 0, 0}
	if gotTTL, _ = parseCmsgs(cmsg(unix.IPPROTO_IPV6, unix.IPV6_HOPLIMIT, hop)); gotTTL != 64 {
		t.Fatalf("hop limit %d", gotTTL)
	}
	if gotTTL, ts = parseCmsgs(nil); gotTTL != -1 || !ts.IsZero() {
		t.Fatal("empty control data")
	}
	if cmsgInt(nil) != -1 || cmsgInt([]byte{9}) != 9 {
		t.Fatal("cmsgInt")
	}
}

func TestSockaddrConversions(t *testing.T) {
	sa := toSockaddr(net.IPv4(10, 1, 2, 3), "")
	if ip := sockaddrIP(sa); !ip.Equal(net.IPv4(10, 1, 2, 3)) {
		t.Fatalf("v4 round trip: %v", ip)
	}
	lo := loopbackName()
	sa6 := toSockaddr(net.ParseIP("fe80::1"), lo)
	if s, ok := sa6.(*unix.SockaddrInet6); !ok || (lo != "" && s.ZoneId == 0) {
		t.Fatalf("v6 zone not resolved: %+v", sa6)
	}
	if sockaddrIP(&unix.SockaddrUnix{Name: "x"}) != nil {
		t.Fatal("non-IP sockaddr")
	}
}

func loopbackName() string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			return i.Name
		}
	}
	return ""
}

// openLoopback opens a socket to the loopback address with the given extra
// configuration, skipping the test when ICMP sockets are not permitted.
func openLoopback(t *testing.T, v6 bool, tweak func(c *Config)) (Conn, []string) {
	t.Helper()
	dst := net.IPv4(127, 0, 0, 1).To4()
	if v6 {
		dst = net.IPv6loopback
	}
	var warns []string
	cfg := Config{V6: v6, Dst: dst, TTL: -1, TOS: -1, Ident: -1, Warnings: &warns}
	if tweak != nil {
		tweak(&cfg)
	}
	c, err := Open(cfg)
	if err != nil {
		skipNoICMP(t, err)
	}
	t.Cleanup(func() { c.Close() })
	return c, warns
}

func pingOnce(t *testing.T, c Conn, v6 bool, seq uint16) Event {
	t.Helper()
	pkt := BuildEcho(v6, c.ID(), seq, BuildPayload(56, []byte{0xab}, time.Now()))
	sent, err := c.Send(pkt)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			skipNoICMP(t, err)
		}
		t.Fatalf("send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok, err := c.Read(200 * time.Millisecond)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if ok && ev.Kind == KindReply && ev.Seq == seq {
			if ev.At.Before(sent) {
				t.Errorf("reply timestamp %v before send %v", ev.At, sent)
			}
			return ev
		}
	}
	t.Fatalf("no echo reply for seq %d", seq)
	return Event{}
}

func TestLoopbackEchoV4(t *testing.T) {
	c, _ := openLoopback(t, false, func(cfg *Config) { cfg.TTL, cfg.TOS = 64, 0x10 })
	if runtime.GOOS == "linux" && !c.Raw() && c.FilterID() {
		t.Error("linux ping sockets demultiplex in the kernel: FilterID must be false")
	}
	if runtime.GOOS == "darwin" && !c.FilterID() {
		t.Error("darwin sockets need user-space ID filtering")
	}
	ev := pingOnce(t, c, false, 1)
	if !ev.From.Equal(net.IPv4(127, 0, 0, 1)) || ev.Size != 64 || ev.TTL <= 0 || len(ev.Payload) != 56 || ev.Payload[8] != 0xab {
		t.Fatalf("unexpected reply %+v", ev)
	}
	pingOnce(t, c, false, 2)
}

func TestLoopbackEchoV6(t *testing.T) {
	c, _ := openLoopback(t, true, func(cfg *Config) { cfg.TTL = 10 })
	ev := pingOnce(t, c, true, 1)
	if !ev.From.Equal(net.IPv6loopback) || ev.Size != 64 {
		t.Fatalf("unexpected reply %+v", ev)
	}
}

func TestLoopbackConnectedSocket(t *testing.T) {
	c, _ := openLoopback(t, false, func(cfg *Config) { cfg.Connect = true })
	pingOnce(t, c, false, 1)
}

// TestOpenOptions exercises every socket option; unsupported ones must only
// produce warnings, never a failure.
func TestOpenOptions(t *testing.T) {
	_, warns := openLoopback(t, false, func(cfg *Config) {
		cfg.TTL, cfg.TOS, cfg.Broadcast, cfg.DontRoute, cfg.Debug = 3, 0x20, true, true, true
		cfg.NoMcastLoop, cfg.SndBuf, cfg.RecordRoute, cfg.Mark, cfg.MarkSet = true, 65536, true, 1, true
		cfg.PMTU, cfg.Ident = "dont", 4242
		// SO_BINDTODEVICE may need privileges on Linux; IP_BOUND_IF does not.
		if lo := loopbackName(); lo != "" && runtime.GOOS == "darwin" {
			if i, err := net.InterfaceByName(lo); err == nil {
				cfg.IfName, cfg.IfIndex = i.Name, i.Index
			}
		}
	})
	t.Logf("warnings: %v", warns)
	openLoopback(t, true, func(cfg *Config) {
		cfg.TOS, cfg.NoMcastLoop, cfg.PMTU, cfg.Src = 0x20, true, "do", net.IPv6loopback
	})
}

func TestReadTimeout(t *testing.T) {
	c, _ := openLoopback(t, false, nil)
	start := time.Now()
	if _, ok, err := c.Read(50 * time.Millisecond); ok || err != nil {
		t.Fatalf("idle read: ok=%v err=%v", ok, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("read ignored its timeout")
	}
}

func TestOpenRejectsMissingBindAddress(t *testing.T) {
	var warns []string
	_, err := Open(Config{Dst: net.IPv4(127, 0, 0, 1).To4(), TTL: -1, TOS: -1, Ident: -1,
		Src: net.IPv4(192, 0, 2, 123), Warnings: &warns})
	if err == nil {
		t.Skip("binding to a foreign address succeeded (privileged environment?)")
	}
	if !errors.Is(err, unix.EADDRNOTAVAIL) && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
		t.Logf("bind error: %v", err)
	}
}

func TestLocalErrorText(t *testing.T) {
	if s := LocalErrorText(&Event{Errno: uint32(unix.ENETUNREACH)}); s == "" || s == "local error: " {
		t.Fatalf("text %q", s)
	}
}
