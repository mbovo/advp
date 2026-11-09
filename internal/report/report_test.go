package report

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/stats"
)

func mkSamples(start time.Time, rtts []int, step time.Duration) []stats.Sample {
	var s []stats.Sample
	for i, r := range rtts {
		x := stats.Sample{Seq: i + 1, Sent: start.Add(time.Duration(i) * step), TTL: 57}
		if r >= 0 {
			x.Received, x.RTT = true, time.Duration(r)*time.Millisecond
		}
		s = append(s, x)
	}
	return s
}

func mkInput(rtts []int, color bool) *Input {
	t0 := time.Unix(1700000000, 0)
	return &Input{
		Host: "h", IP: "192.0.2.1", Target: "h", Start: t0, End: t0.Add(time.Duration(len(rtts)+1) * time.Second),
		Now: t0.Add(time.Hour), Interval: time.Second, Size: 56, SockKind: "fake", Main: mkSamples(t0, rtts, time.Second),
		Hops: []HopView{{TTL: 1, Samples: mkSamples(t0, rtts, time.Second),
			Addrs: []HopAddr{{IP: "10.0.0.1", First: t0, Last: t0, Count: 1}, {IP: "10.0.0.9", First: t0, Last: t0, Count: 2}}}},
		ChartHeight: 5, Width: 60, Color: color,
	}
}

func TestWriteEdgeCases(t *testing.T) {
	for _, rtts := range [][]int{{}, {-1}, {5}, {-1, -1, -1}, {1, 2, 3, 400, 2, 1}} {
		var b bytes.Buffer
		Write(&b, mkInput(rtts, true))
		for _, want := range []string{"advp report", "Timeline", "RTT over time"} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("rtts %v: missing %q", rtts, want)
			}
		}
	}
}

func TestWritePlainHasNoANSI(t *testing.T) {
	var b bytes.Buffer
	Write(&b, mkInput([]int{1, 2, -1, 4}, false))
	if strings.Contains(b.String(), "\x1b[") {
		t.Fatal("escape sequences in uncolored output")
	}
	if !strings.Contains(b.String(), "↳ 10.0.0.9") {
		t.Fatal("route change not listed in hop table")
	}
}

func TestDashboardRender(t *testing.T) {
	var b bytes.Buffer
	d := NewDashboard(&b, true)
	d.Begin()
	d.RenderSize(mkInput([]int{1, 2, 3}, true), DashStatus{Title: "h", State: "running", Elapsed: 3 * time.Second, LastSeq: 3, Keys: true, Recent: []string{"64 bytes from h"}}, 120, 60)
	d.End()
	out := b.String()
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1049l", "advp ▸ PING h", "icmp_seq 3", "view: report", "RTT over time", "Timeline"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard output misses %q", want)
		}
	}
}

func TestDashboardFrameFitsTerminal(t *testing.T) {
	d := NewDashboard(io.Discard, true)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}, {40, 10}} {
		for v := ViewReport; v < numViews; v++ {
			for _, paused := range []bool{false, true} {
				in := mkInput([]int{1, 2, -1, 4, 5, 6, 7, 8, 9}, true)
				in.Width = size[0]
				lines := d.frame(in, DashStatus{Title: "h", View: v, Paused: paused, Keys: true, Recent: []string{"a", "b", "c", "d"}}, size[0], size[1])
				if len(lines) != size[1] {
					t.Fatalf("%dx%d view %v: %d lines", size[0], size[1], v, len(lines))
				}
				if !strings.Contains(lines[len(lines)-1], "quit") {
					t.Fatalf("%dx%d view %v: footer missing", size[0], size[1], v)
				}
				if paused && !strings.Contains(lines[len(lines)-1], "PAUSED") {
					t.Fatalf("paused state not shown")
				}
				if size[1] >= 40 && v == ViewChart && !strings.Contains(strings.Join(lines, "\n"), "RTT over time") {
					t.Fatalf("chart view without chart")
				}
			}
		}
	}
}

