package ping

import "time"

// Options mirrors the iputils ping command line plus the advp extensions.
type Options struct {
	Host string

	Count       int           // -c, 0 = unlimited
	Interval    time.Duration // -i
	IntervalSet bool
	Linger      time.Duration // -W
	LingerSet   bool
	Deadline    time.Duration // -w, 0 = none
	Size        int           // -s
	TTL         int           // -t, -1 = system default
	Preload     int           // -l
	Interface   string        // -I
	Mark        int           // -m
	MarkSet     bool
	PMTU        string // -M do|dont|want|probe
	Pattern     []byte // -p
	TOS         int    // -Q, -1 = unset
	SndBuf      int    // -S
	Ident       int    // -e, -1 = auto
	Family      int    // 0, 4 or 6

	Precise     bool // -3
	Audible     bool // -a
	Adaptive    bool // -A
	Broadcast   bool // -b
	StickySrc   bool // -B
	Connect     bool // -C
	Debug       bool // -d
	Timestamps  bool // -D
	Flood       bool // -f
	ForceRDNS   bool // -H
	NoMcastLoop bool // -L
	Numeric     bool // -n
	Outstanding bool // -O
	Quiet       bool // -q
	DontRoute   bool // -r
	RecordRoute bool // -R
	UserLatency bool // -U
	Verbose     bool // -v

	// advp extensions
	Hops        int
	HopInterval time.Duration
	Bucket      time.Duration
	ChartHeight int
	Live        bool // --live: full-screen report refreshed on every packet
	NoReport    bool
	Color       string // auto, always, never
	CSV         string
	HTML        string

	Warnings []string
}

// DefaultOptions returns the iputils defaults.
func DefaultOptions() *Options {
	return &Options{
		Interval:    time.Second,
		Linger:      10 * time.Second,
		Size:        56,
		TTL:         -1,
		Preload:     1,
		TOS:         -1,
		Ident:       -1,
		Hops:        2,
		ChartHeight: 10,
		Color:       "auto",
	}
}
