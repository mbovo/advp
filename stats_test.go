package main

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
	a := aggregate(s, t0.Add(time.Hour), 0)
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
	a = aggregate(s, t0.Add(1500*time.Millisecond), time.Second)
	if a.Pending != 1 || a.Lost != 0 || a.LossPct() != 0 {
		t.Fatalf("pending: %+v", a)
	}
}

func TestBucketize(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := mkSamples(t0, []int{1, 2, 3, -1, 5, 6}, time.Second)
	b := bucketize(s, t0, t0.Add(6*time.Second), 2*time.Second, t0.Add(time.Hour), 0)
	if len(b) != 3 || b[0].Sent != 2 || b[1].Lost != 1 || b[2].Recv != 2 {
		t.Fatalf("buckets: %+v", b)
	}
	if !b[1].Start.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("start: %v", b[1].Start)
	}
}

func TestNiceBucket(t *testing.T) {
	if got := niceBucket(10*time.Minute, 20, time.Second); got != 30*time.Second {
		t.Errorf("got %v", got)
	}
	if got := niceBucket(5*time.Second, 20, time.Second); got != time.Second {
		t.Errorf("got %v", got)
	}
	if got := niceBucket(72*time.Hour, 20, time.Second); got != 6*time.Hour {
		t.Errorf("got %v", got)
	}
}