func TestExports(t *testing.T) {
	dir := t.TempDir()
	in := mkInput([]int{1, 2, -1, 4, 5}, false)
	if err := WriteCSV(filepath.Join(dir, "x.csv"), in); err != nil {
		t.Fatal(err)
	}
	if err := WriteHTML(filepath.Join(dir, "x.html"), in); err != nil {
		t.Fatal(err)
	}
	csv, _ := os.ReadFile(filepath.Join(dir, "x.csv"))
	if n := strings.Count(string(csv), "\n"); n != 11 { // header + 5 target + 5 hop rows
		t.Errorf("csv lines = %d", n)
	}
	html, _ := os.ReadFile(filepath.Join(dir, "x.html"))
	if !bytes.Contains(html, []byte("<svg")) || !bytes.Contains(html, []byte("Average RTT per hop")) {
		t.Error("html report incomplete")
	}
}

func TestTruncANSI(t *testing.T) {
	s := style{on: true}.red("hello") + " world"
	if got := visLen(truncANSI(s, 7)); got != 7 {
		t.Fatalf("visible len %d", got)
	}
}

func TestNiceCeil(t *testing.T) {
	for in, want := range map[float64]float64{0: 1, 0.7: 1, 3: 5, 12: 20, 230: 250, 999: 1000} {
		if got := niceCeil(in); got != want {
			t.Errorf("niceCeil(%v)=%v want %v", in, got, want)
		}
	}
}

func TestDashboardManyHops(t *testing.T) {
	in := mkInput([]int{1, 2, 3, 4, 5, 6}, false)
	in.Traced, in.PathHops = true, 16
	for ttl := 2; ttl <= 15; ttl++ {
		in.Hops = append(in.Hops, HopView{TTL: ttl, Samples: in.Main, Addrs: []HopAddr{{IP: "10.0.0.1", Count: 6}}})
	}
	d := NewDashboard(io.Discard, false)
	for _, size := range [][2]int{{80, 24}, {160, 50}} {
		for v := ViewReport; v < numViews; v++ {
			in.Width = size[0]
			lines := d.frame(in, DashStatus{Title: "h", View: v, Keys: true}, size[0], size[1])
			text := strings.Join(lines, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%v view %v: %d lines", size, v, len(lines))
			}
			if v == ViewHops && !strings.Contains(text, "Route to destination (15 hops traced)") {
				t.Fatalf("%v hops view without the hop table:\n%s", size, text)
			}
			if size[1] == 50 && v == ViewReport && !strings.Contains(text, "RTT over time") {
				t.Fatalf("report view lost the chart with many hops:\n%s", text)
			}
		}
	}
	var b bytes.Buffer
	Write(&b, in)
	if !strings.Contains(b.String(), "destination at hop 16 (traced)") || !strings.Contains(b.String(), "hop 15 │") {
		t.Fatal("final report misses traced path info or hop sparklines")
	}
}

func TestViewsCycle(t *testing.T) {
	v := ViewReport
	seen := map[string]bool{}
	for i := 0; i < int(numViews); i++ {
		seen[v.String()] = true
		v = v.Next()
	}
	if v != ViewReport || len(seen) != int(numViews) {
		t.Fatalf("views do not cycle: %v", seen)
	}
}

func TestDashboardRenderUsesTerminalSize(t *testing.T) {
	var b bytes.Buffer
	d := NewDashboard(&b, false)
	d.Clear()
	d.Render(mkInput([]int{1, 2}, false), DashStatus{Title: "h"})
	if !strings.HasPrefix(b.String(), "\x1b[H\x1b[2J") || strings.Count(b.String(), "\x1b[K") < 10 {
		t.Fatalf("unexpected frame: %q", b.String()[:min(80, b.Len())])
	}
}

func TestTermWidthFromEnv(t *testing.T) {
	t.Setenv("COLUMNS", "99")
	if w := TermWidth(); w != 99 && w <= 20 {
		t.Fatalf("width %d", w)
	}
	t.Setenv("COLUMNS", "junk")
	if TermWidth() <= 20 {
		t.Fatal("fallback width")
	}
}

func TestFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		500 * time.Microsecond: "0.500", 5 * time.Millisecond: "5.00", 50 * time.Millisecond: "50.0", 5 * time.Second: "5000",
	} {
		if got := fmtMs(d); got != want {
			t.Errorf("fmtMs(%v)=%s want %s", d, got, want)
		}
	}
	if fmtDur(1500*time.Millisecond) != "2s" || fmtDur(1234*time.Microsecond) != "1ms" {
		t.Error("fmtDur")
	}
	for _, c := range []struct {
		bucket, span time.Duration
		want         string
	}{
		{50 * time.Millisecond, time.Second, "15:04:05.00"},
		{500 * time.Millisecond, time.Minute, "15:04:05.0"},
		{time.Minute, time.Hour, "15:04:05"},
		{time.Hour, 48 * time.Hour, "01-02 15:04:05"},
	} {
		if got := timeLayout(c.bucket, c.span); got != c.want {
			t.Errorf("timeLayout(%v,%v)=%q want %q", c.bucket, c.span, got, c.want)
		}
	}
	st := style{on: false}
	for loss, want := range map[float64]string{0: "0.0%", 2: "2.0%", 50: "50.0%"} {
		a := stats.Agg{Sent: 100, Lost: int(loss)}
		if got := fmtLoss(st, a); got != want {
			t.Errorf("fmtLoss %v: %q", loss, got)
		}
	}
	if fmtLoss(st, stats.Agg{Sent: 2, Pending: 2}) != "…" {
		t.Error("pending-only loss")
	}
	if pad("ab", 4, true) != "  ab" || pad("abcdef", 3, false) != "abcdef" || truncVis("abcdef", 4) != "abc…" || truncVis("abc", 1) != "a" {
		t.Error("pad / truncVis")
	}
}

func TestInputNamesAndBuckets(t *testing.T) {
	in := mkInput([]int{1, 2, 3}, false)
	if in.addrLabel("10.0.0.1") != "10.0.0.1" {
		t.Fatal("no resolver")
	}
	in.Name = func(ip string) string { return "gw.lan" }
	if in.addrLabel("10.0.0.1") != "10.0.0.1 (gw.lan)" {
		t.Fatal(in.addrLabel("10.0.0.1"))
	}
	in.Bucket = 7 * time.Second
	if in.tableBucket() != 7*time.Second {
		t.Fatal("explicit --bucket ignored")
	}
	in.Interval = time.Millisecond
	if in.minBucket() != 10*time.Millisecond {
		t.Fatal("minimum bucket")
	}
}

func TestTableDropsColumns(t *testing.T) {
	tb := &table{headers: []string{"A", "Bbbbbbbbbb", "C"}, align: "lrr", drop: []int{1}}
	tb.rows = [][]string{{"1", "2", "3"}}
	wide := strings.Join(tb.render(style{}, 0), "\n")
	narrow := strings.Join(tb.render(style{}, 12), "\n")
	if !strings.Contains(wide, "Bbbbbbbbbb") || strings.Contains(narrow, "Bbbbbbbbbb") {
		t.Fatalf("column dropping:\n%s\n%s", wide, narrow)
	}
}

func TestExportErrors(t *testing.T) {
	in := mkInput([]int{1}, false)
	bad := filepath.Join(t.TempDir(), "missing", "x")
	if WriteCSV(bad, in) == nil || WriteHTML(bad, in) == nil {
		t.Fatal("writing into a missing directory must fail")
	}
}

func TestOutageSummary(t *testing.T) {
	var b bytes.Buffer
	Write(&b, mkInput([]int{1, -1, -1, -1, 2, -1, 3}, false))
	if !strings.Contains(b.String(), "2 loss episode(s) · longest 3 consecutive probe(s)") {
		t.Fatalf("outages:\n%s", b.String())
	}
}
