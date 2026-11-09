package report

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/mbovo/advp/internal/stats"
)

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

// TermWidth returns the width of the terminal on stdout, $COLUMNS, or 120.
func TermWidth() int {
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

func fmtLoss(st style, a stats.Agg) string {
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

func fmtJitter(a stats.Agg) string {
	if !a.HasJitter {
		return "-"
	}
	return fmtMs(a.Jitter)
}
