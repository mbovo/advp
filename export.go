package main

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

func writeCSV(path string, in *reportInput) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"kind", "hop_ttl", "seq", "sent_time", "sent_unix_ms", "received", "rtt_ms", "ttl", "dups", "icmp_error", "from"})
	emit := func(kind string, hop int, samples []Sample) {
		for _, s := range samples {
			rtt := ""
			if s.Received {
				rtt = strconv.FormatFloat(float64(s.RTT)/1e6, 'f', 3, 64)
			}
			ttl := ""
			if s.TTL >= 0 {
				ttl = strconv.Itoa(s.TTL)
			}
			w.Write([]string{kind, strconv.Itoa(hop), strconv.Itoa(s.Seq), s.Sent.Format(time.RFC3339Nano),
				strconv.FormatInt(s.Sent.UnixMilli(), 10), strconv.FormatBool(s.Received), rtt, ttl,
				strconv.Itoa(s.Dups), strconv.FormatBool(s.Err), s.From})
		}
	}
	emit("target", 0, in.Main)
	for _, h := range in.Hops {
		emit("hop", h.TTL, h.Samples)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ---------------------------------------------------------------- SVG

type svgChart struct {
	w, h, l, r, t, b float64
	n                int
	yMax             float64
	sb               strings.Builder
}

func newSVG(w, h float64, n int, yMax float64) *svgChart {
	if yMax <= 0 {
		yMax = 1
	}
	c := &svgChart{w: w, h: h, l: 64, r: 16, t: 28, b: 34, n: max(n, 1), yMax: yMax}
	fmt.Fprintf(&c.sb, `<svg viewBox="0 0 %.0f %.0f" xmlns="http://www.w3.org/2000/svg" class="chart">`, w, h)
	return c
}

func (c *svgChart) pw() float64            { return c.w - c.l - c.r }
func (c *svgChart) ph() float64            { return c.h - c.t - c.b }
func (c *svgChart) x(i int) float64        { return c.l + (float64(i)+0.5)*c.pw()/float64(c.n) }
func (c *svgChart) y(v float64) float64    { return c.t + c.ph()*(1-math.Min(v, c.yMax)/c.yMax) }
func (c *svgChart) colW() float64          { return c.pw() / float64(c.n) }
func (c *svgChart) add(f string, a ...any) { fmt.Fprintf(&c.sb, f, a...) }

func (c *svgChart) axes(title, unit string, start time.Time, bucket time.Duration) {
	c.add(`<text x="%.0f" y="16" class="title">%s</text>`, c.l, template.HTMLEscapeString(title))
	for i := 0; i <= 4; i++ {
		v := c.yMax * float64(i) / 4
		y := c.y(v)
		c.add(`<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="grid"/>`, c.l, c.w-c.r, y, y)
		c.add(`<text x="%.1f" y="%.1f" class="ylab">%s%s</text>`, c.l-6, y+4, fmtMsF(v), unit)
	}
	layout := timeLayout(bucket, time.Duration(c.n)*bucket)
	ticks := min(6, c.n)
	for k := 0; k <= ticks; k++ {
		i := int(float64(k) / float64(ticks) * float64(c.n))
		x := c.l + float64(i)*c.colW()
		c.add(`<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="tick"/>`, x, x, c.t+c.ph(), c.t+c.ph()+4)
		c.add(`<text x="%.1f" y="%.1f" class="xlab">%s</text>`, x, c.h-12, start.Add(time.Duration(i)*bucket).Format(layout))
	}
}

// segments yields runs of consecutive indexes with data.
func segments(has []bool) [][2]int {
	var out [][2]int
	s := -1
	for i := 0; i <= len(has); i++ {
		if i < len(has) && has[i] {
			if s < 0 {
				s = i
			}
		} else if s >= 0 {
			out = append(out, [2]int{s, i})
			s = -1
		}
	}
	return out
}

func (c *svgChart) line(v []float64, has []bool, color string, dash bool) {
	d := ""
	if dash {
		d = ` stroke-dasharray="5 4"`
	}
	for _, sg := range segments(has) {
		var pts []string
		for i := sg[0]; i < sg[1]; i++ {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", c.x(i), c.y(v[i])))
		}
		if len(pts) == 1 {
			c.add(`<circle cx="%.1f" cy="%.1f" r="2" fill="%s"/>`, c.x(sg[0]), c.y(v[sg[0]]), color)
			continue
		}
		c.add(`<polyline points="%s" fill="none" stroke="%s" stroke-width="1.6"%s/>`, strings.Join(pts, " "), color, d)
	}
}

func (c *svgChart) band(lo, hi []float64, has []bool, color string) {
	for _, sg := range segments(has) {
		var pts []string
		for i := sg[0]; i < sg[1]; i++ {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", c.x(i), c.y(hi[i])))
		}
		for i := sg[1] - 1; i >= sg[0]; i-- {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", c.x(i), c.y(lo[i])))
		}
		c.add(`<polygon points="%s" fill="%s" fill-opacity="0.25" stroke="none"/>`, strings.Join(pts, " "), color)
	}
}

func (c *svgChart) dots(v []float64, has []bool, color string) {
	for i := range v {
		if has[i] {
			c.add(`<circle cx="%.1f" cy="%.1f" r="1.6" fill="%s"/>`, c.x(i), c.y(v[i]), color)
		}
	}
}

func (c *svgChart) bars(v []float64, color string) {
	w := math.Max(c.colW()*0.85, 1)
	for i := range v {
		if v[i] > 0 {
			y := c.y(v[i])
			c.add(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, c.x(i)-w/2, y, w, c.t+c.ph()-y, color)
		}
	}
}

func (c *svgChart) lostMarks(lost []bool) {
	w := math.Max(c.colW(), 1)
	for i, l := range lost {
		if l {
			c.add(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#e5484d" fill-opacity="0.18"/>`, c.x(i)-w/2, c.t, w, c.ph())
		}
	}
}

func (c *svgChart) tooltips(tips []string) {
	w := c.colW()
	for i, t := range tips {
		if t == "" {
			continue
		}
		c.add(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="hover"><title>%s</title></rect>`,
			c.l+float64(i)*w, c.t, w, c.ph(), template.HTMLEscapeString(t))
	}
}

func (c *svgChart) legend(items ...[2]string) {
	x := c.w - c.r
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		x -= float64(len(it[1]))*6.5 + 26
		c.add(`<rect x="%.1f" y="8" width="12" height="10" fill="%s"/><text x="%.1f" y="17" class="leg">%s</text>`,
			x, it[0], x+16, template.HTMLEscapeString(it[1]))
	}
}

func (c *svgChart) html() template.HTML {
	c.sb.WriteString("</svg>")
	return template.HTML(c.sb.String())
}

// ---------------------------------------------------------------- HTML

type htmlRow struct {
	Cells []string
	Class string
}

type htmlData struct {
	Title   string
	Summary []string
	Hops    []htmlRow
	HopHdr  []string
	TL      []htmlRow
	TLHdr   []string
	Charts  []template.HTML
	Bucket  string
	Version string
}

var htmlTmpl = template.Must(template.New("r").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
body{font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;margin:24px auto;max-width:1180px;color:#1d1d1f;background:#fafafa}
h1{font-size:20px} h2{font-size:16px;margin-top:28px}
.sum div{font-family:ui-monospace,Menlo,monospace;font-size:13px;margin:2px 0}
table{border-collapse:collapse;font-family:ui-monospace,Menlo,monospace;font-size:12px;background:#fff}
th,td{border:1px solid #ddd;padding:3px 7px;text-align:right} th{background:#f0f0f3} td:first-child,td.l{text-align:left}
tr.bad td{background:#fde8e8} tr.warn td{background:#fff6e0} tr.sub td{color:#777}
.chart{width:100%;height:auto;background:#fff;border:1px solid #e3e3e6;border-radius:6px;margin:10px 0}
.chart .grid{stroke:#eee}.chart .tick{stroke:#999}
.chart text{font-size:11px;fill:#666}.chart .ylab{text-anchor:end}.chart .xlab{text-anchor:middle}
.chart .title{font-size:13px;font-weight:600;fill:#222}.chart .hover{fill:transparent}.chart .hover:hover{fill:#0000000d}
footer{margin-top:30px;color:#999;font-size:11px}
</style></head><body>
<h1>{{.Title}}</h1>
<div class="sum">{{range .Summary}}<div>{{.}}</div>{{end}}</div>
<h2>Charts</h2>
{{range .Charts}}{{.}}{{end}}
{{if .Hops}}<h2>First hops vs destination</h2>
<table><tr>{{range .HopHdr}}<th>{{.}}</th>{{end}}</tr>
{{range .Hops}}<tr class="{{.Class}}">{{range $i, $c := .Cells}}<td{{if eq $i 1}} class="l"{{end}}>{{$c}}</td>{{end}}</tr>{{end}}</table>{{end}}
<h2>Timeline (bucket {{.Bucket}}, ms)</h2>
<table><tr>{{range .TLHdr}}<th>{{.}}</th>{{end}}</tr>
{{range .TL}}<tr class="{{.Class}}">{{range .Cells}}<td>{{.}}</td>{{end}}</tr>{{end}}</table>
<footer>generated by advp {{.Version}}</footer>
</body></html>`))

func lossClass(a Agg) string {
	switch l := a.LossPct(); {
	case l >= 5:
		return "bad"
	case l > 0:
		return "warn"
	}
	return ""
}

func writeHTML(path string, in *reportInput) error {
	plain := style{}
	d := htmlData{Title: "advp report · " + in.Target, Version: version}
	for _, l := range summaryLines(in, plain) {
		d.Summary = append(d.Summary, l)
	}

	// Charts use up to ~300 buckets.
	b := niceBucket(in.span(), 300, in.minBucket())
	n := int((in.span() + b - 1) / b)
	end := in.Start.Add(time.Duration(n) * b)
	main := bucketize(in.Main, in.Start, end, b, in.Now, in.LostAfter)
	ms := func(x time.Duration) float64 { return float64(x) / 1e6 }
	p50, p90, mx, avg, jit, loss := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	has, hasJ, lost := make([]bool, n), make([]bool, n), make([]bool, n)
	tips := make([]string, n)
	layout := timeLayout(b, in.span())
	var yMax, maxMax, jMax float64
	for i, a := range main {
		has[i], hasJ[i] = a.HasRTT, a.HasJitter
		lost[i] = !a.HasRTT && a.Lost > 0
		loss[i] = a.LossPct()
		if a.HasRTT {
			p50[i], p90[i], mx[i], avg[i], jit[i] = ms(a.P50), ms(a.P90), ms(a.Max), ms(a.Avg), ms(a.Jitter)
			yMax, maxMax, jMax = math.Max(yMax, p90[i]), math.Max(maxMax, mx[i]), math.Max(jMax, jit[i])
			tips[i] = fmt.Sprintf("%s  sent %d recv %d loss %.1f%%\nmin %s p50 %s avg %s p90 %s p99 %s max %s ms\njitter %s ms",
				a.Start.Format(layout), a.Sent, a.Recv, a.LossPct(), fmtMs(a.Min), fmtMs(a.P50), fmtMs(a.Avg), fmtMs(a.P90), fmtMs(a.P99), fmtMs(a.Max), fmtJitter(a))
		} else if a.Sent > 0 {
			tips[i] = fmt.Sprintf("%s  sent %d, all lost", a.Start.Format(layout), a.Sent)
		}
	}
	if maxMax <= yMax*1.5 {
		yMax = maxMax
	}
	c := newSVG(1140, 340, n, niceCeil(yMax*1.05))
	c.axes("Round-trip time", "", in.Start, b)
	c.lostMarks(lost)
	c.band(p50, p90, has, "#f5a524")
	c.dots(mx, has, "#8e4ec6")
	c.line(avg, has, "#0091ff", true)
	c.line(p50, has, "#30a46c", false)
	c.legend([2]string{"#30a46c", "p50"}, [2]string{"#f5a524", "p50–p90"}, [2]string{"#0091ff", "avg"}, [2]string{"#8e4ec6", "max"}, [2]string{"#e5484d", "100% loss"})
	c.tooltips(tips)
	d.Charts = append(d.Charts, c.html())

	lc := newSVG(1140, 150, n, 100)
	lc.axes("Packet loss", "%", in.Start, b)
	lc.bars(loss, "#e5484d")
	lc.tooltips(tips)
	d.Charts = append(d.Charts, lc.html())

	jc := newSVG(1140, 150, n, niceCeil(jMax*1.05))
	jc.axes("Jitter (mean |ΔRTT|, ms)", "", in.Start, b)
	jc.line(jit, hasJ, "#12a594", false)
	jc.tooltips(tips)
	d.Charts = append(d.Charts, jc.html())

	if len(in.Hops) > 0 {
		colors := []string{"#0091ff", "#8e4ec6", "#f76b15", "#e93d82", "#12a594"}
		type series struct {
			v    []float64
			has  []bool
			name string
		}
		var ss []series
		hm := 0.0
		for _, h := range in.Hops {
			hb := bucketize(h.Samples, in.Start, end, b, in.Now, in.LostAfter)
			s := series{v: make([]float64, n), has: make([]bool, n), name: fmt.Sprintf("hop %d", h.TTL)}
			for i, a := range hb {
				if a.HasRTT {
					s.v[i], s.has[i] = ms(a.Avg), true
					hm = math.Max(hm, s.v[i])
				}
			}
			ss = append(ss, s)
		}
		ss = append(ss, series{v: avg, has: has, name: "destination"})
		hm = math.Max(hm, maxOf(avg))
		hc := newSVG(1140, 240, n, niceCeil(hm*1.05))
		hc.axes("Average RTT per hop", "", in.Start, b)
		var leg [][2]string
		for i, s := range ss {
			col := colors[i%len(colors)]
			if s.name == "destination" {
				col = "#30a46c"
			}
			hc.line(s.v, s.has, col, s.name != "destination")
			leg = append(leg, [2]string{col, s.name})
		}
		hc.legend(leg...)
		d.Charts = append(d.Charts, hc.html())
	}

	// Hop table.
	d.HopHdr = []string{"Hop", "Address", "Sent", "Recv", "Loss %", "Min", "Avg", "P50", "P90", "Max", "Jitter"}
	hopRow := func(label, addr string, a Agg, class string) htmlRow {
		r := []string{label, addr, strconv.Itoa(a.Sent), strconv.Itoa(a.Recv), fmt.Sprintf("%.1f", a.LossPct())}
		if a.HasRTT {
			r = append(r, fmtMs(a.Min), fmtMs(a.Avg), fmtMs(a.P50), fmtMs(a.P90), fmtMs(a.Max), fmtMs(a.Jitter))
		} else {
			r = append(r, "-", "-", "-", "-", "-", "-")
		}
		return htmlRow{Cells: r, Class: class}
	}
	if len(in.Hops) > 0 {
		for _, h := range in.Hops {
			a := aggregate(h.Samples, in.Now, in.LostAfter)
			addr := "(no answer)"
			if len(h.Addrs) > 0 {
				addr = in.addrLabel(h.Addrs[0].IP)
			}
			d.Hops = append(d.Hops, hopRow(strconv.Itoa(h.TTL), addr, a, lossClass(a)))
			if len(h.Addrs) > 1 {
				for _, x := range h.Addrs {
					d.Hops = append(d.Hops, htmlRow{Class: "sub", Cells: []string{"", fmt.Sprintf("↳ %s  %d× %s–%s", in.addrLabel(x.IP), x.Count,
						x.First.Format("15:04:05"), x.Last.Format("15:04:05")), "", "", "", "", "", "", "", "", ""}})
				}
			}
		}
		a := aggregate(in.Main, in.Now, in.LostAfter)
		d.Hops = append(d.Hops, hopRow("dst", in.addrLabel(in.IP), a, lossClass(a)))
	}

	// Timeline table.
	tb := in.tableBucket()
	d.Bucket = tb.String()
	d.TLHdr = []string{"Time", "Sent", "Recv", "Loss %", "Min", "P50", "Avg", "P75", "P90", "P99", "Max", "Jitter", "mdev"}
	for _, h := range in.Hops {
		d.TLHdr = append(d.TLHdr, fmt.Sprintf("Hop%d avg", h.TTL), fmt.Sprintf("Hop%d loss", h.TTL))
	}
	rows := bucketize(in.Main, in.Start, in.Start.Add(in.span()), tb, in.Now, in.LostAfter)
	var hopRows [][]Agg
	for _, h := range in.Hops {
		hopRows = append(hopRows, bucketize(h.Samples, in.Start, in.Start.Add(in.span()), tb, in.Now, in.LostAfter))
	}
	tl := timeLayout(tb, in.span())
	for i, a := range rows {
		r := []string{a.Start.Format(tl), strconv.Itoa(a.Sent), strconv.Itoa(a.Recv), fmt.Sprintf("%.1f", a.LossPct())}
		if a.HasRTT {
			r = append(r, fmtMs(a.Min), fmtMs(a.P50), fmtMs(a.Avg), fmtMs(a.P75), fmtMs(a.P90), fmtMs(a.P99), fmtMs(a.Max), fmtJitter(a), fmtMs(a.MDev))
		} else {
			r = append(r, "-", "-", "-", "-", "-", "-", "-", "-", "-")
		}
		for _, hr := range hopRows {
			h := hr[i]
			if h.HasRTT {
				r = append(r, fmtMs(h.Avg), fmt.Sprintf("%.1f", h.LossPct()))
			} else {
				r = append(r, "-", fmt.Sprintf("%.1f", h.LossPct()))
			}
		}
		d.TL = append(d.TL, htmlRow{Cells: r, Class: lossClass(a)})
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := htmlTmpl.Execute(f, d); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
