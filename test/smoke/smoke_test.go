//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "smoke setup:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	if b := os.Getenv("ADVP_BIN"); b != "" {
		if !filepath.IsAbs(b) {
			b = filepath.Join("..", "..", b) // relative to the repository root
		}
		abs, err := filepath.Abs(b)
		if err != nil {
			return 0, err
		}
		bin = abs
		return m.Run(), nil
	}
	dir, err := os.MkdirTemp("", "advp-smoke-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	bin = filepath.Join(dir, "advp")
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/mbovo/advp/internal/version.Version=v0.0.0-smoke",
		"-o", bin, "../../cmd/advp")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return 0, fmt.Errorf("go build: %w", err)
	}
	return m.Run(), nil
}

type result struct {
	code           int
	stdout, stderr string
	elapsed        time.Duration
}

func (r result) String() string {
	return fmt.Sprintf("exit %d after %v\n--- stdout\n%s--- stderr\n%s", r.code, r.elapsed, r.stdout, r.stderr)
}

// advp runs the binary with a timeout and returns its exit code and output.
func advp(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	start := time.Now()
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errOut.String(), elapsed: time.Since(start)}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running advp %v: %v", args, err)
	}
	return r
}

func expect(t *testing.T, r result, code int, wants ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("want exit %d\n%s", code, r)
	}
	all := r.stdout + r.stderr
	for _, w := range wants {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q\n%s", w, r)
		}
	}
}

func expectRe(t *testing.T, r result, patterns ...string) {
	t.Helper()
	for _, p := range patterns {
		if !regexp.MustCompile(p).MatchString(r.stdout + r.stderr) {
			t.Errorf("no match for /%s/\n%s", p, r)
		}
	}
}

var (
	icmpOnce sync.Once
	icmpErr  string
	icmp6Err string
)

// needICMP skips (or fails with ADVP_REQUIRE_ICMP=1) when loopback pings
// cannot work in this environment.
func needICMP(t *testing.T, v6 bool) {
	t.Helper()
	icmpOnce.Do(func() {
		if r := advp(t, "-c", "1", "-W", "2", "--no-hops", "--no-report", "127.0.0.1"); r.code != 0 {
			icmpErr = strings.TrimSpace(r.stderr + r.stdout)
		}
		if r := advp(t, "-6", "-c", "1", "-W", "2", "--no-hops", "--no-report", "::1"); r.code != 0 {
			icmp6Err = strings.TrimSpace(r.stderr + r.stdout)
		}
	})
	reason := icmpErr
	if v6 {
		reason = icmp6Err
	}
	if reason == "" {
		return
	}
	if os.Getenv("ADVP_REQUIRE_ICMP") == "1" {
		t.Fatalf("ICMP required but loopback ping failed:\n%s", reason)
	}
	t.Skipf("ICMP not available here: %s", firstLine(reason))
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "advp:") {
			return l
		}
	}
	return s
}

// ---------------------------------------------------------------- no network

func TestVersion(t *testing.T) {
	r := advp(t, "-V")
	expect(t, r, 0)
	expectRe(t, r, `^advp \S+.* (linux|darwin)/(amd64|arm64) go\d`)
	if os.Getenv("ADVP_BIN") == "" {
		expect(t, r, 0, "advp v0.0.0-smoke")
	}
}

func TestHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		expect(t, advp(t, flag), 0, "Usage", "-c <count>", "--live", "--hops-max", "--csv")
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "Destination address required"},
		{[]string{"-c", "0", "127.0.0.1"}, "out of range"},
		{[]string{"-Z", "127.0.0.1"}, "invalid option -- 'Z'"},
		{[]string{"--bogus", "127.0.0.1"}, "unrecognized option '--bogus'"},
		{[]string{"-t", "300", "127.0.0.1"}, "ttl out of range"},
		{[]string{"-6", "127.0.0.1"}, "Address family for hostname not supported"},
		{[]string{"-s", "70000", "127.0.0.1"}, "invalid -s value"},
		{[]string{"255.255.255.255"}, "Do you want to ping broadcast? Then -b"},
		{[]string{"--hops-max", "--no-hops", "127.0.0.1"}, "--hops-max cannot be combined"},
		{[]string{"-f", "-A", "127.0.0.1"}, "-f and -A are incompatible"},
		{[]string{"-I", "no-such-iface0", "127.0.0.1"}, "unknown iface"},
	}
	if os.Geteuid() != 0 {
		cases = append(cases,
			struct {
				args []string
				want string
			}{[]string{"-i", "0.001", "127.0.0.1"}, "minimal interval for user must be >= 2 ms"},
			struct {
				args []string
				want string
			}{[]string{"-l", "4", "127.0.0.1"}, "cannot set preload to value greater than 3"},
		)
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			expect(t, advp(t, c.args...), 2, c.want)
		})
	}
}

