package icmp

import (
	"encoding/binary"
	"testing"
)

func TestErrorTextV4(t *testing.T) {
	gw := binary.BigEndian.Uint32([]byte{192, 168, 1, 1})
	cases := []struct {
		typ, code int
		info      uint32
		want      string
	}{
		{V4Unreach, 0, 0, "Destination Net Unreachable"},
		{V4Unreach, 1, 0, "Destination Host Unreachable"},
		{V4Unreach, 3, 0, "Destination Port Unreachable"},
		{V4Unreach, 4, 1400, "Frag needed and DF set (mtu = 1400)"},
		{V4Unreach, 13, 0, "Packet filtered"},
		{V4Unreach, 15, 0, "Precedence Cutoff"},
		{V4Unreach, 99, 0, "Dest Unreachable, Bad Code: 99"},
		{V4SourceQuench, 0, 0, "Source Quench"},
		{V4Redirect, 1, gw, "Redirect Host(New nexthop: 192.168.1.1)"},
		{V4Redirect, 9, gw, "Redirect, Bad Code: 9(New nexthop: 192.168.1.1)"},
		{V4TimeExceeded, 0, 0, "Time to live exceeded"},
		{V4TimeExceeded, 1, 0, "Frag reassembly time exceeded"},
		{V4TimeExceeded, 7, 0, "Time exceeded, Bad Code: 7"},
		{V4ParamProb, 0, 20, "Parameter problem: pointer = 20"},
		{42, 0, 0, "Bad ICMP type: 42"},
	}
	for _, c := range cases {
		if got := ErrorText(false, c.typ, c.code, c.info); got != c.want {
			t.Errorf("v4 %d/%d: %q, want %q", c.typ, c.code, got, c.want)
		}
	}
}

func TestErrorTextV6(t *testing.T) {
	cases := []struct {
		typ, code int
		info      uint32
		want      string
	}{
		{V6Unreach, 0, 0, "Destination unreachable: No route"},
		{V6Unreach, 1, 0, "Destination unreachable: Administratively prohibited"},
		{V6Unreach, 3, 0, "Destination unreachable: Address unreachable"},
		{V6Unreach, 6, 0, "Destination unreachable: Reject route to destination"},
		{V6Unreach, 9, 0, "Destination unreachable: Unknown code 9"},
		{V6TooBig, 0, 1280, "Packet too big: mtu=1280"},
		{V6TimeExceeded, 0, 0, "Time exceeded: Hop limit"},
		{V6TimeExceeded, 1, 0, "Time exceeded: Defragmentation failure"},
		{V6TimeExceeded, 4, 0, "Time exceeded: code 4"},
		{V6ParamProb, 1, 40, "Parameter problem: Unknown header at 40"},
		{V6ParamProb, 7, 8, "Parameter problem: code 7 at 8"},
		{200, 0, 0, "Unknown icmp type: 200"},
	}
	for _, c := range cases {
		if got := ErrorText(true, c.typ, c.code, c.info); got != c.want {
			t.Errorf("v6 %d/%d: %q, want %q", c.typ, c.code, got, c.want)
		}
	}
}

