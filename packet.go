package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

type EventKind int

const (
	EvReply EventKind = iota // echo reply
	EvError                  // ICMP error (or local error) about one of our probes
	EvOther                  // unrelated ICMP message
)

// Event is a decoded packet (or error-queue entry) received on a socket.
type Event struct {
	Kind    EventKind
	From    net.IP
	ID, Seq uint16
	TTL     int // -1 when unknown
	Size    int // ICMP message length
	At      time.Time
	Type    int
	Code    int
	Info    uint32 // MTU, gateway, pointer... depending on Type/Code
	Local   bool   // locally generated error (Linux error queue)
	Errno   uint32 // errno for local errors
	Payload []byte // echo data (copied)
	Route   []net.IP
}

const (
	icmp4EchoReply    = 0
	icmp4Unreach      = 3
	icmp4SourceQuench = 4
	icmp4Redirect     = 5
	icmp4Echo         = 8
	icmp4TimeExceeded = 11
	icmp4ParamProb    = 12

	icmp6Unreach      = 1
	icmp6TooBig       = 2
	icmp6TimeExceeded = 3
	icmp6ParamProb    = 4
	icmp6Echo         = 128
	icmp6EchoReply    = 129
)

func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = (s >> 16) + (s & 0xffff)
	}
	return ^uint16(s)
}

// buildEcho returns a complete ICMP/ICMPv6 echo request. For ICMPv6 the
// kernel fills in the checksum (it needs the pseudo header).
func buildEcho(v6 bool, id, seq uint16, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = icmp4Echo
	if v6 {
		b[0] = icmp6Echo
	}
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	copy(b[8:], payload)
	if !v6 {
		binary.BigEndian.PutUint16(b[2:], checksum(b))
	}
	return b
}

// buildPayload mimics iputils: a timestamp first (when it fits), then either
// the -p pattern or incrementing bytes.
func buildPayload(size int, pattern []byte, now time.Time) []byte {
	p := make([]byte, size)
	off := 0
	if size >= 8 {
		binary.BigEndian.PutUint64(p, uint64(now.UnixNano()))
		off = 8
	}
	for i := off; i < size; i++ {
		if len(pattern) > 0 {
			p[i] = pattern[(i-off)%len(pattern)]
		} else {
			p[i] = byte(i)
		}
	}
	return p
}

