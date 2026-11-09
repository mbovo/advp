// Package report renders ping session statistics as terminal tables and
// charts, a live dashboard, CSV and HTML.
package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mbovo/advp/internal/stats"
)

// HopAddr is one address seen answering for a hop.
type HopAddr struct {
	IP    string
	First time.Time
	Last  time.Time
	Count int
}

// HopView is the data of one probed hop (TTL).
type HopView struct {
	TTL     int
	Samples []stats.Sample
	Addrs   []HopAddr
}

// Input is a snapshot of a ping session to render.
type Input struct {
	Host, IP, Target string
	V6               bool
	Start, End, Now  time.Time
	LostAfter        time.Duration
	Final            bool
	Interval         time.Duration
	Size             int
	SockKind         string
	Main             []stats.Sample
	Hops             []HopView
	Bucket           time.Duration
	ChartHeight      int
	Width            int
	Color            bool
	Errors           int
	Name             func(ip string) string
}

func (in *Input) name(ip string) string {
	if in.Name == nil {
		return ""
	}
	return in.Name(ip)
}

func (in *Input) addrLabel(ip string) string {
	if n := in.name(ip); n != "" && n != ip {
		return fmt.Sprintf("%s (%s)", ip, n)
	}
	return ip
}

// span is the period covered by probes (the final linger wait is excluded).
func (in *Input) span() time.Duration {
	end := in.End
	var last time.Time
	if n := len(in.Main); n > 0 {
		last = in.Main[n-1].Sent
	}
	for _, h := range in.Hops {
		if n := len(h.Samples); n > 0 && h.Samples[n-1].Sent.After(last) {
			last = h.Samples[n-1].Sent
		}
	}
	if !last.IsZero() {
		step := in.Interval
		if step < 10*time.Millisecond {
			step = 10 * time.Millisecond
		}
		if e := last.Add(step); e.Before(end) {
			end = e
		}
	}
	s := end.Sub(in.Start)
	if s < in.Interval {
		s = in.Interval
	}
	if s <= 0 {
		s = time.Second
	}
	return s
}

func (in *Input) minBucket() time.Duration {
	if in.Interval >= 10*time.Millisecond {
		return in.Interval
	}
	return 10 * time.Millisecond
}

type outage struct {
	count      int
	start, end time.Time
}

func lossEpisodes(samples []stats.Sample, now time.Time, lostAfter time.Duration) (episodes int, longest outage) {
	var cur outage
	flush := func() {
		if cur.count > 0 {
			episodes++
			if cur.count > longest.count {
				longest = cur
			}
		}
		cur = outage{}
	}
	for _, s := range samples {
		if s.Received {
			flush()
			continue
		}
		if !s.Err && lostAfter > 0 && now.Sub(s.Sent) < lostAfter {
			continue
		}
		if cur.count == 0 {
			cur.start = s.Sent
		}
		cur.count++
		cur.end = s.Sent
	}
	flush()
	return
}