func TestParseICMPVariants(t *testing.T) {
	if _, ok := parseICMP(false, []byte{0, 0, 0}); ok {
		t.Error("short packet accepted")
	}
	// Unrelated ICMP (timestamp request) is reported as KindOther.
	if ev, ok := parseICMP(false, []byte{13, 0, 0, 0, 0, 0, 0, 0}); !ok || ev.Kind != KindOther {
		t.Errorf("other: %+v", ev)
	}
	// An error quoting a non-echo datagram (UDP) is KindOther too.
	udp := make([]byte, 28)
	udp[0], udp[9] = 0x45, 17
	if ev, ok := parseICMP(false, append([]byte{V4Unreach, 3, 0, 0, 0, 0, 0, 0}, udp...)); !ok || ev.Kind != KindOther {
		t.Errorf("udp quote: %+v", ev)
	}
	// Frag-needed carries the next-hop MTU, param-problem the pointer.
	echo := make([]byte, 28)
	echo[0], echo[9], echo[20] = 0x45, 1, V4Echo
	binary.BigEndian.PutUint16(echo[24:], 7)
	binary.BigEndian.PutUint16(echo[26:], 9)
	if ev, _ := parseICMP(false, append([]byte{V4Unreach, 4, 0, 0, 0, 0, 0x05, 0x78}, echo...)); ev.Info != 1400 || ev.ID != 7 || ev.Seq != 9 {
		t.Errorf("frag needed: %+v", ev)
	}
	if ev, _ := parseICMP(false, append([]byte{V4ParamProb, 0, 0, 0, 22, 0, 0, 0}, echo...)); ev.Info != 22 {
		t.Errorf("param problem: %+v", ev)
	}
	if ev, ok := parseICMP(true, []byte{V6EchoReply, 0, 0, 0, 0, 1, 0, 2, 'x'}); !ok || ev.Kind != KindReply || ev.ID != 1 || ev.Seq != 2 {
		t.Errorf("v6 reply: %+v", ev)
	}
	if _, ok := parseICMP(true, []byte{V6Echo, 0, 0, 0, 0, 1, 0, 2}); ok {
		t.Error("v6 echo request accepted")
	}
	if !(&Event{Kind: KindError, Type: V4TimeExceeded}).IsTimeExceeded(false) ||
		(&Event{Kind: KindError, Type: V4TimeExceeded, Local: true}).IsTimeExceeded(false) ||
		(&Event{Kind: KindReply}).IsTimeExceeded(false) {
		t.Error("IsTimeExceeded")
	}
}

func TestStripIPv4(t *testing.T) {
	if b, ttl, _, ok := stripIPv4([]byte{V4EchoReply, 0, 0, 0, 0, 0, 0, 0}); !ok || ttl != -1 || len(b) != 8 {
		t.Error("headerless packet must pass through")
	}
	bad := make([]byte, 24)
	bad[0] = 0x4f // IHL 60 > len
	if _, _, _, ok := stripIPv4(bad); ok {
		t.Error("truncated header accepted")
	}
}

func TestChecksumOddLength(t *testing.T) {
	b := []byte{0x08, 0x00, 0x00, 0x00, 0x01}
	cs := checksum(b)
	binary.BigEndian.PutUint16(b[2:], cs)
	if checksum(b) != 0 {
		t.Fatal("odd-length checksum does not verify")
	}
}

func TestParseRecordRouteEdgeCases(t *testing.T) {
	for _, opts := range [][]byte{{0}, {1, 1, 7}, {7, 1}, {7, 3, 4}, {68, 4, 5, 0}} {
		if r := parseRecordRoute(opts); len(r) != 0 {
			t.Errorf("%v: unexpected route %v", opts, r)
		}
	}
}

func TestParseExtErrV6AndShort(t *testing.T) {
	if _, ok := parseExtErr(make([]byte, 8)); ok {
		t.Error("short extended error accepted")
	}
	b := make([]byte, 16+28)
	b[4], b[5] = eeOriginICMP6, V6TimeExceeded
	binary.NativeEndian.PutUint16(b[16:], 10)
	b[16+8], b[16+23] = 0xfe, 0x01
	e, ok := parseExtErr(b)
	if !ok || e.Offender.String() != "fe00::1" {
		t.Fatalf("v6 offender: %+v", e)
	}
}

func TestConfigWarnAndHasErrQueue(t *testing.T) {
	var w []string
	c := Config{Warnings: &w}
	c.warn("x")
	(&Config{}).warn("ignored without a sink")
	if len(w) != 1 {
		t.Fatal("warning not collected")
	}
	if HasErrQueue(nil) {
		t.Fatal("nil conn has no error queue")
	}
}
