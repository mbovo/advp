package ping

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/icmp"
	"github.com/mbovo/advp/internal/report"
)

// fakeConn simulates the network: each sent echo produces (or not) an event
// after a simulated delay.
type fakeConn struct {
	id    uint16
	ch    chan icmp.Event
	reply func(seq uint16) (icmp.Event, time.Duration, bool)
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

func (f *fakeConn) Read(timeout time.Duration) (icmp.Event, bool, error) {
	select {
	case ev := <-f.ch:
		return ev, true, nil
	case <-time.After(timeout):
		return icmp.Event{}, false, nil
	}
}
func (f *fakeConn) ID() uint16     { return f.id }
func (f *fakeConn) FilterID() bool { return true }
func (f *fakeConn) Raw() bool      { return false }
func (f *fakeConn) Close() error   { return nil }

func newFake(id uint16, reply func(seq uint16) (icmp.Event, time.Duration, bool)) *fakeConn {
	return &fakeConn{id: id, ch: make(chan icmp.Event, 1024), reply: reply}
}

func testPinger(t *testing.T, setOpts func(o *Options), mainReply func(uint16) (icmp.Event, time.Duration, bool), hops int) (*Pinger, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	opt := DefaultOptions()
	opt.Host, opt.Numeric = "h", true
	setOpts(opt)
	var hcs []icmp.Conn
	for ttl := 1; ttl <= hops; ttl++ {
		hcs = append(hcs, newFake(uint16(100+ttl), func(seq uint16) (icmp.Event, time.Duration, bool) {
			from := net.IPv4(10, 0, 0, byte(ttl)).To4()
			if ttl == 2 && seq > 2 {
				from = net.IPv4(10, 0, 1, 2).To4() // route change on hop 2
			}
			if ttl == 2 && seq%4 == 0 {
				return icmp.Event{}, 0, false // rate-limited hop
			}
			return icmp.Event{Kind: icmp.KindError, Type: icmp.V4TimeExceeded, From: from, Size: 36}, time.Duration(ttl) * time.Millisecond, true
		}))
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	p := New(Config{
		Options: opt, Host: "test.example", Dst: net.IPv4(192, 0, 2, 10).To4(), IsName: true,
		Conn: newFake(42, mainReply), HopConns: hcs, Stdout: out, Stderr: errOut,
		Signals: make(chan os.Signal), SockKind: "fake",
	})
	return p, out, errOut
}

func ms(n float64) time.Duration { return time.Duration(n * float64(time.Millisecond)) }

func TestPingerEndToEnd(t *testing.T) {
	reply := func(seq uint16) (icmp.Event, time.Duration, bool) {
		if seq == 3 || seq == 7 {
			return icmp.Event{}, 0, false // lost
		}
		ev := icmp.Event{Kind: icmp.KindReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64, TTL: 57}
		return ev, time.Duration(2+seq%5) * time.Millisecond, true
	}
	dir := t.TempDir()
	p, out, _ := testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.Linger, o.LingerSet, o.HopInterval = 20, ms(20), true, ms(200), true, ms(100)
		o.CSV, o.HTML, o.Color = filepath.Join(dir, "x.csv"), filepath.Join(dir, "x.html"), "never"
	}, reply, 2)
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
	reply := func(seq uint16) (icmp.Event, time.Duration, bool) {
		if seq == 2 {
			return icmp.Event{Kind: icmp.KindError, Type: icmp.V4Unreach, Code: 1, From: net.IPv4(10, 0, 0, 1).To4()}, time.Millisecond, true
		}
		return icmp.Event{Kind: icmp.KindReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, time.Millisecond, true
	}
	p, out, _ := testPinger(t, func(o *Options) { o.Count, o.Interval, o.IntervalSet, o.NoReport = 4, ms(10), true, true }, reply, 0)
	// Inject a duplicate for seq 1.
	fc := p.conn.(*fakeConn)
	orig := fc.reply
	fc.reply = func(seq uint16) (icmp.Event, time.Duration, bool) {
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
	p, out, _ = testPinger(t, func(o *Options) {
		o.Count, o.Interval, o.IntervalSet, o.Linger, o.LingerSet, o.Quiet = 2, ms(10), true, ms(100), true, true
	}, func(uint16) (icmp.Event, time.Duration, bool) { return icmp.Event{}, 0, false }, 0)
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
	reply := func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, time.Duration(rand.IntN(3)+1) * time.Millisecond, true
	}
	p, out, _ := testPinger(t, func(o *Options) { o.Deadline, o.Interval, o.IntervalSet, o.Quiet = ms(300), ms(20), true, true }, reply, 1)
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

func fastReply(d time.Duration) func(uint16) (icmp.Event, time.Duration, bool) {
	return func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindReply, From: net.IPv4(192, 0, 2, 10).To4(), Size: 64}, d, true
	}
}