// ---------------------------------------------------------------- loopback

func TestPingIPv4(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-c", "3", "-i", "0.2", "-n", "127.0.0.1")
	expect(t, r, 0,
		"PING 127.0.0.1 (127.0.0.1) 56(84) bytes of data.",
		"--- 127.0.0.1 ping statistics ---",
		"3 packets transmitted, 3 received, 0% packet loss",
		"advp report · 127.0.0.1", "First 2 hop(s) vs destination", "Timeline", "RTT over time (ms)")
	expectRe(t, r,
		`64 bytes from 127\.0\.0\.1: icmp_seq=1 ttl=\d+ time=[\d.]+ ms`,
		`64 bytes from 127\.0\.0\.1: icmp_seq=3 ttl=\d+ time=[\d.]+ ms`,
		`rtt min/avg/max/mdev = [\d.]+/[\d.]+/[\d.]+/[\d.]+ ms`,
		`RTT \(ms\)\s+min [\d.]+ · p50 [\d.]+ · avg [\d.]+ · p75 [\d.]+ · p90 [\d.]+`)
	if strings.Contains(r.stdout, "\x1b[") {
		t.Error("escape sequences written to a pipe")
	}
}

func TestPingIPv6(t *testing.T) {
	needICMP(t, true)
	r := advp(t, "-6", "-c", "2", "-i", "0.2", "-n", "--no-report", "::1")
	expect(t, r, 0, "PING ::1 (::1) 56 data bytes", "2 packets transmitted, 2 received, 0% packet loss")
	expectRe(t, r, `64 bytes from ::1: icmp_seq=2 ttl=\d+ time=[\d.]+ ms`)
}

func TestPacketOptions(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-c", "2", "-i", "0.2", "-n", "-s", "1000", "-p", "ff", "-t", "3", "-D", "-3", "--no-report", "127.0.0.1")
	expect(t, r, 0, "PING 127.0.0.1 (127.0.0.1) 1000(1028) bytes of data.", "2 received")
	expectRe(t, r, `\[\d+\.\d{6}\] 1008 bytes from 127\.0\.0\.1: icmp_seq=1 ttl=\d+ time=\d+\.\d{3} ms`)
}

func TestQuietCountPreload(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-q", "-c", "3", "-l", "3", "-i", "0.5", "-n", "--no-report", "127.0.0.1")
	expect(t, r, 0, "3 packets transmitted, 3 received")
	if strings.Contains(r.stdout, "bytes from") {
		t.Errorf("-q printed replies\n%s", r)
	}
	if r.elapsed > 1500*time.Millisecond {
		t.Errorf("preloaded probes should not wait for the interval (%v)", r.elapsed)
	}
}

func TestDeadline(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-w", "1", "-i", "0.2", "-n", "-q", "--no-report", "127.0.0.1")
	// The probe sent right at the deadline may still be in flight: loss can be > 0.
	expect(t, r, 0)
	expectRe(t, r, `[4-7] packets transmitted, [3-7] received`)
	if r.elapsed < 900*time.Millisecond || r.elapsed > 3*time.Second {
		t.Errorf("-w 1 ran for %v", r.elapsed)
	}
}

func TestFloodUser(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-f", "-i", "0.005", "-c", "50", "-n", "--no-report", "127.0.0.1")
	expect(t, r, 0, "50 packets transmitted, 50 received", "ipg/ewma")
}

func TestHopsOnLoopback(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "-c", "2", "-i", "0.2", "-n", "--hops", "2", "127.0.0.1")
	expect(t, r, 0, "[hop 1] 127.0.0.1 (destination)", "First 2 hop(s) vs destination", "[dest]")
}

func TestHopsMaxOnLoopback(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "--hops-max=5", "-c", "2", "-i", "0.2", "-n", "127.0.0.1")
	expect(t, r, 0,
		"traceroute to 127.0.0.1, 5 hops max, 3 probes per hop",
		"(destination)",
		"→ destination reached at hop 1; no intermediate hops to monitor",
		"destination at hop 1 (traced)")
	expectRe(t, r, ` 1  127\.0\.0\.1  [\d.]+ ms  [\d.]+ ms  [\d.]+ ms  \(destination\)`)
	if strings.Index(r.stdout, "traceroute to") > strings.Index(r.stdout, "PING ") {
		t.Error("route discovery must run before the ping starts")
	}
}

