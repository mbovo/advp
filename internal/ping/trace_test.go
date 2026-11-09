package ping

import (
	"bytes"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/icmp"
	"github.com/mbovo/advp/internal/icmptest"
)

var dstIP = net.IPv4(192, 0, 2, 10).To4()

// route describes a simulated path for hop sockets.
type route struct {
	dest      int          // hop number of the destination (0 = never reached)
	silent    map[int]bool // hops that never answer
	unreachAt int          // hop that answers Destination Host Unreachable
}

func (r route) conns(n int) []icmp.Conn {
	var cs []icmp.Conn
	for ttl := 1; ttl <= n; ttl++ {
		cs = append(cs, icmptest.New(uint16(200+ttl), func(seq uint16) (icmp.Event, time.Duration, bool) {
			d := time.Duration(ttl) * time.Millisecond
			switch {
			case r.silent[ttl]:
				return icmp.Event{}, 0, false
			case ttl == r.unreachAt:
				return icmp.Event{Kind: icmp.KindError, Type: icmp.V4Unreach, Code: 1, From: net.IPv4(10, 0, 0, byte(ttl)).To4()}, d, true
			case r.dest > 0 && ttl >= r.dest:
				return icmp.Event{Kind: icmp.KindReply, From: dstIP, Size: 64}, d, true
			case r.dest == 0 && ttl > 4:
				return icmp.Event{}, 0, false
			}
			return icmp.Event{Kind: icmp.KindError, Type: icmp.V4TimeExceeded, From: net.IPv4(10, 0, 0, byte(ttl)).To4()}, d, true
		}))
	}
	return cs
}

func tracePinger(t *testing.T, r route, maxHops int) (*Pinger, *bytes.Buffer) {
	t.Helper()
	opt := DefaultOptions()
	opt.Host, opt.Numeric, opt.HopsMax = "h", true, maxHops
	opt.Count, opt.Interval, opt.IntervalSet, opt.HopInterval = 3, ms(20), true, ms(100)
	out := &bytes.Buffer{}
	p := New(Config{
		Options: opt, Host: "test.example", Dst: dstIP, IsName: true,
		Conn: icmptest.New(42, fastReply(5*time.Millisecond)), HopConns: r.conns(maxHops),
		Stdout: out, Stderr: &bytes.Buffer{}, Signals: make(chan os.Signal), SockKind: "fake",
	})
	p.traceGap, p.traceWait = 10*time.Millisecond, 150*time.Millisecond
	return p, out
}

func TestTraceReachesDestination(t *testing.T) {
	p, out := tracePinger(t, route{dest: 6, silent: map[int]bool{3: true}}, 30)
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	s := out.String()
	for _, want := range []string{
		"traceroute to test.example (192.0.2.10), 30 hops max, 3 probes per hop",
		" 1  10.0.0.1  1.",
		" 3  * * *",
		" 6  192.0.2.10  6.",
		"(destination)",
		"→ destination reached at hop 6; monitoring hops 1-5 every 100ms",
		"PING test.example",
		"Route to destination (5 hops traced)",
		"destination at hop 6 (traced)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
	if strings.Contains(s, " 7  ") {
		t.Error("trace printed hops past the destination")
	}
	if len(p.hops) != 5 {
		t.Fatalf("monitoring %d hops, want 5", len(p.hops))
	}
	if strings.Contains(s, "[hop 1]") {
		t.Error("hops seen during the trace must not be announced again")
	}
	if p.hops[0].series.Last() == nil || p.hops[4].series.Last() == nil {
		t.Error("kept hops were not probed during the session")
	}
}

func TestTraceStopsAtUnreachable(t *testing.T) {
	p, out := tracePinger(t, route{dest: 8, unreachAt: 4}, 10)
	p.Run()
	s := out.String()
	if !strings.Contains(s, " 4  10.0.0.4  4.") || !strings.Contains(s, "!H") ||
		!strings.Contains(s, "→ hop 4 answered !H; monitoring hops 1-4") || len(p.hops) != 4 {
		t.Fatalf("unexpected trace (hops=%d):\n%s", len(p.hops), s)
	}
}

func TestTraceDestinationNotReached(t *testing.T) {
	p, out := tracePinger(t, route{}, 8)
	start := time.Now()
	p.Run()
	s := out.String()
	if !strings.Contains(s, "→ destination not reached within 8 hops; monitoring hops 1-4") || len(p.hops) != 4 {
		t.Fatalf("unexpected trace (hops=%d):\n%s", len(p.hops), s)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("trace did not respect its timeout")
	}
}

func TestTraceDirectNeighbour(t *testing.T) {
	p, out := tracePinger(t, route{dest: 1}, 5)
	p.Run()
	if !strings.Contains(out.String(), "no intermediate hops to monitor") || len(p.hops) != 0 {
		t.Fatalf("unexpected trace:\n%s", out)
	}
}

func TestTraceQuiet(t *testing.T) {
	p, out := tracePinger(t, route{dest: 3}, 5)
	p.opt.Quiet = true
	p.Run()
	s := out.String()
	if strings.Contains(s, "traceroute to") || !strings.Contains(s, "→ destination reached at hop 3") {
		t.Fatalf("quiet trace output:\n%s", s)
	}
}
