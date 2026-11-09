package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestParseArgsGetoptStyle(t *testing.T) {
	o, err := parseArgs([]string{"-qnc5", "-i0.5", "example.com", "-W", "2", "-s", "100", "--hops=3", "--bucket", "30s", "-D"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Quiet || !o.Numeric || o.Count != 5 || o.Interval != 500*time.Millisecond || o.Linger != 2*time.Second ||
		o.Size != 100 || o.Host != "example.com" || o.Hops != 3 || o.Bucket != 30*time.Second || !o.Timestamps {
		t.Fatalf("unexpected options: %+v", o)
	}
	o, err = parseArgs([]string{"-f", "-p", "ff00", "-Q", "0x10", "-t", "7", "-M", "do", "--", "-host"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Flood || o.Interval != 0 || o.TOS != 16 || o.TTL != 7 || o.PMTU != "do" || o.Host != "-host" || len(o.Pattern) != 2 {
		t.Fatalf("unexpected options: %+v", o)
	}
	for _, bad := range [][]string{{}, {"-c", "0", "h"}, {"-t", "300", "h"}, {"-Z", "h"}, {"-4", "-6", "h"}, {"-M", "x", "h"}, {"-c"}, {"--nope", "h"}, {"-p", "zz", "h"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	if _, err := parseArgs([]string{"-h"}); err != errHelp {
		t.Errorf("want errHelp, got %v", err)
	}
	if o, _ := parseArgs([]string{"-T", "tsonly", "h"}); len(o.Warnings) != 1 {
		t.Errorf("expected a warning for -T")
	}
}

func TestEchoRoundTrip(t *testing.T) {
	pkt := buildEcho(false, 0x1234, 7, buildPayload(56, nil, time.Now()))
	if checksum(pkt) != 0 {
		t.Fatalf("bad checksum")
	}
	reply := append([]byte(nil), pkt...)
	reply[0] = icmp4EchoReply
	ev, ok := parseICMP(false, reply)
	if !ok || ev.Kind != EvReply || ev.ID != 0x1234 || ev.Seq != 7 || ev.Size != 64 {
		t.Fatalf("bad parse: %+v", ev)
	}
	if _, ok := parseICMP(false, pkt); ok {
		t.Fatalf("echo requests must be ignored")
	}
	p := buildPayload(20, []byte{0xab}, time.Now())
	if p[8] != 0xab || p[19] != 0xab {
		t.Fatalf("pattern not applied: %x", p)
	}
}

func TestTimeExceededV4(t *testing.T) {
	// Captured from a real TTL=2 probe on macOS (outer IP header + ICMP type 11).
	raw := []byte{0x45, 0xc0, 0x2c, 0x00, 0x6e, 0x26, 0x00, 0x00, 0x3f, 0x01, 0x36, 0x09, 0xc0, 0xa8, 0x01, 0xfe, 0x0a, 0x0a, 0x0a, 0x1e,
		0x0b, 0x00, 0xf4, 0xff, 0x00, 0x00, 0x00, 0x00,
		0x45, 0x00, 0x24, 0x00, 0xa3, 0x5c, 0x00, 0x00, 0x01, 0x01, 0x00, 0x54, 0x0a, 0x0a, 0x0a, 0x1e, 0x01, 0x01, 0x01, 0x01,
		0x08, 0x00, 0xa1, 0xf8, 0x12, 0x34, 0x00, 0x01, 0x68, 0x65, 0x6c, 0x6c, 0x6f, 0x00, 0x00, 0x00}
	icmp, ttl, _, ok := stripIPv4(raw)
	if !ok || ttl != 63 {
		t.Fatalf("strip: ok=%v ttl=%d", ok, ttl)
	}
	ev, ok := parseICMP(false, icmp)
	if !ok || ev.Kind != EvError || ev.ID != 0x1234 || ev.Seq != 1 || !ev.isTimeExceeded(false) {
		t.Fatalf("bad parse: %+v", ev)
	}
	if got := icmpErrorText(false, ev.Type, ev.Code, ev.Info); got != "Time to live exceeded" {
		t.Fatalf("text: %q", got)
	}
}

func TestTimeExceededV6(t *testing.T) {
	inner := make([]byte, 48)
	inner[0] = 0x60
	inner[6] = 58
	inner[40] = icmp6Echo
	binary.BigEndian.PutUint16(inner[44:], 0xbeef)
	binary.BigEndian.PutUint16(inner[46:], 42)
	msg := append([]byte{icmp6TimeExceeded, 0, 0, 0, 0, 0, 0, 0}, inner...)
	ev, ok := parseICMP(true, msg)
	if !ok || ev.Kind != EvError || ev.ID != 0xbeef || ev.Seq != 42 || !ev.isTimeExceeded(true) {
		t.Fatalf("bad parse: %+v", ev)
	}
	tooBig := append([]byte{icmp6TooBig, 0, 0, 0, 0, 0, 0x05, 0xdc}, inner...)
	ev, _ = parseICMP(true, tooBig)
	if got := icmpErrorText(true, ev.Type, ev.Code, ev.Info); got != "Packet too big: mtu=1500" {
		t.Fatalf("text: %q", got)
	}
}

func TestRecordRoute(t *testing.T) {
	hdr := make([]byte, 60)
	hdr[0] = 0x4f // IHL 15 → 60 bytes
	hdr[8] = 50
	opt := recordRouteOption()
	opt[3] = 12 // two addresses recorded
	copy(opt[4:], []byte{10, 0, 0, 1, 10, 0, 0, 2})
	copy(hdr[20:], opt)
	pkt := append(hdr, make([]byte, 8)...)
	_, ttl, route, ok := stripIPv4(pkt)
	if !ok || ttl != 50 || len(route) != 2 || !route[1].Equal(net.IPv4(10, 0, 0, 2)) {
		t.Fatalf("route=%v ttl=%d ok=%v", route, ttl, ok)
	}
}

func TestParseExtErr(t *testing.T) {
	b := make([]byte, 32)
	binary.NativeEndian.PutUint32(b[0:], 113)
	b[4], b[5], b[6] = eeOriginICMP, 11, 0
	binary.NativeEndian.PutUint16(b[16:], 2)
	copy(b[20:], []byte{192, 168, 1, 1})
	e, ok := parseExtErr(b)
	if !ok || e.Origin != eeOriginICMP || e.Type != 11 || !e.Offender.Equal(net.IPv4(192, 168, 1, 1)) {
		t.Fatalf("bad ext err: %+v", e)
	}
}

func TestFormatRTT(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Microsecond:     "0.045",
		1234 * time.Microsecond:   "1.23",
		12345 * time.Microsecond:  "12.3",
		123456 * time.Microsecond: "123",
	}
	for d, want := range cases {
		if got := formatRTT(d, false); got != want {
			t.Errorf("formatRTT(%v)=%s want %s", d, got, want)
		}
	}
	if got := formatRTT(12345*time.Microsecond, true); got != "12.345" {
		t.Errorf("precise: %s", got)
	}
}
