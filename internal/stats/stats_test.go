package stats

import (
	"testing"
	"time"
)

func mkSamples(start time.Time, rtts []int, step time.Duration) []Sample {
	var s []Sample
	for i, r := range rtts {
		x := Sample{Seq: i + 1, Sent: start.Add(time.Duration(i) * step), TTL: 57}
		if r >= 0 {
			x.Received, x.RTT = true, time.Duration(r)*time.Millisecond
		}
		s = append(s, x)
	}
	return s
}

func TestAggregate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := mkSamples(t0, []int{10, 20, -1, 30, 40, 50, 60, 70, 80, 90, 100}, time.Second)
	a := Aggregate(s, t0.Add(time.Hour), 0)
	if a.Sent != 11 || a.Recv != 10 || a.Lost != 1 || a.Pending != 0 {
		t.Fatalf("counts: %+v", a)
	}
	if a.Min != 10*time.Millisecond || a.Max != 100*time.Millisecond || a.Avg != 55*time.Millisecond {
		t.Fatalf("min/avg/max: %v %v %v", a.Min, a.Avg, a.Max)
	}
	if a.P50 != 55*time.Millisecond || a.P90 != 91*time.Millisecond {
		t.Fatalf("percentiles: p50=%v p90=%v", a.P50, a.P90)
	}
	// Consecutive diffs, the lost probe breaks the chain: |20-10| then 30..100 steps of 10.
	if a.Jitter != 10*time.Millisecond {
		t.Fatalf("jitter: %v", a.Jitter)
	}
	if l := a.LossPct(); l < 9.09 || l > 9.10 {
		t.Fatalf("loss: %v", l)
	}

	// The unanswered last probe is pending while it is still young.
	s = mkSamples(t0, []int{10, -1}, time.Second)
	a = Aggregate(s, t0.Add(1500*time.Millisecond), time.Second)
	if a.Pending != 1 || a.Lost != 0 || a.LossPct() != 0 {
		t.Fatalf("pending: %+v", a)
	}
}

func TestBucketize(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := mkSamples(t0, []int{1, 2, 3, -1, 5, 6}, time.Second)
	b := Bucketize(s, t0, t0.Add(6*time.Second), 2*time.Second, t0.Add(time.Hour), 0)
	if len(b) != 3 || b[0].Sent != 2 || b[1].Lost != 1 || b[2].Recv != 2 {
		t.Fatalf("buckets: %+v", b)
	}
	if !b[1].Start.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("start: %v", b[1].Start)
	}
}

func TestNiceBucket(t *testing.T) {
	if got := NiceBucket(10*time.Minute, 20, time.Second); got != 30*time.Second {
		t.Errorf("got %v", got)
	}
	if got := NiceBucket(5*time.Second, 20, time.Second); got != time.Second {
		t.Errorf("got %v", got)
	}
	if got := NiceBucket(72*time.Hour, 20, time.Second); got != 6*time.Hour {
		t.Errorf("got %v", got)
	}
}

func TestSeriesWrapAndLookup(t *testing.T) {
	s := NewSeries()
	if s.Last() != nil || s.Lookup(1) != nil {
		t.Fatal("empty series")
	}
	t0 := time.Unix(0, 0)
	s.Add(1, t0)
	s.Add(65537, t0.Add(time.Second)) // wraps to wire seq 1
	if got := s.Lookup(1); got == nil || got.Seq != 65537 {
		t.Fatalf("wire seq must map to the latest probe, got %+v", got)
	}
	if s.Last().Seq != 65537 || s.Last().TTL != -1 || len(s.Samples) != 2 {
		t.Fatal("Last / defaults")
	}
}

func TestGuessHops(t *testing.T) {
	cases := map[int][2]int{57: {64, 7}, 64: {64, 0}, 30: {32, 2}, 117: {128, 11}, 250: {255, 5}, 300: {0, 0}}
	for ttl, want := range cases {
		if i, h := GuessHops(ttl); i != want[0] || h != want[1] {
			t.Errorf("GuessHops(%d) = %d,%d want %v", ttl, i, h, want)
		}
	}
}

func TestPercentileAndLossEdges(t *testing.T) {
	if percentile(nil, 50) != 0 || percentile([]float64{7}, 99) != 7 || percentile([]float64{1, 2}, 100) != 2 {
		t.Fatal("percentile edges")
	}
	if (Agg{Sent: 3, Pending: 3}).LossPct() != 0 {
		t.Fatal("loss with only pending probes must be 0")
	}
	a := Aggregate([]Sample{{Sent: time.Unix(0, 0), Err: true}}, time.Unix(0, 0), time.Hour)
	if a.Lost != 1 || a.Errors != 1 || a.Pending != 0 {
		t.Fatalf("an ICMP error is a completed loss: %+v", a)
	}
	if Bucketize(nil, time.Unix(10, 0), time.Unix(5, 0), time.Second, time.Unix(0, 0), 0) != nil {
		t.Fatal("empty range must give no buckets")
	}
	if NiceBucket(10*24*time.Hour, 2, time.Second) != 192*time.Hour {
		t.Fatalf("beyond the step table: %v", NiceBucket(10*24*time.Hour, 2, time.Second))
	}
	if NiceBucket(time.Second, 0, 10*time.Millisecond) != time.Second {
		t.Fatal("n < 1 is treated as 1")
	}
}

func TestJitterAcrossBuckets(t *testing.T) {
	t0 := time.Unix(0, 0)
	b := Bucketize(mkSamples(t0, []int{10, 30, 20}, time.Second), t0, t0.Add(3*time.Second), time.Second, t0.Add(time.Hour), 0)
	if b[0].HasJitter || !b[1].HasJitter || b[1].Jitter != 20*time.Millisecond || b[2].Jitter != 10*time.Millisecond {
		t.Fatalf("per-bucket jitter must use the previous bucket's last RTT: %+v", b)
	}
}
