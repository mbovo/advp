package main

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConn simulates the network: each sent echo produces (or not) an event
// after a simulated delay.
type fakeConn struct {
	id    uint16
	ch    chan Event
	reply func(seq uint16) (Event, time.Duration, bool)
	mu    sync.Mutex
	sent  int
}

func (f *fakeConn) Send(pkt []byte) (time.Time, error) {
	now := time.Now()
	seq := binary.BigEndian.Uint16(pkt[6:])
	f.mu.Lock()
	f.sent++
	f.mu.Unlock()
	if ev, d, ok := f.reply(seq); ok {
		ev.ID, ev.Seq, ev.At = f.id, seq, now.Add(d)
		if ev.TTL == 0 {
			ev.TTL = -1
		}
		time.AfterFunc(d, func() { f.ch <- ev })
	}
	return now, nil
}

func (f *fakeConn) Read(timeout time.Duration) (Event, bool, error) {
	select {
	case ev := <-f.ch:
		return ev, true, nil
	case <-time.After(timeout):
		return Event{}, false, nil
	}
}
func (f *fakeConn) ID() uint16     { return f.id }
func (f *fakeConn) FilterID() bool { return true }
func (f *fakeConn) Raw() bool      { return false }
func (f *fakeConn) Close() error   { return nil }

func newFake(id uint16, reply func(seq uint16) (Event, time.Duration, bool)) *fakeConn {
	return &fakeConn{id: id, ch: make(chan Event, 1024), reply: reply}
}

func testPinger(t *testing.T, args []string, mainReply func(uint16) (Event, time.Duration, bool), hops int) (*Pinger, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	opt, err := parseArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	dst := net.IPv4(192, 0, 2, 10).To4()
	var hs []*hopState
	for ttl := 1; ttl <= hops; ttl++ {
		ttl := ttl
		hs = append(hs, &hopState{ttl: ttl, series: newSeries(), conn: newFake(uint16(100+ttl), func(seq uint16) (Event, time.Duration, bool) {
			from := net.IPv4(10, 0, 0, byte(ttl)).To4()
			if ttl == 2 && seq > 2 {
				from = net.IPv4(10, 0, 1, 2).To4() // route change on hop 2
			}
			if ttl == 2 && seq%4 == 0 {
				return Event{}, 0, false // rate-limited hop
			}
			return Event{Kind: EvError, Type: icmp4TimeExceeded, From: from, Size: 36}, time.Duration(ttl) * time.Millisecond, true
		})})
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	p := &Pinger{
		opt: opt, out: out, errOut: errOut, host: "test.example", dst: dst, isName: true,
		conn: newFake(42, mainReply), hops: hs, names: nil, sigs: make(chan os.Signal), sockKind: "fake",
	}
	opt.Numeric = true
	return p, out, errOut
}

func TestPingerEndToEnd(t *testing.T) {
	reply := func(seq uint16) (Event, time.Duration, bool) {
		if seq == 3 || seq == 7 {
			return Event{}, 0, false // lost
		}
		ev := Event{Kind: EvReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64, TTL: 57}
		return ev, time.Duration(2+seq%5) * time.Millisecond, true
	}
	dir := t.TempDir()
	p, out, _ := testPinger(t, []string{"-c", "20", "-i", "0.02", "-W", "0.2", "--hop-interval", "0.1",
		"--csv", filepath.Join(dir, "x.csv"), "--html", filepath.Join(dir, "x.html"), "--color", "never", "h"}, reply, 2)
	code := p.Run()
	s := out.String()
	if code != 0 {
		t.Fatalf("exit code %d\n%s", code, s)
	}
	for _, want := range []string{
		"PING test.example (192.0.2.10) 56(84) bytes of data.",
		"64 bytes from 192.0.2.10: icmp_seq=1 ttl=57 time=",
		"--- test.example ping statistics ---",
		"20 packets transmitted, 18 received, 10% packet loss",
		"rtt min/avg/max/mdev = ",
		"[hop 1] 10.0.0.1 time=",
		"[hop changed: 2] 10.0.1.2",
		"advp report",
		"First 2 hop(s) vs destination",
		"Timeline",
		"RTT over time (ms)",
		"hop 1 │",
		"Outages",
		"CSV written to", "HTML report written to",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n%s", want, s)
		}
	}
	csv, _ := os.ReadFile(filepath.Join(dir, "x.csv"))
	if n := strings.Count(string(csv), "\ntarget,"); n != 20 {
		t.Errorf("csv target rows = %d", n)
	}
	html, _ := os.ReadFile(filepath.Join(dir, "x.html"))
	if !bytes.Contains(html, []byte("<svg")) || !bytes.Contains(html, []byte("Average RTT per hop")) {
		t.Errorf("html report incomplete")
	}
}

