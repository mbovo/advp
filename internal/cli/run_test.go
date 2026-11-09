package cli

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mbovo/advp/internal/icmp"
	"github.com/mbovo/advp/internal/icmptest"
)

// fakeNet replaces openConn: every socket is a simulated connection whose
// behaviour depends on the requested configuration (TTL -1 = destination).
type fakeNet struct {
	mu     sync.Mutex
	cfgs   []icmp.Config
	conns  []*icmptest.Conn
	nextID uint16
	fail   func(cfg icmp.Config) error
	route  func(cfg icmp.Config) icmptest.Reply
	raw    bool
}

func (f *fakeNet) open(cfg icmp.Config) (icmp.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, cfg)
	if f.fail != nil {
		if err := f.fail(cfg); err != nil {
			return nil, err
		}
	}
	f.nextID++
	reply := icmptest.EchoReply(cfg.Dst, time.Millisecond, 57)
	if f.route != nil {
		reply = f.route(cfg)
	}
	c := icmptest.New(1000+f.nextID, reply)
	c.SetRaw(f.raw)
	f.conns = append(f.conns, c)
	return c, nil
}

func (f *fakeNet) mainCfg() icmp.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfgs[0]
}

func useFakeNet(t *testing.T, f *fakeNet) *fakeNet {
	t.Helper()
	old := openConn
	openConn = f.open
	t.Cleanup(func() { openConn = old })
	return f
}

