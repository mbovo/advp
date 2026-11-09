package ping

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/mbovo/advp/internal/icmp"
)

// Route discovery (--hops-max): every hop socket (TTL 1..N) is probed in
// parallel, traceProbes times, then the path is cut at the destination.
const (
	traceProbes       = 3
	defaultTraceGap   = 250 * time.Millisecond // between probe rounds
	defaultTraceWait  = time.Second            // after the last round
	traceNameLookupMs = 1500
)

type traceHop struct {
	ttl     int
	addrs   []string // responders in order of first answer
	rtts    map[string][]time.Duration
	at      map[string]time.Time
	lost    int
	note    string // traceroute style annotation (!H, !N, ...)
	reached bool   // answered by the destination itself
	errEnd  bool   // an ICMP error other than Time Exceeded ends the path
}

func (r *traceHop) answered() bool { return len(r.addrs) > 0 }

// trace discovers the route using the hop sockets. It returns early when
// every hop up to the end of the path has answered all its probes.
func (p *Pinger) trace() []*traceHop {
	n := len(p.hops)
	res := make([]*traceHop, n)
	pending := make([]map[uint16]time.Time, n)
	for i, h := range p.hops {
		res[i] = &traceHop{ttl: h.ttl, rtts: map[string][]time.Duration{}, at: map[string]time.Time{}}
		pending[i] = map[uint16]time.Time{}
	}
	rounds := 0
	sendRound := func() {
		for i, h := range p.hops {
			h.seq++
			pkt := icmp.BuildEcho(p.v6, h.conn.ID(), uint16(h.seq), icmp.BuildPayload(p.opt.Size, p.opt.Pattern, time.Now()))
			t, err := h.conn.Send(pkt)
			if err != nil {
				if p.traceErr == nil {
					p.traceErr = err
				}
				continue
			}
			pending[i][uint16(h.seq)] = t
		}
		rounds++
	}
	finish := func() []*traceHop {
		for i := range res {
			res[i].lost = len(pending[i])
		}
		return res
	}
	// complete reports whether all probes up to the end of the path are answered.
	complete := func() bool {
		if rounds < traceProbes {
			return false
		}
		for i, r := range res {
			if len(pending[i]) > 0 {
				return false
			}
			if r.reached || r.errEnd {
				return true
			}
		}
		return true
	}

	sendRound()
	tick := time.NewTicker(p.traceGap)
	defer tick.Stop()
	deadline := time.NewTimer(time.Duration(traceProbes-1)*p.traceGap + p.traceWait)
	defer deadline.Stop()
	for {
		select {
		case <-tick.C:
			if rounds < traceProbes {
				sendRound()
			}
		case ce := <-p.events:
			if ce.err != nil || ce.idx < 1 || ce.idx > n {
				continue
			}
			i, ev := ce.idx-1, ce.ev
			if ev.Kind == icmp.KindOther || ev.Local {
				continue
			}
			sent, ok := pending[i][ev.Seq]
			if !ok {
				continue
			}
			delete(pending[i], ev.Seq)
			r, from := res[i], ev.From.String()
			if _, seen := r.rtts[from]; !seen {
				r.addrs = append(r.addrs, from)
				r.at[from] = ev.At
			}
			r.rtts[from] = append(r.rtts[from], max(ev.At.Sub(sent), 0))
			switch {
			case ev.Kind == icmp.KindReply:
				r.reached = true
			case !ev.IsTimeExceeded(p.v6):
				r.errEnd = true
				r.note = traceNote(p.v6, ev)
			}
			if complete() {
				return finish()
			}
		case <-deadline.C:
			return finish()
		case sig := <-p.sigs:
			if sig != syscall.SIGWINCH && sig != syscall.SIGQUIT {
				p.stopping = true
				return finish()
			}
		}
	}
}

