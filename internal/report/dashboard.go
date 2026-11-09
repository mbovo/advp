package report

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Dashboard is a full-screen live view of the report, drawn on the
// terminal's alternate screen.
type Dashboard struct {
	w  io.Writer
	st style
}

// View selects the dashboard layout.
type View int

// Dashboard views, cycled with the "v" key.
const (
	ViewReport   View = iota // chart and timeline table, like the final report
	ViewChart                // large RTT chart
	ViewTimeline             // timeline table only
	ViewHops                 // per-hop table (mtr style), useful with --hops-max
	numViews
)

// Next returns the following view, wrapping around.
func (v View) Next() View { return (v + 1) % numViews }

func (v View) String() string {
	return [...]string{"report", "chart", "table", "hops"}[v]
}

// DashStatus is the session state shown in the dashboard header and footer.
type DashStatus struct {
	Title   string
	State   string
	Elapsed time.Duration
	LastSeq int
	Recent  []string // latest per-packet lines, oldest first
	View    View
	Paused  bool
	Keys    bool // keyboard input is available
	Updated time.Time
}

// NewDashboard returns a dashboard writing to w.
func NewDashboard(w io.Writer, color bool) *Dashboard {
	return &Dashboard{w: w, st: style{on: color}}
}

// Begin switches to the alternate screen and hides the cursor.
func (d *Dashboard) Begin() { fmt.Fprint(d.w, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J") }

// Clear erases the screen; the next frame repaints everything.
func (d *Dashboard) Clear() { fmt.Fprint(d.w, "\x1b[H\x1b[2J") }

// End restores the normal screen and the cursor.
func (d *Dashboard) End() { fmt.Fprint(d.w, "\x1b[?25h\x1b[?1049l") }

func termSize() (int, int) {
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 && h > 5 {
		return w, h
	}
	return 120, 40
}

// truncANSI cuts s to w visible runes while keeping escape sequences intact.
func truncANSI(s string, w int) string {
	if visLen(s) <= w {
		return s
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if loc := ansiRe.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			continue
		}
		if n >= w {
			i++
			continue
		}
		r := []rune(s[i:])[0]
		b.WriteRune(r)
		i += len(string(r))
		n++
	}
	return b.String() + "\x1b[0m"
}

// Render draws one frame sized to the current terminal.
func (d *Dashboard) Render(in *Input, s DashStatus) {
	width, height := termSize()
	d.RenderSize(in, s, width, height)
}

// RenderSize draws one frame for a width×height terminal.
func (d *Dashboard) RenderSize(in *Input, s DashStatus, width, height int) {
	in.Width = width
	lines := d.frame(in, s, width, height)
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		b.WriteString(truncANSI(l, width))
		b.WriteString("\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\x1b[J")
	io.WriteString(d.w, b.String())
}

// frame lays out exactly height lines: header, the report sections that fit,
// recent events and a footer on the last row.
func (d *Dashboard) frame(in *Input, s DashStatus, width, height int) []string {
	st := d.st
	summary := summaryLines(in, st)
	var hops []string
	if hl := hopTable(in, st); len(hl) > 0 {
		hops = append([]string{st.bold(in.hopTitle())}, hl...)
	}
	recent := s.Recent
	if len(recent) > 3 {
		recent = recent[len(recent)-3:]
	}
	section := func(l []string) int {
		if len(l) == 0 {
			return 0
		}
		return len(l) + 1 // trailing blank line
	}

	body := height - 2 // header and footer
	// Per-hop sparklines under the chart: all of them in the chart view (if
	// they fit), only short routes in the report view.
	sparks := 0
	switch {
	case s.View == ViewChart:
		sparks = min(len(in.Hops), max(0, body-section(summary)-5-1-8))
	case s.View == ViewReport && len(in.Hops) <= 4:
		sparks = len(in.Hops)
	}
	chartFixed := 5 + sparks
	minMain := chartFixed + 4
	if s.View == ViewHops {
		minMain = 0 // the hop table is the main content
	}
	if body-section(summary)-section(hops)-section(recent) < minMain {
		recent = nil
	}
	switch {
	case s.View == ViewHops:
		if body-section(summary)-section(hops) < 0 && len(summary) > 3 {
			summary = summary[:3] // period, probes, RTT
		}
	case body-section(summary)-section(hops) < minMain:
		hops = nil
	}
	avail := body - section(summary) - section(hops) - section(recent)

	const tlFixed = 5 // title, borders, header row, separator
	chartH, tlRows := 0, 0
	switch s.View {
	case ViewReport:
		// The chart has priority; the table gets what is left and any space
		// it cannot use (few buckets) goes back to the chart.
		rest := avail - chartFixed - 1
		chartH = max(6, rest*45/100)
		tlRows = min(in.timelineRows(), rest-chartH-tlFixed-1)
		if tlRows < 2 {
			tlRows, chartH = 0, rest
		} else {
			chartH = rest - tlRows - tlFixed - 1
		}
		chartH = min(chartH, 30)
	case ViewChart:
		chartH = avail - chartFixed - 1
	case ViewTimeline:
		tlRows = max(avail-tlFixed-1, 1)
	}

	lines := []string{d.header(in, s, width)}
	add := func(l []string) {
		if len(l) > 0 {
			lines = append(lines, l...)
			lines = append(lines, "")
		}
	}
	add(summary)
	add(hops)
	if chartH >= 3 {
		add(chartLines(in, st, chartH, sparks))
	}
	if tlRows > 0 {
		b := in.tableBucket()
		tl := []string{st.bold("Timeline") + st.dim(fmt.Sprintf("  (bucket %s, latest rows, ms)", b))}
		add(append(tl, timelineTable(in, st, b, tlRows)...))
	}
	for _, r := range recent {
		lines = append(lines, st.dim(r))
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	return append(lines, d.footer(s, width))
}

func (d *Dashboard) header(in *Input, s DashStatus, width int) string {
	st := d.st
	dot := st.green("●")
	if s.Paused {
		dot = st.yellow("❚❚")
	}
	left := st.bold("advp ▸ PING "+s.Title) + "  " + dot + " " +
		st.dim(fmt.Sprintf("%s · %s · icmp_seq %d", s.State, fmtDur(s.Elapsed), s.LastSeq))
	right := ""
	if !s.Updated.IsZero() {
		right = st.dim("updated " + s.Updated.Format("15:04:05"))
	}
	gap := width - visLen(left) - visLen(right)
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// footer is a reverse-video key bar. It uses attribute-specific "off" codes
// so inner highlights do not end the reverse video early.
func (d *Dashboard) footer(s DashStatus, width int) string {
	on := d.st.on
	bold := func(t string) string {
		if !on {
			return t
		}
		return "\x1b[1m" + t + "\x1b[22m"
	}
	var parts []string
	if s.Keys {
		parts = append(parts, bold("q")+" quit", bold("p")+" pause", bold("v")+" view: "+s.View.String(), bold("r")+" redraw")
	}
	parts = append(parts, bold("Ctrl-C")+" stop & report")
	f := " " + strings.Join(parts, "  │  ")
	if s.Paused {
		f = " " + bold("PAUSED") + " (stats still collected) ·" + f
	}
	if pad := width - visLen(f); pad > 0 {
		f += strings.Repeat(" ", pad)
	}
	if !on {
		return f
	}
	return "\x1b[7m" + f + "\x1b[0m"
}
