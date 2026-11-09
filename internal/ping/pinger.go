// Package ping implements an iputils compatible ping session that also
// probes the first hops and collects samples for the extended report.
package ping

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mbovo/advp/internal/icmp"
	"github.com/mbovo/advp/internal/report"
	"github.com/mbovo/advp/internal/stats"
	"github.com/mbovo/advp/internal/tty"
)

type connEvent struct {
	idx int // 0 = target socket, k = hop k
	ev  icmp.Event
	err error
}

type hopState struct {
	ttl    int
	conn   icmp.Conn
	series *stats.Series
	seq    int
	addrs  []report.HopAddr
	cur    string
}

func (h *hopState) note(ip string, at time.Time) (changed bool) {
	for i := range h.addrs {
		if h.addrs[i].IP == ip {
			h.addrs[i].Last = at
			h.addrs[i].Count++
			changed = h.cur != ip
			h.cur = ip
			return changed
		}
	}
	h.addrs = append(h.addrs, report.HopAddr{IP: ip, First: at, Last: at, Count: 1})
	h.cur = ip
	return true
}

// Pinger runs one ping session.
type Pinger struct {
	opt      *Options
	out      io.Writer
	errOut   io.Writer
	host     string
	dst      net.IP
	v6       bool
	isName   bool
	conn     icmp.Conn
	hops     []*hopState
	series   *stats.Series
	names    *nameCache
	sigs     <-chan os.Signal
	color    bool
	root     bool
	sockKind string

	start, end   time.Time
	ntransmitted int
	nreceived    int
	nrepeats     int
	nerrors      int
	ewma         float64 // iputils style: 8 * average, in ns
	tmax         time.Duration
	pipe         int
	lastSend     time.Time
	nextSend     time.Time
	sendTimer    *time.Timer
	lastRR       string
	stopping     bool
	sendingDone  bool

	events chan connEvent
	done   chan struct{}
	wg     sync.WaitGroup
	recent []string
	dash   *report.Dashboard

	// live view state
	keysIn    io.Reader
	keys      chan tty.Key
	view      report.View
	paused    bool
	dirty     bool
	lastFrame time.Time
	frameGap  time.Duration
	frames    int
}

// Config wires a Pinger to its sockets, output and environment.
type Config struct {
	Options *Options
	// Host is the destination as given by the user; Dst its resolved address.
	Host   string
	Dst    net.IP
	IsName bool
	// Conn is the socket used for the destination; HopConns[i] probes TTL i+1.
	Conn     icmp.Conn
	HopConns []icmp.Conn
	Stdout   io.Writer
	Stderr   io.Writer
	// Signals delivers SIGINT/SIGTERM (stop) and SIGQUIT (status line).
	Signals <-chan os.Signal
	Color   bool
	// Privileged relaxes the minimum interval like iputils does for root.
	Privileged bool
	// SockKind describes the socket type in the report.
	SockKind string
	// Dashboard enables the live full-screen view (Stdout must be a terminal).
	Dashboard bool
	// Keys is the keyboard input for the live view (optional, normally a
	// terminal in cbreak mode).
	Keys io.Reader
}

// New returns a Pinger ready to Run. The Pinger takes ownership of the
// connections and closes them when Run returns.
func New(c Config) *Pinger {
	p := &Pinger{
		opt: c.Options, out: c.Stdout, errOut: c.Stderr, host: c.Host, dst: c.Dst,
		v6: c.Dst.To4() == nil, isName: c.IsName, conn: c.Conn, sigs: c.Signals,
		color: c.Color, root: c.Privileged, sockKind: c.SockKind, names: newNameCache(), keysIn: c.Keys,
	}
	for i, hc := range c.HopConns {
		p.hops = append(p.hops, &hopState{ttl: i + 1, conn: hc, series: stats.NewSeries()})
	}
	if c.Dashboard {
		p.dash = report.NewDashboard(c.Stdout, c.Color)
	}
	return p
}

// Frame pacing for the live view: a frame is drawn after every send/reply,
// but never more often than frameGap, which adapts to the rendering cost.
const (
	minFrameGap = 20 * time.Millisecond
	maxFrameGap = time.Second
)

