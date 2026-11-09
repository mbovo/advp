package main

import (
	"math"
	"sort"
	"time"
)

// Sample is one probe sent to the target or to a hop.
type Sample struct {
	Seq      int
	Sent     time.Time
	RTT      time.Duration
	Received bool
	Dups     int
	TTL      int
	Err      bool   // answered by an ICMP error instead of a reply
	From     string // responder address
	Reached  bool   // hop probe answered by the destination itself
}

// Series stores samples in send order and maps wire sequence numbers
// (16 bit, wrapping) to the latest sample using them.
type Series struct {
	Samples []Sample
	index   map[uint16]int
}

func newSeries() *Series { return &Series{index: map[uint16]int{}} }

func (s *Series) Add(seq int, sent time.Time) *Sample {
	s.Samples = append(s.Samples, Sample{Seq: seq, Sent: sent, TTL: -1})
	s.index[uint16(seq)] = len(s.Samples) - 1
	return &s.Samples[len(s.Samples)-1]
}

func (s *Series) Lookup(seq uint16) *Sample {
	i, ok := s.index[seq]
	if !ok {
		return nil
	}
	return &s.Samples[i]
}

func (s *Series) Last() *Sample {
	if len(s.Samples) == 0 {
		return nil
	}
	return &s.Samples[len(s.Samples)-1]
}

// Agg is the aggregate of a set of samples (whole run or one time bucket).
type Agg struct {
	Start     time.Time
	Sent      int
	Recv      int
	Lost      int
	Pending   int
	Errors    int
	Dups      int
	HasRTT    bool
	Min       time.Duration
	Max       time.Duration
	Avg       time.Duration
	P50       time.Duration
	P75       time.Duration
	P90       time.Duration
	P95       time.Duration
	P99       time.Duration
	MDev      time.Duration // standard deviation, as iputils "mdev"
	Jitter    time.Duration // mean absolute difference of consecutive RTTs (RFC 3550 style)
	HasJitter bool
	LastRTT   time.Duration
	TTL       int
}

// LossPct is the loss over probes whose fate is known.
func (a Agg) LossPct() float64 {
	done := a.Sent - a.Pending
	if done <= 0 {
		return 0
	}
	return float64(a.Lost) * 100 / float64(done)
}

// aggregate computes statistics; probes younger than lostAfter without an
// answer are counted as pending rather than lost. Use lostAfter=0 for a
// final report.
func aggregate(samples []Sample, now time.Time, lostAfter time.Duration) Agg {
	return aggregateFrom(samples, -1, now, lostAfter)
}

// aggregateFrom is aggregate with the RTT (ns) of the sample immediately
// preceding the set, so jitter is defined even for a single-sample bucket.
func aggregateFrom(samples []Sample, prevRTT float64, now time.Time, lostAfter time.Duration) Agg {
	a := Agg{TTL: -1}
	rtts := make([]float64, 0, len(samples))
	var sum, sum2, jsum float64
	var jn int
	prev := prevRTT
	for i := range samples {
		s := &samples[i]
		a.Sent++
		a.Dups += s.Dups
		if s.Err {
			a.Errors++
		}
		if !s.Received {
			prev = -1
			if !s.Err && lostAfter > 0 && now.Sub(s.Sent) < lostAfter {
				a.Pending++
			} else {
				a.Lost++
			}
			continue
		}
		a.Recv++
		v := float64(s.RTT)
		rtts = append(rtts, v)
		sum += v
		sum2 += v * v
		if prev >= 0 {
			jsum += math.Abs(v - prev)
			jn++
		}
		prev = v
		a.LastRTT = s.RTT
		if s.TTL >= 0 {
			a.TTL = s.TTL
		}
	}
	if len(rtts) == 0 {
		return a
	}
	a.HasRTT = true
	sort.Float64s(rtts)
	n := float64(len(rtts))
	mean := sum / n
	a.Min = time.Duration(rtts[0])
	a.Max = time.Duration(rtts[len(rtts)-1])
	a.Avg = time.Duration(mean)
	a.MDev = time.Duration(math.Sqrt(math.Max(0, sum2/n-mean*mean)))
	a.P50 = time.Duration(percentile(rtts, 50))
	a.P75 = time.Duration(percentile(rtts, 75))
	a.P90 = time.Duration(percentile(rtts, 90))
	a.P95 = time.Duration(percentile(rtts, 95))
	a.P99 = time.Duration(percentile(rtts, 99))
	if jn > 0 {
		a.Jitter = time.Duration(jsum / float64(jn))
		a.HasJitter = true
	}
	return a
}

// percentile uses linear interpolation between closest ranks on sorted data.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	r := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(r))
	hi := int(math.Ceil(r))
	if hi >= len(sorted) {
		hi = len(sorted) - 1
	}
	f := r - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*f
}

// bucketize splits samples (ordered by send time) into fixed width buckets
// starting at start, covering [start, end).
func bucketize(samples []Sample, start, end time.Time, width time.Duration, now time.Time, lostAfter time.Duration) []Agg {
	if width <= 0 || !end.After(start) {
		return nil
	}
	n := int((end.Sub(start) + width - 1) / width)
	if n <= 0 {
		n = 1
	}
	out := make([]Agg, n)
	i := 0
	for b := 0; b < n; b++ {
		bs := start.Add(time.Duration(b) * width)
		be := bs.Add(width)
		j := i
		for j < len(samples) && samples[j].Sent.Before(be) {
			j++
		}
		prev := -1.0
		if i > 0 && samples[i-1].Received {
			prev = float64(samples[i-1].RTT)
		}
		out[b] = aggregateFrom(samples[i:j], prev, now, lostAfter)
		out[b].Start = bs
		i = j
	}
	return out
}

var niceSteps = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// niceBucket picks a round bucket width so that span fits in at most n buckets,
// never smaller than minWidth.
func niceBucket(span time.Duration, n int, minWidth time.Duration) time.Duration {
	if n < 1 {
		n = 1
	}
	want := span / time.Duration(n)
	if span%time.Duration(n) != 0 {
		want++
	}
	if want < minWidth {
		want = minWidth
	}
	for _, s := range niceSteps {
		if s >= want {
			return s
		}
	}
	d := 24 * time.Hour
	for d < want {
		d *= 2
	}
	return d
}

func fmtJitter(a Agg) string {
	if !a.HasJitter {
		return "-"
	}
	return fmtMs(a.Jitter)
}