// traceNote returns the traceroute annotation for an ICMP error.
func traceNote(v6 bool, ev icmp.Event) string {
	if v6 {
		if ev.Type == icmp.V6Unreach {
			switch ev.Code {
			case 0:
				return "!N"
			case 1:
				return "!X"
			case 3:
				return "!H"
			}
			return fmt.Sprintf("!<%d>", ev.Code)
		}
		return fmt.Sprintf("!T%d", ev.Type)
	}
	if ev.Type == icmp.V4Unreach {
		switch ev.Code {
		case 0:
			return "!N"
		case 1:
			return "!H"
		case 2:
			return "!P"
		case 4:
			return "!F"
		case 9, 10, 13:
			return "!X"
		}
		return fmt.Sprintf("!<%d>", ev.Code)
	}
	return fmt.Sprintf("!T%d", ev.Type)
}

// applyTrace prints the discovered route and keeps monitoring only the hops
// before the destination (or up to the last hop that answered).
func (p *Pinger) applyTrace(res []*traceHop) {
	end, reached := -1, false
	for i, r := range res {
		if r.reached || r.errEnd {
			end, reached = i, r.reached
			break
		}
	}
	lastAnswer := -1
	for i, r := range res {
		if i > end && end >= 0 {
			break
		}
		if r.answered() {
			lastAnswer = i
		}
	}
	keep, shown := 0, lastAnswer+1
	switch {
	case end >= 0 && reached:
		keep, shown = end, end+1
		p.pathHops = end + 1
	case end >= 0:
		keep, shown = end+1, end+1
	default:
		keep = lastAnswer + 1
	}
	p.traced = true

	if !p.opt.Quiet {
		p.printTrace(res[:shown])
	}
	var msg string
	switch {
	case reached && keep == 0:
		msg = fmt.Sprintf("destination reached at hop %d; no intermediate hops to monitor", end+1)
	case reached:
		msg = fmt.Sprintf("destination reached at hop %d; monitoring hops 1-%d every %s", end+1, keep, p.hopInterval())
	case end >= 0:
		msg = fmt.Sprintf("hop %d answered %s; monitoring hops 1-%d every %s", end+1, res[end].note, keep, p.hopInterval())
	case keep == 0:
		msg = fmt.Sprintf("no hop answered within %d hops; hop monitoring disabled", len(res))
		if p.traceErr != nil {
			msg += fmt.Sprintf(" (send failed: %v)", p.traceErr)
		}
	default:
		msg = fmt.Sprintf("destination not reached within %d hops; monitoring hops 1-%d every %s", len(res), keep, p.hopInterval())
	}
	fmt.Fprintf(p.out, "→ %s\n\n", msg)

	// Seed the hop address lists so the session only announces new responders.
	for i := 0; i < keep; i++ {
		for _, a := range res[i].addrs {
			p.hops[i].seed(a, res[i].at[a], len(res[i].rtts[a]))
		}
	}
	// Sockets past the cut stay open (closed at the end of Run) but idle.
	p.hops = p.hops[:keep]
}

func (p *Pinger) printTrace(res []*traceHop) {
	fmt.Fprintf(p.out, "traceroute to %s, %d hops max, %d probes per hop\n", p.targetString(), len(p.hops), traceProbes)
	resolve := !p.opt.Numeric
	if resolve {
		for _, r := range res {
			for _, a := range r.addrs {
				p.names.Lookup(a, 0) // start lookups in parallel
			}
		}
	}
	for _, r := range res {
		var b strings.Builder
		fmt.Fprintf(&b, "%2d ", r.ttl)
		for _, a := range r.addrs {
			b.WriteString(" " + p.fmtAddr(net.ParseIP(a), resolve, traceNameLookupMs*time.Millisecond))
			for _, d := range r.rtts[a] {
				fmt.Fprintf(&b, "  %s ms", ms3(d))
			}
		}
		b.WriteString(strings.Repeat(" *", r.lost))
		if r.note != "" {
			b.WriteString(" " + r.note)
		}
		if r.reached {
			b.WriteString("  (destination)")
		}
		fmt.Fprintln(p.out, b.String())
	}
}