func TestAdaptivePacing(t *testing.T) {
	// Non-root -A: replies arrive after 5ms, so sends happen every 200ms (not every 1s).
	p, _, _ := testPinger(t, func(o *Options) { o.Adaptive, o.Deadline, o.Quiet, o.NoReport = true, ms(1050), true, true }, fastReply(5*time.Millisecond), 0)
	p.Run()
	if p.ntransmitted < 5 || p.ntransmitted > 7 {
		t.Fatalf("adaptive sent %d probes in ~1s, want ~6", p.ntransmitted)
	}
}

func TestFloodPacing(t *testing.T) {
	p, _, _ := testPinger(t, func(o *Options) { o.Flood, o.Interval, o.Deadline, o.Quiet, o.NoReport = true, 0, ms(300), true, true }, fastReply(time.Millisecond), 0)
	p.Run()
	if p.ntransmitted < 60 {
		t.Fatalf("flood sent only %d probes in 0.3s", p.ntransmitted)
	}
}

func TestLocalErrorEvent(t *testing.T) {
	p, out, errOut := testPinger(t, func(o *Options) { o.Count, o.Interval, o.IntervalSet, o.NoReport = 2, ms(10), true, true }, fastReply(time.Millisecond), 0)
	fc := p.conn.(*fakeConn)
	fc.ch <- icmp.Event{Kind: icmp.KindError, Local: true, Errno: 90, Info: 1500, TTL: -1}
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

func TestLiveRendersEveryPacket(t *testing.T) {
	p, out, _ := testPinger(t, func(o *Options) { o.Count, o.Interval, o.IntervalSet, o.Live = 5, ms(100), true, true }, fastReply(2*time.Millisecond), 1)
	p.dash = report.NewDashboard(p.out, false)
	if code := p.Run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if p.frames < 5 {
		t.Fatalf("only %d frames for 5 probes", p.frames)
	}
	s := out.String()
	enter, leave := strings.Index(s, "\x1b[?1049h"), strings.LastIndex(s, "\x1b[?1049l")
	if enter < 0 || leave < enter || !strings.Contains(s[leave:], "advp report") {
		t.Fatalf("final report must follow the live view")
	}
	if !strings.Contains(s[enter:leave], "icmp_seq 5") {
		t.Fatalf("live frames never showed the last probe")
	}
}

func TestLiveKeys(t *testing.T) {
	r, w := io.Pipe()
	p, out, _ := testPinger(t, func(o *Options) { o.Interval, o.IntervalSet, o.Live = ms(20), true, true }, fastReply(time.Millisecond), 0)
	p.dash = report.NewDashboard(p.out, false)
	p.keysIn = r
	go func() {
		time.Sleep(100 * time.Millisecond)
		w.Write([]byte("v"))
		time.Sleep(100 * time.Millisecond)
		w.Write([]byte("q"))
	}()
	start := time.Now()
	p.Run()
	if time.Since(start) > 2*time.Second {
		t.Fatal("q did not stop the session")
	}
	if p.view != report.ViewChart || !strings.Contains(out.String(), "view: chart") {
		t.Fatalf("view key not applied (view=%v)", p.view)
	}
}

func TestLivePauseStopsRedraws(t *testing.T) {
	r, w := io.Pipe()
	p, _, _ := testPinger(t, func(o *Options) { o.Interval, o.IntervalSet, o.Live = ms(10), true, true }, fastReply(time.Millisecond), 0)
	p.dash = report.NewDashboard(p.out, false)
	p.keysIn = r
	go func() {
		w.Write([]byte("p"))
		time.Sleep(400 * time.Millisecond)
		w.Write([]byte("q"))
	}()
	p.Run()
	if p.ntransmitted < 20 {
		t.Fatalf("probing must continue while paused, sent %d", p.ntransmitted)
	}
	if p.frames > 5 {
		t.Fatalf("%d frames drawn while paused", p.frames)
	}
}