func TestExports(t *testing.T) {
	needICMP(t, false)
	dir := t.TempDir()
	csvPath, htmlPath := filepath.Join(dir, "probes.csv"), filepath.Join(dir, "report.html")
	r := advp(t, "-c", "3", "-i", "0.2", "-n", "-q", "--hops", "1", "--csv", csvPath, "--html", htmlPath, "127.0.0.1")
	expect(t, r, 0, "CSV written to", "HTML report written to")

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rows[0], ",") != "kind,hop_ttl,seq,sent_time,sent_unix_ms,received,rtt_ms,ttl,dups,icmp_error,from" {
		t.Fatalf("csv header: %v", rows[0])
	}
	target := 0
	for _, row := range rows[1:] {
		if row[0] == "target" {
			target++
			if row[5] != "true" || row[10] != "127.0.0.1" {
				t.Errorf("unexpected target row %v", row)
			}
		}
	}
	if target != 3 {
		t.Errorf("%d target rows, want 3", target)
	}
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"<!doctype html>", "<svg", "Round-trip time", "Packet loss", "Jitter", "Timeline"} {
		if !bytes.Contains(html, []byte(w)) {
			t.Errorf("html report misses %q", w)
		}
	}
}

func TestSignals(t *testing.T) {
	needICMP(t, false)
	cmd := exec.Command(bin, "-i", "0.2", "-n", "-q", "--no-hops", "--no-report", "127.0.0.1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGQUIT)
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGINT must end with exit 0: %v\n%s%s", err, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("advp ignored SIGINT")
	}
	if !regexp.MustCompile(`\d+/\d+ packets, \d+% loss, min/avg/ewma/max = `).MatchString(errOut.String()) {
		t.Errorf("SIGQUIT status line missing: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "--- 127.0.0.1 ping statistics ---") {
		t.Errorf("summary missing after SIGINT:\n%s", out.String())
	}
}

func TestLiveFallsBackWithoutTerminal(t *testing.T) {
	needICMP(t, false)
	r := advp(t, "--live", "-c", "1", "-n", "--no-hops", "--no-report", "127.0.0.1")
	expect(t, r, 0, "--live needs a terminal, falling back to line output", "1 received")
}

// TestLiveInPty drives the full-screen view through script(1), which gives
// advp a pseudo-terminal: keys switch the view and quit, and the final
// report is printed on the normal screen afterwards.
func TestLiveInPty(t *testing.T) {
	needICMP(t, false)
	scriptBin, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script(1) not available")
	}
	advpCmd := []string{bin, "--live", "-i", "0.2", "-n", "--hops", "1", "127.0.0.1"}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command(scriptBin, "-qfec", strings.Join(advpCmd, " "), "/dev/null")
	case "darwin":
		cmd = exec.Command(scriptBin, append([]string{"-q", "/dev/null"}, advpCmd...)...)
	default:
		t.Skip("no pty helper for " + runtime.GOOS)
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLUMNS=120", "LINES=40")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_, _ = io.WriteString(stdin, "v")
		time.Sleep(700 * time.Millisecond)
		_, _ = io.WriteString(stdin, "q")
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("live view did not quit on 'q':\n%q", out.String())
	}
	s := out.String()
	enter, leave := strings.Index(s, "\x1b[?1049h"), strings.LastIndex(s, "\x1b[?1049l")
	if enter < 0 || leave < enter {
		t.Fatalf("alternate screen not entered/left:\n%q", s)
	}
	live := s[enter:leave]
	for _, w := range []string{"advp ▸ PING 127.0.0.1", "view: report", "view: chart", "RTT over time"} {
		if !strings.Contains(live, w) {
			t.Errorf("live view misses %q", w)
		}
	}
	if frames := strings.Count(live, "\x1b[H"); frames < 5 {
		t.Errorf("only %d frames drawn", frames)
	}
	if !strings.Contains(s[leave:], "--- 127.0.0.1 ping statistics ---") || !strings.Contains(s[leave:], "advp report") {
		t.Errorf("final report missing after the live view:\n%q", s[leave:])
	}
}