// stripIPv4 removes an IPv4 header if present (raw sockets and Darwin
// datagram sockets deliver it) and returns the TTL and record-route data.
func stripIPv4(b []byte) (icmp []byte, ttl int, route []net.IP, ok bool) {
	if len(b) < 20 || b[0]>>4 != 4 {
		return b, -1, nil, true
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < 20 || ihl > len(b) {
		return nil, -1, nil, false
	}
	if ihl > 20 {
		route = parseRecordRoute(b[20:ihl])
	}
	return b[ihl:], int(b[8]), route, true
}

func parseRecordRoute(opts []byte) []net.IP {
	for i := 0; i < len(opts); {
		switch opts[i] {
		case 0:
			return nil
		case 1:
			i++
			continue
		}
		if i+1 >= len(opts) {
			return nil
		}
		l := int(opts[i+1])
		if l < 2 || i+l > len(opts) {
			return nil
		}
		if opts[i] == 7 && l >= 3 {
			ptr := int(opts[i+2])
			var ips []net.IP
			// ptr is the 1-based offset of the next free slot.
			for p := 3; p+4 <= l && p+4 <= ptr-1; p += 4 {
				ips = append(ips, net.IP(append([]byte(nil), opts[i+p:i+p+4]...)))
			}
			return ips
		}
		i += l
	}
	return nil
}

// recordRouteOption returns the IP_OPTIONS payload requesting record route.
func recordRouteOption() []byte {
	o := make([]byte, 40)
	o[0] = 1  // NOP, keeps the option 4-byte aligned like iputils
	o[1] = 7  // RR
	o[2] = 39 // length
	o[3] = 4  // pointer
	return o
}

// parseICMP decodes an ICMP message (IP header already stripped).
// ok=false means the packet should be silently ignored.
func parseICMP(v6 bool, b []byte) (Event, bool) {
	ev := Event{TTL: -1}
	if len(b) < 8 {
		return ev, false
	}
	ev.Type, ev.Code, ev.Size = int(b[0]), int(b[1]), len(b)
	echoReply, echoReq := icmp4EchoReply, icmp4Echo
	if v6 {
		echoReply, echoReq = icmp6EchoReply, icmp6Echo
	}
	switch {
	case ev.Type == echoReply:
		ev.Kind = EvReply
		ev.ID = binary.BigEndian.Uint16(b[4:])
		ev.Seq = binary.BigEndian.Uint16(b[6:])
		ev.Payload = append([]byte(nil), b[8:]...)
		return ev, true
	case ev.Type == echoReq:
		return ev, false
	case isICMPError(v6, ev.Type):
		id, seq, ok := innerEcho(v6, b[8:])
		if !ok {
			ev.Kind = EvOther
			return ev, true
		}
		ev.Kind, ev.ID, ev.Seq = EvError, id, seq
		switch {
		case !v6 && ev.Type == icmp4Unreach && ev.Code == 4:
			ev.Info = uint32(binary.BigEndian.Uint16(b[6:]))
		case !v6 && (ev.Type == icmp4Redirect):
			ev.Info = binary.BigEndian.Uint32(b[4:])
		case !v6 && ev.Type == icmp4ParamProb:
			ev.Info = uint32(b[4])
		case v6 && (ev.Type == icmp6TooBig || ev.Type == icmp6ParamProb):
			ev.Info = binary.BigEndian.Uint32(b[4:])
		}
		return ev, true
	default:
		ev.Kind = EvOther
		return ev, true
	}
}

func isICMPError(v6 bool, t int) bool {
	if v6 {
		return t >= 1 && t <= 4
	}
	switch t {
	case icmp4Unreach, icmp4SourceQuench, icmp4Redirect, icmp4TimeExceeded, icmp4ParamProb:
		return true
	}
	return false
}

// innerEcho extracts id/seq from the original datagram quoted in an ICMP error.
func innerEcho(v6 bool, b []byte) (id, seq uint16, ok bool) {
	if v6 {
		if len(b) < 48 || b[0]>>4 != 6 || b[6] != 58 || b[40] != icmp6Echo {
			return 0, 0, false
		}
		return binary.BigEndian.Uint16(b[44:]), binary.BigEndian.Uint16(b[46:]), true
	}
	if len(b) < 28 || b[0]>>4 != 4 || b[9] != 1 {
		return 0, 0, false
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < 20 || len(b) < ihl+8 || b[ihl] != icmp4Echo {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(b[ihl+4:]), binary.BigEndian.Uint16(b[ihl+6:]), true
}

// isTimeExceeded reports whether the event is a TTL/hop-limit expiry.
func (e *Event) isTimeExceeded(v6 bool) bool {
	if e.Kind != EvError || e.Local {
		return false
	}
	if v6 {
		return e.Type == icmp6TimeExceeded
	}
	return e.Type == icmp4TimeExceeded
}

// icmpErrorText returns the iputils wording for an ICMP error.
func icmpErrorText(v6 bool, typ, code int, info uint32) string {
	if v6 {
		switch typ {
		case icmp6Unreach:
			msgs := []string{"No route", "Administratively prohibited", "Beyond scope of source address",
				"Address unreachable", "Port unreachable", "Source address failed ingress/egress policy",
				"Reject route to destination"}
			if code < len(msgs) {
				return "Destination unreachable: " + msgs[code]
			}
			return fmt.Sprintf("Destination unreachable: Unknown code %d", code)
		case icmp6TooBig:
			return fmt.Sprintf("Packet too big: mtu=%d", info)
		case icmp6TimeExceeded:
			switch code {
			case 0:
				return "Time exceeded: Hop limit"
			case 1:
				return "Time exceeded: Defragmentation failure"
			}
			return fmt.Sprintf("Time exceeded: code %d", code)
		case icmp6ParamProb:
			msgs := []string{"Erroneous header field", "Unknown header", "Unknown option"}
			if code < len(msgs) {
				return fmt.Sprintf("Parameter problem: %s at %d", msgs[code], info)
			}
			return fmt.Sprintf("Parameter problem: code %d at %d", code, info)
		}
		return fmt.Sprintf("Unknown icmp type: %d", typ)
	}
	switch typ {
	case icmp4Unreach:
		msgs := []string{"Destination Net Unreachable", "Destination Host Unreachable",
			"Destination Protocol Unreachable", "Destination Port Unreachable", "",
			"Source Route Failed", "Destination Net Unknown", "Destination Host Unknown",
			"Source Host Isolated", "Destination Net Prohibited", "Destination Host Prohibited",
			"Destination Net Unreachable for Type of Service", "Destination Host Unreachable for Type of Service",
			"Packet filtered", "Precedence Violation", "Precedence Cutoff"}
		if code == 4 {
			return fmt.Sprintf("Frag needed and DF set (mtu = %d)", info)
		}
		if code < len(msgs) {
			return msgs[code]
		}
		return fmt.Sprintf("Dest Unreachable, Bad Code: %d", code)
	case icmp4SourceQuench:
		return "Source Quench"
	case icmp4Redirect:
		var gw [4]byte
		binary.BigEndian.PutUint32(gw[:], info)
		msgs := []string{"Redirect Network", "Redirect Host", "Redirect Type of Service and Network", "Redirect Type of Service and Host"}
		m := fmt.Sprintf("Redirect, Bad Code: %d", code)
		if code < len(msgs) {
			m = msgs[code]
		}
		return fmt.Sprintf("%s(New nexthop: %s)", m, net.IP(gw[:]))
	case icmp4TimeExceeded:
		switch code {
		case 0:
			return "Time to live exceeded"
		case 1:
			return "Frag reassembly time exceeded"
		}
		return fmt.Sprintf("Time exceeded, Bad Code: %d", code)
	case icmp4ParamProb:
		return fmt.Sprintf("Parameter problem: pointer = %d", info)
	}
	return fmt.Sprintf("Bad ICMP type: %d", typ)
}

// extErr is the Linux struct sock_extended_err followed by the offender address.
type extErr struct {
	Errno    uint32
	Origin   uint8
	Type     uint8
	Code     uint8
	Info     uint32
	Offender net.IP
}

const (
	eeOriginLocal = 1
	eeOriginICMP  = 2
	eeOriginICMP6 = 3
)

// parseExtErr decodes a Linux IP_RECVERR/IPV6_RECVERR control message payload.
func parseExtErr(b []byte) (extErr, bool) {
	if len(b) < 16 {
		return extErr{}, false
	}
	ne := binary.NativeEndian
	e := extErr{
		Errno:  ne.Uint32(b[0:]),
		Origin: b[4],
		Type:   b[5],
		Code:   b[6],
		Info:   ne.Uint32(b[8:]),
	}
	sa := b[16:]
	if len(sa) >= 2 {
		switch ne.Uint16(sa) {
		case 2: // AF_INET
			if len(sa) >= 8 {
				e.Offender = net.IP(append([]byte(nil), sa[4:8]...))
			}
		case 10: // AF_INET6 on Linux
			if len(sa) >= 24 {
				e.Offender = net.IP(append([]byte(nil), sa[8:24]...))
			}
		}
	}
	return e, true
}
