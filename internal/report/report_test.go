package report

import (
	"bytes"
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
	d.Render(mkInput([]int{1, 2, 3}, true), DashStatus{Title: "h", State: "running", Elapsed: 3 * time.Second, Recent: []string{"64 bytes from h"}})
	d.End()
	out := b.String()
	if !strings.Contains(out, "\x1b[?1049h") || !strings.Contains(out, "\x1b[?1049l") || !strings.Contains(out, "advp ▸ PING h") {
		t.Fatalf("unexpected dashboard output: %q", out)
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