// hopRoute answers like a path whose destination is at hop dest.
func hopRoute(dest int) func(cfg icmp.Config) icmptest.Reply {
	return func(cfg icmp.Config) icmptest.Reply {
		if cfg.TTL > 0 && cfg.TTL < dest {
			return icmptest.TimeExceeded(net.IPv4(10, 0, 0, byte(cfg.TTL)).To4(), time.Duration(cfg.TTL)*time.Millisecond)
		}
		return icmptest.EchoReply(cfg.Dst, time.Duration(dest)*time.Millisecond, 57)
	}
}

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func mustContain(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestRunPingWithReport(t *testing.T) {
	useFakeNet(t, &fakeNet{})
	code, out, errOut := run(t, "-c", "3", "-i", "0.01", "--color", "never", "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	mustContain(t, out,
		"PING 192.0.2.1 (192.0.2.1) 56(84) bytes of data.",
		"64 bytes from 192.0.2.1: icmp_seq=1 ttl=57 time=",
		"--- 192.0.2.1 ping statistics ---",
		"3 packets transmitted, 3 received, 0% packet loss",
		"advp report", "First 2 hop(s) vs destination", "Timeline", "RTT over time")
	if strings.Contains(out, "\x1b[") {
		t.Error("--color never produced escape sequences")
	}
}

func TestRunHopsAndColor(t *testing.T) {
	f := useFakeNet(t, &fakeNet{route: hopRoute(5)})
	code, out, _ := run(t, "-c", "2", "-i", "0.01", "--hops", "3", "--hop-interval", "100ms", "--color", "always", "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "[hop 1] 10.0.0.1", "[hop 3] 10.0.0.3", "First 3 hop(s)", "\x1b[")
	if len(f.cfgs) != 4 || f.cfgs[1].TTL != 1 || f.cfgs[3].TTL != 3 || f.cfgs[1].Ident != -1 {
		t.Fatalf("unexpected socket configs: %+v", f.cfgs)
	}
	for _, c := range f.conns {
		if !c.Closed() {
			t.Error("socket not closed at exit")
		}
	}
}

func TestRunHopsMax(t *testing.T) {
	f := useFakeNet(t, &fakeNet{route: hopRoute(4)})
	code, out, _ := run(t, "--hops-max=8", "-c", "2", "-i", "0.01", "-n", "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out,
		"traceroute to 192.0.2.1, 8 hops max, 3 probes per hop",
		" 4  192.0.2.1", "→ destination reached at hop 4; monitoring hops 1-3",
		"Route to destination (3 hops traced)", "destination at hop 4 (traced)")
	if len(f.cfgs) != 9 {
		t.Fatalf("opened %d sockets, want 1+8", len(f.cfgs))
	}
}

func TestRunNoHops(t *testing.T) {
	f := useFakeNet(t, &fakeNet{})
	if code, out, _ := run(t, "--no-hops", "-c", "1", "192.0.2.1"); code != 0 || strings.Contains(out, "hop(s)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(f.cfgs) != 1 {
		t.Fatalf("opened %d sockets", len(f.cfgs))
	}
}

func TestRunSocketConfig(t *testing.T) {
	f := useFakeNet(t, &fakeNet{})
	code, _, errOut := run(t, "-c", "1", "-t", "7", "-Q", "0x10", "-m", "5", "-M", "do", "-r", "-d", "-L", "-C",
		"-S", "4096", "-e", "77", "-I", "127.0.0.1", "--no-hops", "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := f.mainCfg()
	if c.TTL != 7 || c.TOS != 16 || c.Mark != 5 || !c.MarkSet || c.PMTU != "do" || !c.DontRoute || !c.Debug ||
		!c.NoMcastLoop || !c.Connect || c.SndBuf != 4096 || c.Ident != 77 || !c.Src.Equal(net.IPv4(127, 0, 0, 1)) || c.V6 {
		t.Fatalf("options not propagated: %+v", c)
	}
}

func TestRunInterfaceByName(t *testing.T) {
	ifs, err := net.Interfaces()
	if err != nil || len(ifs) == 0 {
		t.Skip("no network interfaces")
	}
	f := useFakeNet(t, &fakeNet{})
	if code, _, errOut := run(t, "-c", "1", "--no-hops", "-I", ifs[0].Name, "192.0.2.1"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if c := f.mainCfg(); c.IfName != ifs[0].Name || c.IfIndex != ifs[0].Index {
		t.Fatalf("interface not propagated: %+v", c)
	}
	if code, _, errOut := run(t, "-I", "nope-does-not-exist0", "192.0.2.1"); code != 2 || !strings.Contains(errOut, "unknown iface") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestRunIPv6(t *testing.T) {
	f := useFakeNet(t, &fakeNet{})
	code, out, errOut := run(t, "-c", "1", "--no-hops", "-R", "::1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	mustContain(t, out, "PING ::1 (::1) 56 data bytes")
	mustContain(t, errOut, "-R is an IPv4 option, ignored")
	if !f.mainCfg().V6 {
		t.Fatal("IPv6 not requested")
	}
	if code, _, errOut := run(t, "-s", "65528", "::1"); code != 2 || !strings.Contains(errOut, "0 <= value <= 65527") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestRunWarnings(t *testing.T) {
	useFakeNet(t, &fakeNet{})
	code, _, errOut := run(t, "-c", "1", "--no-hops", "-R", "-T", "tsonly", "-F", "1", "-N", "name", "--live", "192.0.2.9", "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	mustContain(t, errOut,
		"warning: option -T", "warning: option -F", "warning: option -N",
		"only the last one is used",
		"-R needs a raw socket",
		"--live needs a terminal")
}

func TestRunSocketErrors(t *testing.T) {
	useFakeNet(t, &fakeNet{fail: func(icmp.Config) error { return errors.New("socket: permission denied") }})
	if code, _, errOut := run(t, "192.0.2.1"); code != 2 || !strings.Contains(errOut, "advp: socket: permission denied") {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	// Hop sockets failing disables hop probing but keeps pinging; opened hops are closed.
	f := useFakeNet(t, &fakeNet{fail: func(c icmp.Config) error {
		if c.TTL == 2 {
			return errors.New("too many open files")
		}
		return nil
	}})
	code, out, errOut := run(t, "-c", "1", "192.0.2.1")
	if code != 0 || strings.Contains(out, "hop(s)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, errOut, "hop probing disabled: too many open files")
	if !f.conns[1].Closed() {
		t.Error("hop 1 socket leaked after hop 2 failed")
	}
}

func TestRunNoReplyExitCode(t *testing.T) {
	useFakeNet(t, &fakeNet{route: func(icmp.Config) icmptest.Reply { return icmptest.Silent() }})
	code, out, _ := run(t, "-c", "2", "-i", "0.01", "-W", "0.1", "--no-hops", "-q", "192.0.2.1")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	mustContain(t, out, "2 packets transmitted, 0 received, 100% packet loss")
}

func TestRunExports(t *testing.T) {
	useFakeNet(t, &fakeNet{})
	dir := t.TempDir()
	csvPath, htmlPath := filepath.Join(dir, "r.csv"), filepath.Join(dir, "r.html")
	code, out, _ := run(t, "-c", "2", "-i", "0.01", "--no-report", "--csv", csvPath, "--html="+htmlPath, "192.0.2.1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "CSV written to "+csvPath, "HTML report written to "+htmlPath)
	if strings.Contains(out, "advp report") {
		t.Error("--no-report ignored")
	}
	for _, p := range []string{csvPath, htmlPath} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Errorf("%s not written: %v", p, err)
		}
	}
	if code, _, errOut := run(t, "-c", "1", "--no-hops", "--csv", filepath.Join(dir, "missing", "x.csv"), "192.0.2.1"); code != 0 ||
		!strings.Contains(errOut, "advp: csv:") {
		t.Fatalf("csv write error not reported: %d %s", code, errOut)
	}
}

func TestRunLocalhostName(t *testing.T) {
	useFakeNet(t, &fakeNet{})
	code, out, _ := run(t, "-4", "-c", "1", "--no-hops", "-n", "localhost")
	if code != 0 {
		t.Skipf("localhost does not resolve here (exit %d)", code)
	}
	mustContain(t, out, "PING localhost (127.0.0.1)", "--- localhost ping statistics ---")
}

func TestIsBroadcast(t *testing.T) {
	if !isBroadcast(net.IPv4bcast) || isBroadcast(net.IPv4(127, 0, 0, 1)) || isBroadcast(net.IPv6loopback) {
		t.Fatal("basic broadcast detection")
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil {
			continue
		}
		if ones, _ := n.Mask.Size(); ones >= 31 || len(n.Mask) != net.IPv4len {
			continue
		}
		b := make(net.IP, 4)
		for i := range b {
			b[i] = n.IP.To4()[i] | ^n.Mask[i]
		}
		if !isBroadcast(b) {
			t.Errorf("%v is the broadcast of %v", b, n)
		}
	}
}
