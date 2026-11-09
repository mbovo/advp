package cli

import (
	"errors"
	"testing"
	"time"
)

func TestParseArgsGetoptStyle(t *testing.T) {
	o, err := parseArgs([]string{"-qnc5", "-i0.5", "example.com", "-W", "2", "-s", "100", "--hops=3", "--bucket", "30s", "-D"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Quiet || !o.Numeric || o.Count != 5 || o.Interval != 500*time.Millisecond || o.Linger != 2*time.Second ||
		o.Size != 100 || o.Host != "example.com" || o.Hops != 3 || o.Bucket != 30*time.Second || !o.Timestamps {
		t.Fatalf("unexpected options: %+v", o)
	}
	o, err = parseArgs([]string{"-f", "-p", "ff00", "-Q", "0x10", "-t", "7", "-M", "do", "--", "-host"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Flood || o.Interval != 0 || o.TOS != 16 || o.TTL != 7 || o.PMTU != "do" || o.Host != "-host" || len(o.Pattern) != 2 {
		t.Fatalf("unexpected options: %+v", o)
	}
	for _, bad := range [][]string{{}, {"-c", "0", "h"}, {"-t", "300", "h"}, {"-Z", "h"}, {"-4", "-6", "h"}, {"-M", "x", "h"}, {"-c"}, {"--nope", "h"}, {"-p", "zz", "h"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	if _, err := parseArgs([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Errorf("want errHelp, got %v", err)
	}
	if o, _ := parseArgs([]string{"-T", "tsonly", "h"}); len(o.Warnings) != 1 {
		t.Errorf("expected a warning for -T")
	}
}

func TestParseLive(t *testing.T) {
	for _, flag := range []string{"--live", "--tui", "--dashboard"} {
		o, err := parseArgs([]string{flag, "h"})
		if err != nil || !o.Live {
			t.Errorf("%s: live=%v err=%v", flag, o != nil && o.Live, err)
		}
	}
	if o, _ := parseArgs([]string{"h"}); o.Live {
		t.Error("live must be off by default")
	}
}
