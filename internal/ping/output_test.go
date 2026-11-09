package ping

import (
	"bytes"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/icmp"
	"github.com/mbovo/advp/internal/icmptest"
	"github.com/mbovo/advp/internal/report"
)

var dst4 = net.IPv4(192, 0, 2, 10).To4()

func TestTimestampsAudiblePrecise(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.NoReport = 2, ms(10), true, true
		o.Timestamps, o.Audible, o.Precise = true, true, true
	}, icmptest.EchoReply(dst4, 12345*time.Microsecond, 50), 0)
	p.Run()
	s := out.String()
	if !regexp.MustCompile(`\[\d{10}\.\d{6}\] 64 bytes from 192\.0\.2\.10: icmp_seq=1 ttl=50 time=12\.\d{3} ms\a`).MatchString(s) {
		t.Fatalf("unexpected reply line:\n%q", s)
	}
}

func TestOutstandingAndVerbose(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.Linger, o.LingerSet, o.NoReport = 3, ms(30), true, ms(100), true, true
		o.Outstanding, o.Verbose = true, true
	}, func(seq uint16) (icmp.Event, time.Duration, bool) {
		if seq == 1 {
			return icmp.Event{}, 0, false
		}
		return icmp.Event{Kind: icmp.KindReply, From: dst4, Size: 64}, time.Millisecond, true
	}, 0)
	fc := p.conn.(*icmptest.Conn)
	fc.Inject(icmp.Event{Kind: icmp.KindOther, Type: 13, From: dst4, Size: 20}, 5*time.Millisecond)
	fc.Inject(icmp.Event{Kind: icmp.KindReply, Seq: 999, From: dst4, Size: 64}, 5*time.Millisecond)
	p.Run()
	s := out.String()
	for _, want := range []string{"no answer yet for icmp_seq=1", "From 192.0.2.10: icmp type=13 code=0 size=20", "icmp_seq=999 (stray reply)"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
}

func TestRecordRouteOutput(t *testing.T) {
	route := []net.IP{net.IPv4(10, 0, 0, 1), net.IPv4(10, 0, 0, 2)}
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.NoReport, o.RecordRoute = 2, ms(10), true, true, true
	}, func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindReply, From: dst4, Size: 64, Route: route}, time.Millisecond, true
	}, 0)
	p.Run()
	s := out.String()
	if !strings.Contains(s, "RR: \t10.0.0.1\n\t10.0.0.2") || !strings.Contains(s, "\t(same route)") {
		t.Fatalf("record route output:\n%s", s)
	}
	if !strings.Contains(s, "PING test.example (192.0.2.10) 56(124) bytes of data.") {
		t.Fatalf("header must account for IP options:\n%s", s)
	}
}

func TestFloodOutputAndSendErrors(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) {
		o.Flood, o.Interval, o.Count, o.Linger, o.LingerSet, o.NoReport = true, 0, 20, ms(100), true, true
	}, icmptest.EchoReply(dst4, time.Millisecond, 0), 0)
	p.conn.(*icmptest.Conn).SetSendError(func(seq uint16) error {
		if seq == 5 {
			return errors.New("no buffer space available")
		}
		return nil
	})
	p.Run()
	s := out.String()
	body := s[strings.Index(s, "\n")+1 : strings.Index(s, "--- ")]
	if !strings.Contains(body, ".") || !strings.Contains(body, "\b \b") || !strings.Contains(body, "E") {
		t.Fatalf("flood output should show dots, backspaces and E:\n%q", body)
	}
	if !strings.Contains(s, "+1 errors") {
		t.Fatalf("send error not counted:\n%s", s)
	}
}

func TestSendErrorMessage(t *testing.T) {
	p, _, errOut := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.Linger, o.LingerSet, o.NoReport = 2, ms(10), true, ms(50), true, true
	}, icmptest.EchoReply(dst4, time.Millisecond, 0), 0)
	p.conn.(*icmptest.Conn).SetSendError(func(uint16) error { return syscall.ENETUNREACH })
	if code := p.Run(); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "advp: sendmsg: network is unreachable") {
		t.Fatalf("stderr: %q", errOut.String())
	}
}

func TestStatusSignalAndInterrupt(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	opt := DefaultOptions()
	opt.Interval, opt.IntervalSet, opt.Numeric, opt.Quiet, opt.NoReport = ms(10), true, true, true, true
	var out, errOut bytes.Buffer
	p := New(Config{Options: opt, Host: "h", Dst: dst4, Conn: icmptest.New(1, icmptest.EchoReply(dst4, time.Millisecond, 0)),
		Stdout: &out, Stderr: &errOut, Signals: sigs, SockKind: "fake"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		sigs <- syscall.SIGQUIT
		time.Sleep(50 * time.Millisecond)
		sigs <- os.Interrupt
	}()
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !regexp.MustCompile(`\r\d+/\d+ packets, \d+% loss, min/avg/ewma/max = [\d.]+/[\d.]+/[\d.]+/[\d.]+ ms`).MatchString(errOut.String()) {
		t.Fatalf("status line: %q", errOut.String())
	}
}

func TestDeadlineExitCodes(t *testing.T) {
	// -c not reached before -w → exit 1 even with replies.
	p, _, _ := testPinger(t, func(o *Options) {
		o.Count, o.Deadline, o.Interval, o.IntervalSet, o.Quiet, o.NoReport = 100, ms(150), ms(20), true, true, true
	}, icmptest.EchoReply(dst4, time.Millisecond, 0), 0)
	if code := p.Run(); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	// With -w, an ICMP error ends the run like iputils.
	p, out, _ := testPinger(t, func(o *Options) {
		o.Deadline, o.Interval, o.IntervalSet, o.NoReport = 5*time.Second, ms(20), true, true
	}, func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindError, Type: icmp.V4Unreach, Code: 0, From: net.IPv4(10, 0, 0, 1).To4()}, time.Millisecond, true
	}, 0)
	start := time.Now()
	if code := p.Run(); code != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("exit %d after %v", code, time.Since(start))
	}
	if !strings.Contains(out.String(), "Destination Net Unreachable") {
		t.Fatal(out.String())
	}
}