func summaryLines(in *Input, st style) []string {
	a := stats.Aggregate(in.Main, in.Now, in.LostAfter)
	key := func(k string) string { return st.cyan(pad(k, 11, false)) }
	sep := st.dim(" · ")
	var out []string

	period := fmt.Sprintf("%s → %s (%s)", in.Start.Format("2006-01-02 15:04:05"), in.End.Format("15:04:05"), fmtDur(in.End.Sub(in.Start)))
	out = append(out, key("Period")+period)

	probes := fmt.Sprintf("%d sent%s%d received%s%d lost (%s)", a.Sent, sep, a.Recv, sep, a.Lost, fmtLoss(st, a))
	if a.Pending > 0 {
		probes += sep + fmt.Sprintf("%d in flight", a.Pending)
	}
	probes += sep + fmt.Sprintf("%d duplicates", a.Dups) + sep + fmt.Sprintf("%d errors", in.Errors)
	out = append(out, key("Probes")+probes)

	if a.HasRTT {
		parts := []string{
			"min " + fmtMs(a.Min), "p50 " + st.bold(fmtMs(a.P50)), "avg " + fmtMs(a.Avg),
			"p75 " + fmtMs(a.P75), "p90 " + st.bold(fmtMs(a.P90)), "p95 " + fmtMs(a.P95),
			"p99 " + fmtMs(a.P99), "max " + fmtMs(a.Max),
		}
		out = append(out, key("RTT (ms)")+strings.Join(parts, sep))
		out = append(out, key("Variation")+fmt.Sprintf("jitter %s ms%smdev %s ms%sspread p90-p50 %s ms",
			fmtMs(a.Jitter), sep, fmtMs(a.MDev), sep, fmtMs(a.P90-a.P50)))
	}
	if ep, lo := lossEpisodes(in.Main, in.Now, in.LostAfter); ep > 0 {
		out = append(out, key("Outages")+fmt.Sprintf("%d loss episode(s)%slongest %d consecutive probe(s) at %s (%s)",
			ep, sep, lo.count, lo.start.Format("15:04:05"), fmtDur(lo.end.Sub(lo.start)+in.Interval)))
	}
	if a.TTL >= 0 {
		initial, hops := stats.GuessHops(a.TTL)
		out = append(out, key("Path")+fmt.Sprintf("reply TTL %d → ≈%d hops away (initial TTL %d)", a.TTL, hops, initial))
	}
	fam := "IPv4"
	if in.V6 {
		fam = "IPv6"
	}
	out = append(out, key("Probe")+fmt.Sprintf("%s, ICMP %s socket, %d data bytes, interval %s", fam, in.SockKind, in.Size, in.Interval))
	return out
}

func hopTable(in *Input, st style) []string {
	if len(in.Hops) == 0 {
		return nil
	}
	t := &table{
		headers: []string{"Hop", "Address", "Sent", "Recv", "Loss", "Min", "Avg", "P50", "P90", "Max", "Jitter"},
		align:   "rlrrrrrrrrr",
		drop:    []int{5, 9, 10, 2},
	}
	addRow := func(label, addr string, a stats.Agg) {
		r := []string{label, addr, strconv.Itoa(a.Sent), strconv.Itoa(a.Recv), fmtLoss(st, a)}
		if a.HasRTT {
			r = append(r, fmtMs(a.Min), fmtMs(a.Avg), fmtMs(a.P50), fmtMs(a.P90), fmtMs(a.Max), fmtMs(a.Jitter))
		} else {
			r = append(r, "-", "-", "-", "-", "-", "-")
		}
		t.rows = append(t.rows, r)
	}
	maxAddr := in.Width/2 - 10
	anyLoss := false
	for _, h := range in.Hops {
		a := stats.Aggregate(h.Samples, in.Now, in.LostAfter)
		if a.Lost > 0 {
			anyLoss = true
		}
		addr := st.dim("(no answer)")
		if len(h.Addrs) > 0 {
			best := h.Addrs[0]
			for _, x := range h.Addrs {
				if x.Count > best.Count {
					best = x
				}
			}
			addr = in.addrLabel(best.IP)
			for _, s := range h.Samples {
				if s.Reached && s.From == best.IP {
					addr += st.dim(" [dest]")
					break
				}
			}
		}
		addRow(strconv.Itoa(h.TTL), truncVis(addr, maxAddr), a)
		if len(h.Addrs) > 1 {
			for _, x := range h.Addrs {
				t.rows = append(t.rows, []string{"", st.dim(truncVis(fmt.Sprintf("↳ %s  %d× %s–%s", in.addrLabel(x.IP), x.Count,
					x.First.Format("15:04:05"), x.Last.Format("15:04:05")), maxAddr)), "", "", "", "", "", "", "", "", ""})
			}
		}
	}
	addRow("dst", truncVis(in.addrLabel(in.IP), maxAddr), stats.Aggregate(in.Main, in.Now, in.LostAfter))
	out := t.render(st, in.Width)
	if anyLoss {
		out = append(out, st.dim("  note: routers often rate-limit ICMP Time Exceeded; loss on a hop alone does not imply forwarding loss."))
	}
	return out
}

