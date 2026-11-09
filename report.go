package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

type hopView struct {
	TTL     int
	Samples []Sample
	Addrs   []hopAddr
}

type reportInput struct {
	Host, IP, Target string
	V6               bool
	Start, End, Now  time.Time
	LostAfter        time.Duration
	Final            bool
	Interval         time.Duration
	Size             int
	SockKind         string
	Main             []Sample
	Hops             []hopView
	Bucket           time.Duration
	ChartHeight      int
	Width            int
	Color            bool
	Errors           int
	Name             func(ip string) string
}

// ---------------------------------------------------------------- styling

type style struct{ on bool }

func (s style) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}
func (s style) bold(t string) string   { return s.wrap("1", t) }
func (s style) dim(t string) string    { return s.wrap("2", t) }
func (s style) red(t string) string    { return s.wrap("31", t) }
func (s style) green(t string) string  { return s.wrap("32", t) }
func (s style) yellow(t string) string { return s.wrap("33", t) }
func (s style) blue(t string) string   { return s.wrap("34", t) }
func (s style) mag(t string) string    { return s.wrap("35", t) }
func (s style) cyan(t string) string   { return s.wrap("36", t) }

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

func visLen(s string) int { return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, "")) }

func pad(s string, w int, right bool) string {
	n := w - visLen(s)
	if n <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", n) + s
	}
	return s + strings.Repeat(" ", n)
}

func truncVis(s string, w int) string {
	if visLen(s) <= w {
		return s
	}
	plain := []rune(ansiRe.ReplaceAllString(s, ""))
	if w <= 1 {
		return string(plain[:max(w, 0)])
	}
	return string(plain[:w-1]) + "…"
}

func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 20 {
		return c
	}
	return 120
}

// fmtMs renders a duration in milliseconds with precision adapted to its size.
func fmtMs(d time.Duration) string {
	return fmtMsF(float64(d) / float64(time.Millisecond))
}

