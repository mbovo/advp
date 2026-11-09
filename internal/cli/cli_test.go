package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	type tc struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}
	cases := []tc{
		{"help", []string{"-h"}, 0, "Usage", ""},
		{"version", []string{"-V"}, 0, "advp ", ""},
		{"no destination", nil, 2, "", "Destination address required"},
		{"bad count", []string{"-c", "0", "192.0.2.1"}, 2, "", "out of range"},
		{"bad option", []string{"-Z", "192.0.2.1"}, 2, "", "invalid option -- 'Z'"},
		{"family mismatch", []string{"-6", "192.0.2.1"}, 2, "", "Address family"},
		{"broadcast", []string{"255.255.255.255"}, 2, "", "Then -b"},
		{"size too large", []string{"-s", "70000", "192.0.2.1"}, 2, "", "invalid -s value"},
	}
	if os.Geteuid() != 0 {
		cases = append(cases,
			tc{"user flood", []string{"-f", "192.0.2.1"}, 2, "", "cannot flood"},
			tc{"user preload", []string{"-l", "5", "192.0.2.1"}, 2, "", "preload"},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(c.args, &out, &errOut); code != c.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), c.stdout) || !strings.Contains(errOut.String(), c.stderr) {
				t.Fatalf("stdout %q / stderr %q, want %q / %q", out.String(), errOut.String(), c.stdout, c.stderr)
			}
		})
	}
}

func TestResolveLiteral(t *testing.T) {
	ip, zone, isName, err := resolve("fe80::1%en0", 0)
	if err != nil || zone != "en0" || isName || ip.To4() != nil {
		t.Fatalf("ip=%v zone=%q isName=%v err=%v", ip, zone, isName, err)
	}
	if _, _, _, err := resolve("192.0.2.1", 6); err == nil {
		t.Fatal("expected family error")
	}
}