func (in *Input) tableBucket() time.Duration {
	if in.Bucket > 0 {
		return in.Bucket
	}
	return stats.NiceBucket(in.span(), 20, in.minBucket())
}

// timelineTable renders one row per time bucket; lastN>0 keeps only the most recent rows.
func timelineTable(in *Input, st style, bucket time.Duration, lastN int) []string {
	buckets := stats.Bucketize(in.Main, in.Start, in.Start.Add(in.span()), bucket, in.Now, in.LostAfter)
	if len(buckets) == 0 {
		return nil
	}
	var hopB [][]stats.Agg
	for _, h := range in.Hops {
		hopB = append(hopB, stats.Bucketize(h.Samples, in.Start, in.Start.Add(in.span()), bucket, in.Now, in.LostAfter))
	}
	headers := []string{"Time", "Sent", "Recv", "Loss", "Min", "P50", "Avg", "P75", "P90", "P99", "Max", "Jitter", "mdev"}
	align := "lrrrrrrrrrrrr"
	for _, h := range in.Hops {
		headers = append(headers, fmt.Sprintf("Hop%d avg/loss", h.TTL))
		align += "r"
	}
	t := &table{headers: headers, align: align, drop: []int{12, 9, 4, 7, 1, 10}}
	if len(in.Hops) > 0 {
		// Hop columns are dropped before the core latency columns.
		var d []int
		for i := len(in.Hops) - 1; i >= 0; i-- {
			d = append(d, 13+i)
		}
		t.drop = append([]int{12, 9, 4}, append(d, 7, 1, 10)...)
	}
	layout := timeLayout(bucket, in.span())
	first := 0
	if lastN > 0 && len(buckets) > lastN {
		first = len(buckets) - lastN
	}
	for bi := first; bi < len(buckets); bi++ {
		a := buckets[bi]
		r := []string{a.Start.Format(layout), strconv.Itoa(a.Sent), strconv.Itoa(a.Recv), fmtLoss(st, a)}
		if a.Sent == 0 {
			r[3] = st.dim("-")
		}
		if a.HasRTT {
			r = append(r, fmtMs(a.Min), fmtMs(a.P50), fmtMs(a.Avg), fmtMs(a.P75), fmtMs(a.P90), fmtMs(a.P99), fmtMs(a.Max), fmtJitter(a), fmtMs(a.MDev))
		} else {
			for i := 0; i < 9; i++ {
				r = append(r, st.dim("-"))
			}
		}
		for _, hb := range hopB {
			h := hb[bi]
			switch {
			case h.Sent == 0 || h.Sent == h.Pending:
				r = append(r, st.dim("-"))
			case !h.HasRTT:
				r = append(r, st.red("lost"))
			default:
				r = append(r, fmtMs(h.Avg)+"/"+fmtLoss(st, h))
			}
		}
		t.rows = append(t.rows, r)
	}
	return t.render(st, in.Width)
}

// Write renders the full end-of-run report: summary, hop table, timeline
// table and charts.
func Write(w io.Writer, in *Input) {
	st := style{on: in.Color}
	title := fmt.Sprintf(" advp report · %s ", in.Target)
	fill := in.Width - visLen(title) - 4
	if fill < 3 {
		fill = 3
	}
	lines := []string{"", st.bold(st.dim("══") + title + st.dim(rep("═", fill)))}
	lines = append(lines, summaryLines(in, st)...)
	if hl := hopTable(in, st); len(hl) > 0 {
		lines = append(lines, "", st.bold(fmt.Sprintf("First %d hop(s) vs destination", len(in.Hops))))
		lines = append(lines, hl...)
	}
	b := in.tableBucket()
	lines = append(lines, "", st.bold("Timeline")+st.dim(fmt.Sprintf("  (bucket %s, times in ms, loss over completed probes)", b)))
	lines = append(lines, timelineTable(in, st, b, 0)...)
	lines = append(lines, "")
	lines = append(lines, chartLines(in, st, in.ChartHeight)...)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}