func TestPreloadAndPipe(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Preload, o.Interval, o.IntervalSet, o.Quiet, o.NoReport = 3, 3, time.Second, true, true, true
	}, icmptest.EchoReply(dst4, 20*time.Millisecond, 0), 0)
	start := time.Now()
	p.Run()
	if time.Since(start) > 900*time.Millisecond {
		t.Fatal("preloaded probes were not sent at once")
	}
	if !strings.Contains(out.String(), ", pipe 3") {
		t.Fatalf("pipe not reported:\n%s", out.String())
	}
}

func TestIPv6Header(t *testing.T) {
	opt := DefaultOptions()
	opt.Count, opt.Interval, opt.IntervalSet, opt.Numeric, opt.NoReport = 1, ms(10), true, true, true
	var out bytes.Buffer
	p := New(Config{Options: opt, Host: "::1", Dst: net.IPv6loopback,
		Conn: icmptest.New(1, icmptest.EchoReply(net.IPv6loopback, time.Millisecond, 64)), Stdout: &out, Stderr: &out,
		Signals: make(chan os.Signal), SockKind: "fake"})
	p.Run()
	if !strings.Contains(out.String(), "PING ::1 (::1) 56 data bytes") || !strings.Contains(out.String(), "from ::1: icmp_seq=1 ttl=64") {
		t.Fatal(out.String())
	}
}

func TestDuplicatesInFlood(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.NoReport = 3, ms(10), true, true
	}, nil, 0)
	fc := p.conn.(*icmptest.Conn)
	fc.SetReply(func(seq uint16) (icmp.Event, time.Duration, bool) {
		ev := icmp.Event{Kind: icmp.KindReply, From: dst4, Size: 64, Seq: seq}
		fc.Inject(ev, 3*time.Millisecond)
		fc.Inject(ev, 4*time.Millisecond)
		return ev, time.Millisecond, true
	})
	p.Run()
	// The run stops at the -c-th reply (iputils), so only earlier probes are
	// guaranteed to have their duplicates counted.
	for _, seq := range []string{"1", "2"} {
		if got := len(regexp.MustCompile(`icmp_seq=`+seq+` time=[\d.]+ ms \(DUP!\)`).FindAllString(out.String(), -1)); got != 2 {
			t.Fatalf("icmp_seq=%s: %d duplicates printed, want 2\n%s", seq, got, out)
		}
	}
	if !regexp.MustCompile(`\+[4-6] duplicates`).MatchString(out.String()) {
		t.Fatal(out.String())
	}
}

func TestHelpers(t *testing.T) {
	p := New(Config{Options: DefaultOptions(), Host: "192.0.2.10", Dst: dst4, Conn: icmptest.New(1, nil), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if p.targetString() != "192.0.2.10" || p.fmtAddr(nil, true, 0) != "?" {
		t.Fatal("targetString / fmtAddr(nil)")
	}
	if p.tsPrefix(time.Now()) != "" {
		t.Fatal("tsPrefix without -D")
	}
	p.dash = report.NewDashboard(&bytes.Buffer{}, false)
	p.eprintf("diag %d\n", 1)
	if len(p.recent) != 1 || p.recent[0] != "diag 1" {
		t.Fatalf("eprintf in live mode must go to the event log: %v", p.recent)
	}
	for i := 0; i < 60; i++ {
		p.printf("line %d\n", i)
	}
	if len(p.recent) != 50 || p.recent[49] != "line 59" {
		t.Fatalf("event log must keep the last 50 lines, got %d", len(p.recent))
	}
}

func TestTraceNote(t *testing.T) {
	cases := []struct {
		v6        bool
		typ, code int
		want      string
	}{
		{false, icmp.V4Unreach, 0, "!N"}, {false, icmp.V4Unreach, 1, "!H"}, {false, icmp.V4Unreach, 2, "!P"},
		{false, icmp.V4Unreach, 4, "!F"}, {false, icmp.V4Unreach, 13, "!X"}, {false, icmp.V4Unreach, 7, "!<7>"},
		{false, icmp.V4ParamProb, 0, "!T12"},
		{true, icmp.V6Unreach, 0, "!N"}, {true, icmp.V6Unreach, 1, "!X"}, {true, icmp.V6Unreach, 3, "!H"},
		{true, icmp.V6Unreach, 5, "!<5>"}, {true, icmp.V6ParamProb, 0, "!T4"},
	}
	for _, c := range cases {
		if got := traceNote(c.v6, icmp.Event{Type: c.typ, Code: c.code}); got != c.want {
			t.Errorf("v6=%v %d/%d: %q, want %q", c.v6, c.typ, c.code, got, c.want)
		}
	}
}

func TestNameCache(t *testing.T) {
	c := newNameCache()
	first := c.Lookup("127.0.0.1", 0) // starts the lookup, does not wait
	got := c.Lookup("127.0.0.1", 3*time.Second)
	if first != "" && first != got {
		t.Fatalf("inconsistent cached names %q / %q", first, got)
	}
	if got == "" {
		t.Skip("no reverse DNS for 127.0.0.1 in this environment")
	}
	var nilCache *nameCache
	if nilCache.Lookup("127.0.0.1", time.Second) != "" {
		t.Fatal("nil cache must not resolve")
	}
}