func TestPingerDupsErrorsAndExitCodes(t *testing.T) {
	reply := func(seq uint16) (Event, time.Duration, bool) {
		if seq == 2 {
			return Event{Kind: EvError, Type: icmp4Unreach, Code: 1, From: net.IPv4(10, 0, 0, 1).To4()}, time.Millisecond, true
		}
		return Event{Kind: EvReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, time.Millisecond, true
	}
	p, out, _ := testPinger(t, []string{"-c", "4", "-i", "0.01", "--no-report", "h"}, reply, 0)
	// Inject a duplicate for seq 1.
	fc := p.conn.(*fakeConn)
	orig := fc.reply
	fc.reply = func(seq uint16) (Event, time.Duration, bool) {
		if seq == 1 {
			ev, d, ok := orig(seq)
			ev.ID, ev.Seq, ev.At, ev.TTL = fc.id, seq, time.Now().Add(2*d), -1
			time.AfterFunc(2*d, func() { fc.ch <- ev })
			return ev, d, ok
		}
		return orig(seq)
	}
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	s := out.String()
	for _, want := range []string{"(DUP!)", "From 10.0.0.1 icmp_seq=2 Destination Host Unreachable", "+1 duplicates, +1 errors", "4 packets transmitted, 3 received"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "advp report") {
		t.Errorf("--no-report ignored")
	}

	// No reply at all → exit code 1, and linger (-W) bounds the wait.
	p, out, _ = testPinger(t, []string{"-c", "2", "-i", "0.01", "-W", "0.1", "-q", "h"}, func(uint16) (Event, time.Duration, bool) { return Event{}, 0, false }, 0)
	start := time.Now()
	if code := p.Run(); code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("linger not honoured")
	}
	if !strings.Contains(out.String(), "100% packet loss") {
		t.Fatalf("summary: %s", out.String())
	}
}

func TestPingerDeadline(t *testing.T) {
	reply := func(uint16) (Event, time.Duration, bool) {
		return Event{Kind: EvReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, time.Duration(rand.IntN(3)+1) * time.Millisecond, true
	}
	p, out, _ := testPinger(t, []string{"-w", "0.3", "-i", "0.02", "-q", "h"}, reply, 1)
	start := time.Now()
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if el := time.Since(start); el < 250*time.Millisecond || el > 2*time.Second {
		t.Fatalf("deadline not honoured: %v", el)
	}
	if strings.Contains(out.String(), "bytes from") {
		t.Fatalf("-q printed replies")
	}
}

func TestReportEdgeCases(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, rtts := range [][]int{{}, {-1}, {5}, {-1, -1, -1}, {1, 2, 3, 400, 2, 1}} {
		in := &reportInput{Host: "h", IP: "192.0.2.1", Target: "h", Start: t0, End: t0.Add(time.Duration(len(rtts)+1) * time.Second),
			Now: t0.Add(time.Hour), Interval: time.Second, Size: 56, SockKind: "fake", Main: mkSamples(t0, rtts, time.Second),
			Hops: []hopView{{TTL: 1, Samples: mkSamples(t0, rtts, time.Second)}}, ChartHeight: 5, Width: 60, Color: true}
		var b bytes.Buffer
		writeReport(&b, in)
		if !strings.Contains(b.String(), "Timeline") {
			t.Errorf("rtts %v: no timeline", rtts)
		}
		var d bytes.Buffer
		(&dashboard{w: &d, st: style{on: true}}).begin()
	}
}

func TestTruncANSI(t *testing.T) {
	s := style{on: true}.red("hello") + " world"
	if got := visLen(truncANSI(s, 7)); got != 7 {
		t.Fatalf("visible len %d", got)
	}
}

func fastReply(d time.Duration) func(uint16) (Event, time.Duration, bool) {
	return func(uint16) (Event, time.Duration, bool) {
		return Event{Kind: EvReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, d, true
	}
}

func TestAdaptivePacing(t *testing.T) {
	// Non-root -A: replies arrive after 5ms, so sends happen every 200ms (not every 1s).
	p, _, _ := testPinger(t, []string{"-A", "-w", "1.05", "-q", "--no-report", "h"}, fastReply(5*time.Millisecond), 0)
	p.Run()
	if p.ntransmitted < 5 || p.ntransmitted > 7 {
		t.Fatalf("adaptive sent %d probes in ~1s, want ~6", p.ntransmitted)
	}
}

func TestFloodPacing(t *testing.T) {
	p, _, _ := testPinger(t, []string{"-f", "-w", "0.3", "-q", "--no-report", "h"}, fastReply(time.Millisecond), 0)
	p.Run()
	if p.ntransmitted < 60 {
		t.Fatalf("flood sent only %d probes in 0.3s", p.ntransmitted)
	}
}

func TestLocalErrorEvent(t *testing.T) {
	p, out, errOut := testPinger(t, []string{"-c", "2", "-i", "0.01", "--no-report", "h"}, fastReply(time.Millisecond), 0)
	fc := p.conn.(*fakeConn)
	fc.ch <- Event{Kind: EvError, Local: true, Errno: 90, Info: 1500, TTL: -1}
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "local error") {
		t.Fatalf("local error not printed: %q", errOut.String())
	}
	if strings.Contains(out.String(), "errors") || p.nreceived != 2 {
		t.Fatalf("local error must not hit a sample:\n%s", out.String())
	}
}