func fmtMsF(v float64) string {
	switch {
	case v < 1:
		return strconv.FormatFloat(v, 'f', 3, 64)
	case v < 10:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case v < 1000:
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func fmtLoss(st style, a Agg) string {
	if a.Sent == a.Pending {
		return st.dim("…")
	}
	l := a.LossPct()
	s := strconv.FormatFloat(l, 'f', 1, 64) + "%"
	switch {
	case l == 0:
		return st.green(s)
	case l < 5:
		return st.yellow(s)
	}
	return st.red(s)
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func timeLayout(bucket time.Duration, span time.Duration) string {
	l := "15:04:05"
	switch {
	case bucket < 100*time.Millisecond:
		l = "15:04:05.00"
	case bucket < time.Second:
		l = "15:04:05.0"
	}
	if span >= 24*time.Hour {
		l = "01-02 " + l
	}
	return l
}

// ---------------------------------------------------------------- tables

type table struct {
	headers []string
	align   string // per column: 'l' or 'r'
	rows    [][]string
	drop    []int // column indexes to drop first when too wide (in order)
}

func (t *table) width(cols []int, w []int) int {
	n := 1
	for _, c := range cols {
		n += w[c] + 3
	}
	return n
}

func (t *table) render(st style, maxWidth int) []string {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = visLen(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(w) && visLen(c) > w[i] {
				w[i] = visLen(c)
			}
		}
	}
	keep := make([]int, 0, len(t.headers))
	for i := range t.headers {
		keep = append(keep, i)
	}
	for _, d := range t.drop {
		if maxWidth <= 0 || t.width(keep, w) <= maxWidth {
			break
		}
		for k, c := range keep {
			if c == d {
				keep = append(keep[:k], keep[k+1:]...)
				break
			}
		}
	}
	line := func(l, m, r string) string {
		var b strings.Builder
		b.WriteString(l)
		for k, c := range keep {
			b.WriteString(strings.Repeat("─", w[c]+2))
			if k < len(keep)-1 {
				b.WriteString(m)
			}
		}
		b.WriteString(r)
		return st.dim(b.String())
	}
	row := func(cells []string, hdr bool) string {
		var b strings.Builder
		b.WriteString(st.dim("│"))
		for _, c := range keep {
			v := ""
			if c < len(cells) {
				v = cells[c]
			}
			if hdr {
				v = st.bold(v)
			}
			b.WriteString(" " + pad(v, w[c], t.align[c] == 'r') + " ")
			b.WriteString(st.dim("│"))
		}
		return b.String()
	}
	out := []string{line("┌", "┬", "┐"), row(t.headers, true), line("├", "┼", "┤")}
	for _, r := range t.rows {
		out = append(out, row(r, false))
	}
	out = append(out, line("└", "┴", "┘"))
	return out
}

// ---------------------------------------------------------------- sections

func (in *reportInput) name(ip string) string {
	if in.Name == nil {
		return ""
	}
	return in.Name(ip)
}

func (in *reportInput) addrLabel(ip string) string {
	if n := in.name(ip); n != "" && n != ip {
		return fmt.Sprintf("%s (%s)", ip, n)
	}
	return ip
}

// span is the period covered by probes (the final linger wait is excluded).
func (in *reportInput) span() time.Duration {
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

func (in *reportInput) minBucket() time.Duration {
	if in.Interval >= 10*time.Millisecond {
		return in.Interval
	}
	return 10 * time.Millisecond
}

type outage struct {
	count      int
	start, end time.Time
}

func lossEpisodes(samples []Sample, now time.Time, lostAfter time.Duration) (episodes int, longest outage) {
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

func summaryLines(in *reportInput, st style) []string {
	a := aggregate(in.Main, in.Now, in.LostAfter)
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
		initial, hops := guessHops(a.TTL)
		out = append(out, key("Path")+fmt.Sprintf("reply TTL %d → ≈%d hops away (initial TTL %d)", a.TTL, hops, initial))
	}
	fam := "IPv4"
	if in.V6 {
		fam = "IPv6"
	}
	out = append(out, key("Probe")+fmt.Sprintf("%s, ICMP %s socket, %d data bytes, interval %s", fam, in.SockKind, in.Size, in.Interval))
	return out
}

func hopTable(in *reportInput, st style) []string {
	if len(in.Hops) == 0 {
		return nil
	}
	t := &table{
		headers: []string{"Hop", "Address", "Sent", "Recv", "Loss", "Min", "Avg", "P50", "P90", "Max", "Jitter"},
		align:   "rlrrrrrrrrr",
		drop:    []int{5, 9, 10, 2},
	}
	addRow := func(label, addr string, a Agg) {
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
		a := aggregate(h.Samples, in.Now, in.LostAfter)
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
	addRow("dst", truncVis(in.addrLabel(in.IP), maxAddr), aggregate(in.Main, in.Now, in.LostAfter))
	out := t.render(st, in.Width)
	if anyLoss {
		out = append(out, st.dim("  note: routers often rate-limit ICMP Time Exceeded; loss on a hop alone does not imply forwarding loss."))
	}
	return out
}

func (in *reportInput) tableBucket() time.Duration {
	if in.Bucket > 0 {
		return in.Bucket
	}
	return niceBucket(in.span(), 20, in.minBucket())
}

// timelineTable renders one row per time bucket; lastN>0 keeps only the most recent rows.
func timelineTable(in *reportInput, st style, bucket time.Duration, lastN int) []string {
	buckets := bucketize(in.Main, in.Start, in.Start.Add(in.span()), bucket, in.Now, in.LostAfter)
	if len(buckets) == 0 {
		return nil
	}
	var hopB [][]Agg
	for _, h := range in.Hops {
		hopB = append(hopB, bucketize(h.Samples, in.Start, in.Start.Add(in.span()), bucket, in.Now, in.LostAfter))
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

// ---------------------------------------------------------------- charts

var blocks = []rune("▁▂▃▄▅▆▇█")

// niceCeil rounds v up to 1, 2, 2.5 or 5 times a power of ten.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*e >= v {
			return m * e
		}
	}
	return 10 * e
}

const chartLabelW = 9 // label + axis glyph

type chartLayout struct {
	bucket time.Duration
	n      int // buckets
	colW   int
}

func (in *reportInput) chartLayout() chartLayout {
	cols := in.Width - chartLabelW - 2
	if cols > 240 {
		cols = 240
	}
	if cols < 10 {
		cols = 10
	}
	b := niceBucket(in.span(), cols, in.minBucket())
	n := int((in.span() + b - 1) / b)
	if n < 1 {
		n = 1
	}
	cw := cols / n
	if cw > 3 {
		cw = 3
	}
	if cw < 1 {
		cw = 1
	}
	return chartLayout{bucket: b, n: n, colW: cw}
}

func rep(s string, n int) string { return strings.Repeat(s, n) }

func chartLines(in *reportInput, st style, height int) []string {
	if height < 3 {
		height = 3
	}
	cl := in.chartLayout()
	end := in.Start.Add(time.Duration(cl.n) * cl.bucket)
	main := bucketize(in.Main, in.Start, end, cl.bucket, in.Now, in.LostAfter)

	var yMax, maxMax float64
	for _, a := range main {
		if a.HasRTT {
			yMax = math.Max(yMax, float64(a.P90))
			maxMax = math.Max(maxMax, float64(a.Max))
		}
	}
	if maxMax <= yMax*1.5 {
		yMax = maxMax
	}
	yMaxMs := niceCeil(yMax * 1.05 / float64(time.Millisecond))
	if yMax == 0 {
		yMaxMs = 1
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

	var out []string
	out = append(out, st.bold("RTT over time (ms)")+st.dim(fmt.Sprintf("  1 column = %s   ", cl.bucket))+
		st.green("█")+st.dim(" p50  ")+st.yellow("░")+st.dim(" p50→p90  ")+st.mag("•")+st.dim(" max  ")+
		st.mag("▲")+st.dim(" max off-scale  ")+st.red("x")+st.dim(" all lost"))
	for r := height - 1; r >= 0; r-- {
		lo := yMaxMs * float64(r) / float64(height)
		hi := yMaxMs * float64(r+1) / float64(height)
		label := strings.Repeat(" ", chartLabelW-1) + "│"
		if r == height-1 || r == 0 || r == height/2 {
			v := hi
			if r == 0 {
				v = 0
			} else if r == height/2 {
				v = lo
			}
			label = pad(fmtMsF(v), chartLabelW-2, true) + " ┤"
		}
		var b strings.Builder
		b.WriteString(st.dim(label))
		for _, a := range main {
			cell := " "
			switch {
			case !a.HasRTT:
				if r == 0 && a.Lost > 0 {
					cell = st.red("x")
				}
			default:
				p50, p90, mx := ms(a.P50), ms(a.P90), ms(a.Max)
				switch {
				case p50 >= hi:
					cell = st.green("█")
				case p50 > lo:
					idx := int((p50 - lo) / (hi - lo) * 8)
					if idx > 7 {
						idx = 7
					}
					cell = st.green(string(blocks[idx]))
				case p90 > lo:
					cell = st.yellow("░")
				case mx >= hi && r == height-1:
					cell = st.mag("▲")
				case mx >= lo && mx < hi:
					cell = st.mag("•")
				}
				if r == 0 && a.Lost > 0 && cell == st.green("█") {
					cell = st.red("█")
				}
			}
			b.WriteString(rep(cell, cl.colW))
		}
		out = append(out, b.String())
	}
	totalW := cl.n * cl.colW
	out = append(out, st.dim(strings.Repeat(" ", chartLabelW-1)+"└"+rep("─", totalW)))
	out = append(out, strings.Repeat(" ", chartLabelW)+timeAxis(in.Start, cl, totalW))

	// Sparkline rows sharing the same columns.
	lossRow := make([]string, len(main))
	for i, a := range main {
		switch {
		case a.Sent == 0 || a.Sent == a.Pending:
			lossRow[i] = " "
		case a.Lost == 0:
			lossRow[i] = st.dim("·")
		default:
			idx := int(math.Ceil(a.LossPct()/100*8)) - 1
			lossRow[i] = st.red(string(blocks[max(0, min(idx, 7))]))
		}
	}
	maxLoss := 0.0
	for _, a := range main {
		maxLoss = math.Max(maxLoss, a.LossPct())
	}
	out = append(out, sparkLine(st, "loss", lossRow, cl.colW, fmt.Sprintf("max %.1f%% (scale 0-100%%)", maxLoss)))

	jit := make([]float64, len(main))
	has := make([]bool, len(main))
	for i, a := range main {
		jit[i], has[i] = ms(a.Jitter), a.HasJitter
	}
	out = append(out, sparkLine(st, "jitter", scaleSpark(st, jit, has, nil, st.cyan), cl.colW, "max "+fmtMsF(maxOf(jit))+" ms"))

	for _, h := range in.Hops {
		hb := bucketize(h.Samples, in.Start, end, cl.bucket, in.Now, in.LostAfter)
		v := make([]float64, len(hb))
		has := make([]bool, len(hb))
		lost := make([]bool, len(hb))
		for i, a := range hb {
			v[i], has[i] = ms(a.Avg), a.HasRTT
			lost[i] = !a.HasRTT && a.Lost > 0
		}
		agg := aggregate(h.Samples, in.Now, in.LostAfter)
		note := "no answer"
		if agg.HasRTT {
			note = fmt.Sprintf("avg %s ms, max %s ms, loss %.1f%%", fmtMs(agg.Avg), fmtMsF(maxOf(v)), agg.LossPct())
		}
		out = append(out, sparkLine(st, fmt.Sprintf("hop %d", h.TTL), scaleSpark(st, v, has, lost, st.blue), cl.colW, note))
	}
	return out
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}

func scaleSpark(st style, v []float64, has, lost []bool, color func(string) string) []string {
	m := maxOf(v)
	out := make([]string, len(v))
	for i, x := range v {
		switch {
		case lost != nil && lost[i]:
			out[i] = st.red("x")
		case !has[i]:
			out[i] = " "
		case m == 0:
			out[i] = color(string(blocks[0]))
		default:
			idx := int(x / m * 7.999)
			out[i] = color(string(blocks[max(0, min(idx, 7))]))
		}
	}
	return out
}

func sparkLine(st style, label string, cells []string, colW int, note string) string {
	var b strings.Builder
	b.WriteString(st.cyan(pad(label, chartLabelW-2, true)) + st.dim(" │"))
	for _, c := range cells {
		b.WriteString(rep(c, colW))
	}
	b.WriteString("  " + st.dim(note))
	return b.String()
}

func timeAxis(start time.Time, cl chartLayout, width int) string {
	layout := timeLayout(cl.bucket, time.Duration(cl.n)*cl.bucket)
	end := start.Add(time.Duration(cl.n) * cl.bucket)
	l := start.Format(layout)
	r := end.Format(layout)
	if width < len(l)+len(r)+2 {
		return l
	}
	axis := []rune(strings.Repeat(" ", width))
	copy(axis, []rune(l))
	copy(axis[width-len(r):], []rune(r))
	if width >= 3*len(l)+6 {
		m := start.Add(time.Duration(cl.n/2) * cl.bucket).Format(layout)
		pos := (cl.n / 2) * cl.colW
		if pos+len(m) < width-len(r)-1 && pos > len(l)+1 {
			copy(axis[pos:], []rune(m))
		}
	}
	return string(axis)
}

// ---------------------------------------------------------------- report

func writeReport(w io.Writer, in *reportInput) {
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
