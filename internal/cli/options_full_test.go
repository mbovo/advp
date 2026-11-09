package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/ping"
)

func TestParseEveryOption(t *testing.T) {
	cases := []struct {
		args  []string
		check func(o *ping.Options) bool
	}{
		{[]string{"-3"}, func(o *ping.Options) bool { return o.Precise }},
		{[]string{"-4"}, func(o *ping.Options) bool { return o.Family == 4 }},
		{[]string{"-6"}, func(o *ping.Options) bool { return o.Family == 6 }},
		{[]string{"-a"}, func(o *ping.Options) bool { return o.Audible }},
		{[]string{"-A"}, func(o *ping.Options) bool { return o.Adaptive }},
		{[]string{"-b"}, func(o *ping.Options) bool { return o.Broadcast }},
		{[]string{"-B"}, func(o *ping.Options) bool { return o.StickySrc }},
		{[]string{"-C"}, func(o *ping.Options) bool { return o.Connect }},
		{[]string{"-d"}, func(o *ping.Options) bool { return o.Debug }},
		{[]string{"-D"}, func(o *ping.Options) bool { return o.Timestamps }},
		{[]string{"-f"}, func(o *ping.Options) bool { return o.Flood && o.Interval == 0 }},
		{[]string{"-f", "-i", "0.01"}, func(o *ping.Options) bool { return o.Flood && o.Interval == 10*time.Millisecond }},
		{[]string{"-n", "-H"}, func(o *ping.Options) bool { return o.ForceRDNS && !o.Numeric }},
		{[]string{"-H", "-n"}, func(o *ping.Options) bool { return o.Numeric && !o.ForceRDNS }},
		{[]string{"-L"}, func(o *ping.Options) bool { return o.NoMcastLoop }},
		{[]string{"-O"}, func(o *ping.Options) bool { return o.Outstanding }},
		{[]string{"-q"}, func(o *ping.Options) bool { return o.Quiet }},
		{[]string{"-r"}, func(o *ping.Options) bool { return o.DontRoute }},
		{[]string{"-R"}, func(o *ping.Options) bool { return o.RecordRoute }},
		{[]string{"-U"}, func(o *ping.Options) bool { return o.UserLatency }},
		{[]string{"-v"}, func(o *ping.Options) bool { return o.Verbose }},
		{[]string{"-c", "7"}, func(o *ping.Options) bool { return o.Count == 7 }},
		{[]string{"-c0x10"}, func(o *ping.Options) bool { return o.Count == 16 }},
		{[]string{"-i", "0,5"}, func(o *ping.Options) bool { return o.Interval == 500*time.Millisecond && o.IntervalSet }},
		{[]string{"-I", "eth0"}, func(o *ping.Options) bool { return o.Interface == "eth0" }},
		{[]string{"-l", "3"}, func(o *ping.Options) bool { return o.Preload == 3 }},
		{[]string{"-m", "9"}, func(o *ping.Options) bool { return o.Mark == 9 && o.MarkSet }},
		{[]string{"-M", "want"}, func(o *ping.Options) bool { return o.PMTU == "want" }},
		{[]string{"-p", "abc"}, func(o *ping.Options) bool { return bytes.Equal(o.Pattern, []byte{0x0a, 0xbc}) }},
		{[]string{"-Q", "255"}, func(o *ping.Options) bool { return o.TOS == 255 }},
		{[]string{"-s", "0"}, func(o *ping.Options) bool { return o.Size == 0 }},
		{[]string{"-S", "1"}, func(o *ping.Options) bool { return o.SndBuf == 1 }},
		{[]string{"-t", "255"}, func(o *ping.Options) bool { return o.TTL == 255 }},
		{[]string{"-w", "2.5"}, func(o *ping.Options) bool { return o.Deadline == 2500*time.Millisecond }},
		{[]string{"-W", "0.3"}, func(o *ping.Options) bool { return o.Linger == 300*time.Millisecond && o.LingerSet }},
		{[]string{"-e", "65535"}, func(o *ping.Options) bool { return o.Ident == 65535 }},
		{[]string{"-T", "tsonly"}, func(o *ping.Options) bool { return len(o.Warnings) == 1 }},
		{[]string{"-F", "0"}, func(o *ping.Options) bool { return len(o.Warnings) == 1 }},
		{[]string{"-N", "name"}, func(o *ping.Options) bool { return len(o.Warnings) == 1 }},
		{[]string{"--hops", "5"}, func(o *ping.Options) bool { return o.Hops == 5 }},
		{[]string{"--no-hops"}, func(o *ping.Options) bool { return o.Hops == 0 }},
		{[]string{"--hop-interval", "2s"}, func(o *ping.Options) bool { return o.HopInterval == 2*time.Second }},
		{[]string{"--hop-interval=0.5"}, func(o *ping.Options) bool { return o.HopInterval == 500*time.Millisecond }},
		{[]string{"--bucket", "1m"}, func(o *ping.Options) bool { return o.Bucket == time.Minute }},
		{[]string{"--chart-height", "20"}, func(o *ping.Options) bool { return o.ChartHeight == 20 }},
		{[]string{"--csv", "a.csv"}, func(o *ping.Options) bool { return o.CSV == "a.csv" }},
		{[]string{"--html=b.html"}, func(o *ping.Options) bool { return o.HTML == "b.html" }},
		{[]string{"--no-report"}, func(o *ping.Options) bool { return o.NoReport }},
		{[]string{"--color", "always"}, func(o *ping.Options) bool { return o.Color == "always" }},
		{[]string{"--no-color"}, func(o *ping.Options) bool { return o.Color == "never" }},
		{[]string{}, func(o *ping.Options) bool {
			d := ping.DefaultOptions()
			return o.Interval == d.Interval && o.Size == 56 && o.TTL == -1 && o.Hops == 2 && o.Color == "auto" && o.Count == 0
		}},
	}
	for _, c := range cases {
		o, err := parseArgs(append(c.args, "dest"))
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if o.Host != "dest" || !c.check(o) {
			t.Errorf("%v: unexpected options %+v", c.args, o)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string][]string{
		"invalid argument":                {"-c", "abc", "h"},
		"bad timing interval":             {"-i", "-1", "h"},
		"bad linger time":                 {"-W", "0", "h"},
		"bad wait time":                   {"-w", "x", "h"},
		"TOS bits must be in range":       {"-Q", "256", "h"},
		"patterns must be specified":      {"-p", "0123456789abcdef0123456789abcdef00", "h"},
		"out of range":                    {"-e", "65536", "h"},
		"ttl out of range":                {"-t", "-1", "h"},
		"invalid -M argument":             {"-M", "maybe", "h"},
		"only one -4 or -6":               {"-6", "-4", "h"},
		"requires an argument -- 'c'":     {"h", "-c"},
		"option '--csv' requires":         {"h", "--csv"},
		"doesn't allow an argument":       {"--live=yes", "h"},
		"unrecognized option '--bogus'":   {"--bogus", "h"},
		"--bucket must be > 0":            {"--bucket", "0s", "h"},
		"--hop-interval must be >= 100ms": {"--hop-interval", "50ms", "h"},
		"invalid --color value":           {"--color", "rainbow", "h"},
		"-f and -A are incompatible":      {"-f", "-A", "h"},
		"Destination address required":    {"-c", "1"},
	}
	for want, args := range cases {
		_, err := parseArgs(args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want error containing %q", args, err, want)
		}
	}
}

func TestParseDoubleDash(t *testing.T) {
	o, err := parseArgs([]string{"-c", "1", "--", "-weird-host"})
	if err != nil || o.Host != "-weird-host" || o.Count != 1 {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if _, err := parseArgs([]string{"--version"}); !errors.Is(err, errVersion) {
		t.Fatalf("--version: %v", err)
	}
	if _, err := parseArgs([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Fatalf("--help: %v", err)
	}
}
