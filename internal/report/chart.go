package report

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mbovo/advp/internal/stats"
)

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

func (in *Input) chartLayout() chartLayout {
	cols := in.Width - chartLabelW - 2
	if cols > 240 {
		cols = 240
	}
	if cols < 10 {
		cols = 10
	}
	b := stats.NiceBucket(in.span(), cols, in.minBucket())
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

func chartLines(in *Input, st style, height int) []string {
	if height < 3 {
		height = 3
	}
	cl := in.chartLayout()
	end := in.Start.Add(time.Duration(cl.n) * cl.bucket)
	main := stats.Bucketize(in.Main, in.Start, end, cl.bucket, in.Now, in.LostAfter)

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
			switch r {
			case 0:
				v = 0
			case height / 2:
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
		hb := stats.Bucketize(h.Samples, in.Start, end, cl.bucket, in.Now, in.LostAfter)
		v := make([]float64, len(hb))
		has := make([]bool, len(hb))
		lost := make([]bool, len(hb))
		for i, a := range hb {
			v[i], has[i] = ms(a.Avg), a.HasRTT
			lost[i] = !a.HasRTT && a.Lost > 0
		}
		agg := stats.Aggregate(h.Samples, in.Now, in.LostAfter)
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