// renderDash draws a live frame now.
func (p *Pinger) renderDash(now time.Time) {
	state := "running"
	switch {
	case p.stopping:
		state = "stopping"
	case p.sendingDone:
		state = "waiting for last replies"
	}
	in := p.reportInput(now, false)
	in.End = now
	p.dash.Render(in, report.DashStatus{
		Title: p.targetString(), State: state, Elapsed: now.Sub(p.start), LastSeq: p.ntransmitted,
		Recent: p.recent, View: p.view, Paused: p.paused, Keys: p.keysIn != nil, Updated: now,
	})
	p.frames++
	p.dirty = false
	p.lastFrame = time.Now()
	p.frameGap = min(max(3*p.lastFrame.Sub(now), minFrameGap), maxFrameGap)
}

// frameNow draws immediately (used right after a send) when the frame budget
// allows; otherwise the pending paced frame will pick the change up.
func (p *Pinger) frameNow() {
	if p.dash != nil && !p.paused && time.Since(p.lastFrame) >= p.frameGap {
		p.renderDash(time.Now())
	}
}

// readKeys decodes keyboard input until EOF; it may stay blocked in Read
// after Run returns, which is harmless for a CLI.
func (p *Pinger) readKeys() {
	buf := make([]byte, 64)
	for {
		n, err := p.keysIn.Read(buf)
		for _, k := range tty.Decode(buf[:n]) {
			select {
			case p.keys <- k:
			case <-p.done:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Pinger) handleKey(k tty.Key) {
	switch k {
	case tty.KeyQuit:
		p.stopping = true
	case tty.KeyPause:
		p.paused = !p.paused
	case tty.KeyView:
		p.view = p.view.Next()
		p.dash.Clear()
	case tty.KeyRedraw:
		p.dash.Clear()
	}
	if !p.stopping {
		p.renderDash(time.Now())
	}
}

func (p *Pinger) targetString() string {
	if p.isName {
		return fmt.Sprintf("%s (%s)", p.host, p.dst)
	}
	return p.dst.String()
}

func (p *Pinger) fmtAddr(ip net.IP, resolve bool, wait time.Duration) string {
	if ip == nil {
		return "?"
	}
	if resolve {
		if n := p.names.Lookup(ip.String(), wait); n != "" && n != ip.String() {
			return fmt.Sprintf("%s (%s)", n, ip)
		}
	}
	return ip.String()
}

func (p *Pinger) resolveReplies() bool {
	return !p.opt.Numeric && (p.isName || p.opt.ForceRDNS)
}

// printf writes per-packet output; in dashboard mode lines go to the event log.
func (p *Pinger) printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if p.dash != nil {
		p.recent = append(p.recent, strings.TrimRight(s, "\n"))
		if len(p.recent) > 50 {
			p.recent = p.recent[len(p.recent)-50:]
		}
		return
	}
	io.WriteString(p.out, s)
}

// eprintf writes diagnostics to stderr, or to the event log in dashboard mode.
func (p *Pinger) eprintf(format string, a ...any) {
	if p.dash != nil {
		p.printf(format, a...)
		return
	}
	fmt.Fprintf(p.errOut, format, a...)
}

func (p *Pinger) tsPrefix(t time.Time) string {
	if !p.opt.Timestamps {
		return ""
	}
	return fmt.Sprintf("[%d.%06d] ", t.Unix(), t.Nanosecond()/1000)
}

func (p *Pinger) lostAfter() time.Duration {
	if p.opt.LingerSet {
		return p.opt.Linger
	}
	d := 2 * p.tmax
	if d < time.Second {
		d = time.Second
	}
	if d > p.opt.Linger {
		d = p.opt.Linger
	}
	return d
}

func (p *Pinger) hopInterval() time.Duration {
	if p.opt.HopInterval > 0 {
		return p.opt.HopInterval
	}
	if p.opt.Interval > time.Second {
		return p.opt.Interval
	}
	return time.Second
}

func (p *Pinger) canSend() bool {
	if p.stopping {
		return false
	}
	if p.opt.Count == 0 || p.opt.Deadline > 0 {
		return true
	}
	return p.ntransmitted < p.opt.Count
}

func (p *Pinger) reader(idx int, c icmp.Conn) {
	defer p.wg.Done()
	for {
		select {
		case <-p.done:
			return
		default:
		}
		ev, ok, err := c.Read(100 * time.Millisecond)
		if err != nil {
			select {
			case p.events <- connEvent{idx: idx, err: err}:
			case <-p.done:
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if ok {
			select {
			case p.events <- connEvent{idx: idx, ev: ev}:
			case <-p.done:
				return
			}
		}
	}
}

func (p *Pinger) sendProbe() {
	if p.opt.Outstanding && p.ntransmitted > 0 {
		if last := p.series.Last(); last != nil && !last.Received && !last.Err {
			p.printf("%sno answer yet for icmp_seq=%d\n", p.tsPrefix(time.Now()), last.Seq)
		}
	}
	p.ntransmitted++
	seq := p.ntransmitted
	payload := icmp.BuildPayload(p.opt.Size, p.opt.Pattern, time.Now())
	pkt := icmp.BuildEcho(p.v6, p.conn.ID(), uint16(seq), payload)
	sent, err := p.conn.Send(pkt)
	s := p.series.Add(seq, sent)
	p.lastSend = sent
	if err != nil {
		s.Err = true
		p.nerrors++
		if p.opt.Flood && !p.opt.Quiet && p.dash == nil {
			io.WriteString(p.out, "E")
		} else if !p.opt.Quiet && !(errors.Is(err, syscall.EMSGSIZE) && icmp.HasErrQueue(p.conn)) {
			p.eprintf("advp: sendmsg: %v\n", err)
		}
		return
	}
	if out := p.ntransmitted - p.nreceived - p.nerrors; out > p.pipe {
		p.pipe = out
	}
	if p.opt.Flood && !p.opt.Quiet && p.dash == nil {
		io.WriteString(p.out, ".")
	}
}

func (p *Pinger) sendHopProbes() {
	for _, h := range p.hops {
		h.seq++
		pkt := icmp.BuildEcho(p.v6, h.conn.ID(), uint16(h.seq), icmp.BuildPayload(p.opt.Size, p.opt.Pattern, time.Now()))
		sent, err := h.conn.Send(pkt)
		s := h.series.Add(h.seq, sent)
		if err != nil {
			s.Err = true
		}
	}
}

func (p *Pinger) handle(ce connEvent) {
	if ce.err != nil {
		if p.opt.Verbose {
			p.eprintf("advp: recvmsg: %v\n", ce.err)
		}
		return
	}
	if ce.idx == 0 {
		p.handleMain(ce.ev)
	} else {
		p.handleHop(p.hops[ce.idx-1], ce.ev)
	}
}

func (p *Pinger) handleMain(ev icmp.Event) {
	switch ev.Kind {
	case icmp.KindReply:
		s := p.series.Lookup(ev.Seq)
		if s == nil {
			if p.opt.Verbose {
				p.printf("%d bytes from %s: icmp_seq=%d (stray reply)\n", ev.Size, ev.From, ev.Seq)
			}
			return
		}
		rtt := ev.At.Sub(s.Sent)
		if rtt < 0 {
			rtt = 0
		}
		dup := s.Received
		if dup {
			s.Dups++
			p.nrepeats++
		} else {
			s.Received, s.RTT, s.TTL, s.From = true, rtt, ev.TTL, ev.From.String()
			if s.Err {
				s.Err = false
			}
			p.nreceived++
			if p.ewma == 0 {
				p.ewma = float64(rtt) * 8
			} else {
				p.ewma += float64(rtt) - p.ewma/8
			}
			if rtt > p.tmax {
				p.tmax = rtt
			}
		}
		p.printReply(ev, s, rtt, dup)
		if (p.opt.Flood || p.opt.Adaptive) && s.Seq == p.ntransmitted && p.canSend() {
			p.sendSoon()
		}
	case icmp.KindError:
		if ev.Local {
			// Local errors carry no packet, so no sequence number; the failed
			// send was already accounted for in sendProbe.
			if p.opt.Quiet {
				return
			}
			if p.opt.Flood && p.dash == nil {
				io.WriteString(p.out, "E")
			} else {
				p.eprintf("advp: %s\n", icmp.LocalErrorText(&ev))
			}
			return
		}
		s := p.series.Lookup(ev.Seq)
		if s == nil {
			return
		}
		p.nerrors++
		if !s.Received {
			s.Err = true
		}
		if p.opt.Flood && p.dash == nil {
			if !p.opt.Quiet {
				io.WriteString(p.out, "\bE")
			}
		} else if !p.opt.Quiet {
			p.printf("%sFrom %s icmp_seq=%d %s\n", p.tsPrefix(ev.At),
				p.fmtAddr(ev.From, !p.opt.Numeric, 500*time.Millisecond), s.Seq,
				icmp.ErrorText(p.v6, ev.Type, ev.Code, ev.Info))
		}
		if p.opt.Deadline > 0 {
			p.stopping = true
		}
	case icmp.KindOther:
		if p.opt.Verbose && !p.opt.Quiet {
			p.printf("%sFrom %s: icmp type=%d code=%d size=%d\n", p.tsPrefix(ev.At), ev.From, ev.Type, ev.Code, ev.Size)
		}
	}
	if p.opt.Count > 0 && p.nreceived+p.nerrors >= p.opt.Count {
		p.stopping = true
	}
}

func (p *Pinger) printReply(ev icmp.Event, s *stats.Sample, rtt time.Duration, dup bool) {
	if p.opt.Quiet {
		return
	}
	if p.opt.Flood && p.dash == nil {
		if !dup {
			io.WriteString(p.out, "\b \b")
		}
		return
	}
	var b strings.Builder
	b.WriteString(p.tsPrefix(ev.At))
	fmt.Fprintf(&b, "%d bytes from %s: icmp_seq=%d", ev.Size, p.fmtAddr(ev.From, p.resolveReplies(), 2*time.Second), s.Seq)
	if ev.TTL >= 0 {
		fmt.Fprintf(&b, " ttl=%d", ev.TTL)
	}
	fmt.Fprintf(&b, " time=%s ms", formatRTT(rtt, p.opt.Precise))
	if dup {
		b.WriteString(" (DUP!)")
	}
	if p.opt.Audible {
		b.WriteString("\a")
	}
	b.WriteString("\n")
	if p.opt.RecordRoute && len(ev.Route) > 0 {
		var r []string
		for _, ip := range ev.Route {
			r = append(r, p.fmtAddr(ip, !p.opt.Numeric, 500*time.Millisecond))
		}
		rs := strings.Join(r, "\n\t")
		if rs == p.lastRR {
			b.WriteString("\t(same route)\n")
		} else {
			fmt.Fprintf(&b, "RR: \t%s\n", rs)
			p.lastRR = rs
		}
	}
	p.printf("%s", b.String())
}

func (p *Pinger) handleHop(h *hopState, ev icmp.Event) {
	if ev.Kind == icmp.KindOther || ev.Local {
		return
	}
	s := h.series.Lookup(ev.Seq)
	if s == nil || s.Received {
		return
	}
	rtt := ev.At.Sub(s.Sent)
	if rtt < 0 {
		rtt = 0
	}
	s.Received, s.RTT, s.TTL, s.Err = true, rtt, ev.TTL, false
	s.From = ev.From.String()
	s.Reached = ev.Kind == icmp.KindReply
	if h.note(s.From, ev.At) && !p.opt.Quiet && !(p.opt.Flood && p.dash == nil) {
		what := ""
		if s.Reached {
			what = " (destination)"
		} else if !ev.IsTimeExceeded(p.v6) {
			what = " " + icmp.ErrorText(p.v6, ev.Type, ev.Code, ev.Info)
		}
		verb := "hop"
		if len(h.addrs) > 1 {
			verb = "hop changed:"
		}
		p.printf("%s[%s %d] %s%s time=%s ms\n", p.tsPrefix(ev.At), verb, h.ttl,
			p.fmtAddr(ev.From, !p.opt.Numeric, 300*time.Millisecond), what, formatRTT(rtt, p.opt.Precise))
	}
}

// sendSoon sends the next probe as soon as allowed (flood / adaptive mode).
func (p *Pinger) sendSoon() {
	minGap := 200 * time.Millisecond
	if p.root || p.opt.Flood {
		minGap = 2 * time.Millisecond
	}
	if p.opt.Flood && p.opt.IntervalSet {
		minGap = p.opt.Interval
	}
	if time.Since(p.lastSend) >= minGap {
		p.sendProbe()
		p.scheduleSend(time.Now().Add(p.sendTick()))
		return
	}
	// Too early: send as soon as the minimum gap has elapsed.
	if at := p.lastSend.Add(minGap); at.Before(p.nextSend) {
		p.scheduleSend(at)
	}
}

func (p *Pinger) scheduleSend(at time.Time) {
	p.nextSend = at
	if p.sendTimer != nil {
		p.sendTimer.Reset(time.Until(at))
	}
}

func (p *Pinger) sendTick() time.Duration {
	if p.opt.Interval > 0 {
		return p.opt.Interval
	}
	if p.opt.Flood {
		return 10 * time.Millisecond
	}
	return time.Millisecond
}

// exitWait mirrors iputils schedule_exit().
func (p *Pinger) exitWait() time.Duration {
	if p.nreceived > 0 {
		w := 2 * p.tmax
		if w < p.opt.Interval {
			w = p.opt.Interval
		}
		return w
	}
	return p.opt.Linger
}

// Run executes the session and returns the process exit code.
func (p *Pinger) Run() int {
	p.events = make(chan connEvent, 1024)
	p.done = make(chan struct{})
	p.series = stats.NewSeries()
	conns := []icmp.Conn{p.conn}
	for _, h := range p.hops {
		conns = append(conns, h.conn)
	}
	for i, c := range conns {
		p.wg.Add(1)
		go p.reader(i, c)
	}

	if p.v6 {
		fmt.Fprintf(p.out, "PING %s (%s) %d data bytes\n", p.host, p.dst, p.opt.Size)
	} else {
		optlen := 0
		if p.opt.RecordRoute {
			optlen = 40
		}
		fmt.Fprintf(p.out, "PING %s (%s) %d(%d) bytes of data.\n", p.host, p.dst, p.opt.Size, p.opt.Size+8+20+optlen)
	}
	if p.dash != nil {
		p.dash.Begin()
	}

	p.start = time.Now()
	if p.opt.Count > 0 && p.opt.Count < p.opt.Preload && p.opt.Deadline == 0 {
		p.opt.Preload = p.opt.Count
	}
	for i := 0; i < p.opt.Preload && p.canSend(); i++ {
		p.sendProbe()
	}
	// Sends are anchored to start + n*interval so timer latency does not accumulate.
	p.nextSend = p.start.Add(p.sendTick())
	p.sendTimer = time.NewTimer(time.Until(p.nextSend))
	defer p.sendTimer.Stop()

	var hopC <-chan time.Time
	if len(p.hops) > 0 {
		p.sendHopProbes()
		t := time.NewTicker(p.hopInterval())
		defer t.Stop()
		hopC = t.C
	}
	var deadlineC, exitC <-chan time.Time
	if p.opt.Deadline > 0 {
		deadlineC = time.After(p.opt.Deadline)
	}
	// Live view: frames follow sends and replies (paced by frameC); the ticker
	// keeps the clock and in-flight/lost accounting fresh when idle.
	var tickC, frameC <-chan time.Time
	if p.dash != nil {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		tickC = t.C
		p.keys = make(chan tty.Key, 16)
		if p.keysIn != nil {
			go p.readKeys()
		}
		p.renderDash(time.Now())
	}

	checkSendingDone := func() {
		if !p.sendingDone && !p.canSend() && !p.stopping {
			p.sendingDone = true
			exitC = time.After(p.exitWait())
		}
	}
	checkSendingDone()

	for !p.stopping {
		select {
		case ce := <-p.events:
			p.handle(ce)
			p.dirty = true
		case <-p.sendTimer.C:
			if p.canSend() {
				p.sendProbe()
				next := p.nextSend.Add(p.sendTick())
				if now := time.Now(); next.Before(now.Add(-p.sendTick())) {
					next = now.Add(p.sendTick())
				}
				p.scheduleSend(next)
				p.dirty = true
				p.frameNow()
			}
		case <-hopC:
			if !p.sendingDone {
				p.sendHopProbes()
			}
		case <-deadlineC:
			p.stopping = true
		case <-exitC:
			p.stopping = true
		case <-tickC:
			p.dirty = true
		case <-frameC:
			frameC = nil
			if !p.paused {
				p.renderDash(time.Now())
			}
		case k := <-p.keys:
			p.handleKey(k)
		case sig := <-p.sigs:
			switch sig {
			case syscall.SIGWINCH:
				if p.dash != nil {
					p.dash.Clear()
					p.renderDash(time.Now())
				}
			case syscall.SIGQUIT:
				if p.dash == nil {
					p.printStatus()
				}
			default:
				p.stopping = true
			}
		}
		checkSendingDone()
		if p.dash != nil && p.dirty && !p.paused && frameC == nil && !p.stopping {
			frameC = time.After(time.Until(p.lastFrame.Add(p.frameGap)))
		}
	}
	p.end = time.Now()

	close(p.done)
	p.wg.Wait()
	for _, c := range conns {
		c.Close()
	}
	if p.dash != nil {
		p.dash.End()
	}
	return p.finish()
}

func (p *Pinger) printStatus() {
	loss := 0
	if p.ntransmitted > 0 {
		loss = (p.ntransmitted - p.nreceived) * 100 / p.ntransmitted
	}
	fmt.Fprintf(p.errOut, "\r%d/%d packets, %d%% loss", p.nreceived, p.ntransmitted, loss)
	if p.nreceived > 0 {
		a := stats.Aggregate(p.series.Samples, time.Now(), p.lostAfter())
		fmt.Fprintf(p.errOut, ", min/avg/ewma/max = %s/%s/%s/%s ms",
			ms3(a.Min), ms3(a.Avg), ms3(time.Duration(p.ewma/8)), ms3(a.Max))
	}
	fmt.Fprintln(p.errOut)
}

// finish prints the iputils summary plus the extended report and returns the exit code.
func (p *Pinger) finish() int {
	if p.opt.Flood && !p.opt.Quiet {
		fmt.Fprintln(p.out)
	}
	a := stats.Aggregate(p.series.Samples, p.end, 0)
	fmt.Fprintf(p.out, "\n--- %s ping statistics ---\n", p.host)
	fmt.Fprintf(p.out, "%d packets transmitted, %d received", p.ntransmitted, p.nreceived)
	if p.nrepeats > 0 {
		fmt.Fprintf(p.out, ", +%d duplicates", p.nrepeats)
	}
	if p.nerrors > 0 {
		fmt.Fprintf(p.out, ", +%d errors", p.nerrors)
	}
	if p.ntransmitted > 0 {
		loss := float64(p.ntransmitted-p.nreceived) * 100 / float64(p.ntransmitted)
		fmt.Fprintf(p.out, ", %.6g%% packet loss", float64(float32(loss)))
	}
	fmt.Fprintf(p.out, ", time %dms\n", p.end.Sub(p.start).Milliseconds())
	if p.nreceived > 0 {
		fmt.Fprintf(p.out, "rtt min/avg/max/mdev = %s/%s/%s/%s ms", ms3(a.Min), ms3(a.Avg), ms3(a.Max), ms3(a.MDev))
		if p.pipe > 1 {
			fmt.Fprintf(p.out, ", pipe %d", p.pipe)
		}
		if (p.opt.Interval == 0 || p.opt.Flood || p.opt.Adaptive) && p.ntransmitted > 1 {
			ipg := p.end.Sub(p.start) / time.Duration(p.ntransmitted-1)
			fmt.Fprintf(p.out, ", ipg/ewma %s/%s ms", ms3(ipg), ms3(time.Duration(p.ewma/8)))
		}
		fmt.Fprintln(p.out)
	} else if p.pipe > 1 {
		fmt.Fprintf(p.out, ", pipe %d\n", p.pipe)
	}

	if !p.opt.NoReport && p.ntransmitted > 0 {
		in := p.reportInput(p.end, true)
		report.Write(p.out, in)
	}
	if p.opt.CSV != "" {
		if err := report.WriteCSV(p.opt.CSV, p.reportInput(p.end, true)); err != nil {
			fmt.Fprintf(p.errOut, "advp: csv: %v\n", err)
		} else if !p.opt.Quiet {
			fmt.Fprintf(p.out, "CSV written to %s\n", p.opt.CSV)
		}
	}
	if p.opt.HTML != "" {
		if err := report.WriteHTML(p.opt.HTML, p.reportInput(p.end, true)); err != nil {
			fmt.Fprintf(p.errOut, "advp: html: %v\n", err)
		} else if !p.opt.Quiet {
			fmt.Fprintf(p.out, "HTML report written to %s\n", p.opt.HTML)
		}
	}

	if p.nreceived > 0 && (p.opt.Deadline == 0 || p.opt.Count == 0 || p.nreceived >= p.opt.Count) {
		return 0
	}
	return 1
}

func (p *Pinger) reportInput(now time.Time, final bool) *report.Input {
	lost := p.lostAfter()
	if final {
		lost = 0
	}
	end := now
	in := &report.Input{
		Host: p.host, IP: p.dst.String(), Target: p.targetString(), V6: p.v6,
		Start: p.start, End: end, Now: now, LostAfter: lost, Final: final,
		Interval: p.opt.Interval, Size: p.opt.Size, SockKind: p.sockKind,
		Main: p.series.Samples, Bucket: p.opt.Bucket, ChartHeight: p.opt.ChartHeight,
		Width: report.TermWidth(), Color: p.color, Errors: p.nerrors,
	}
	in.Name = func(ip string) string {
		if p.opt.Numeric {
			return ""
		}
		return p.names.Lookup(ip, 300*time.Millisecond)
	}
	for _, h := range p.hops {
		in.Hops = append(in.Hops, report.HopView{TTL: h.ttl, Samples: h.series.Samples, Addrs: h.addrs})
	}
	return in
}
