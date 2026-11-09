package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// dashboard is a full-screen live view redrawn periodically on the
// terminal's alternate screen.
type dashboard struct {
	w  io.Writer
	st style
}

func (d *dashboard) begin() { fmt.Fprint(d.w, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J") }
func (d *dashboard) end()   { fmt.Fprint(d.w, "\x1b[?25h\x1b[?1049l") }

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

func (d *dashboard) render(p *Pinger, now time.Time) {
	width, height := termSize()
	in := p.reportInput(now, false)
	in.Width = width
	in.End = now
	st := d.st

	state := "running"
	if p.sendingDone {
		state = "waiting for last replies"
	}
	head := []string{
		st.bold("advp ▸ PING "+p.targetString()) + st.dim(fmt.Sprintf("  %s %s · Ctrl-C stop & report · Ctrl-\\ status", state, fmtDur(now.Sub(p.start)))),
	}
	head = append(head, summaryLines(in, st)...)
	if hl := hopTable(in, st); len(hl) > 0 {
		head = append(head, hl...)
	}

	recentN := 4
	if len(p.recent) < recentN {
		recentN = len(p.recent)
	}
	tlRows := 5
	extras := 4 + len(in.Hops) // chart title, axis, time labels, loss, jitter, hops
	used := len(head) + 1 + extras + 1 + (tlRows + 4) + 1 + recentN
	chartH := height - used - 1
	if chartH < 4 {
		// Not enough room: drop the timeline table first.
		tlRows = 0
		chartH = height - (len(head) + 1 + extras + 1 + recentN) - 1
	}
	chartH = max(3, min(chartH, 24))

	lines := head
	lines = append(lines, "")
	lines = append(lines, chartLines(in, st, chartH)...)
	if tlRows > 0 {
		lines = append(lines, "")
		lines = append(lines, timelineTable(in, st, in.tableBucket(), tlRows)...)
	}
	if recentN > 0 {
		lines = append(lines, "")
		for _, r := range p.recent[len(p.recent)-recentN:] {
			lines = append(lines, st.dim(r))
		}
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for _, l := range lines {
		b.WriteString(truncANSI(l, width))
		b.WriteString("\x1b[K\n")
	}
	b.WriteString("\x1b[J")
	io.WriteString(d.w, b.String())
}
