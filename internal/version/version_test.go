package version

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })

	Version, Commit, Date = "v1.2.3", "0123456789abcdef0123", "2026-01-02T03:04:05Z"
	s := String()
	if !strings.HasPrefix(s, "advp v1.2.3 (0123456789ab, 2026-01-02T03:04:05Z) ") {
		t.Fatalf("unexpected version string %q", s)
	}
	Commit, Date = "", ""
	if s := String(); !strings.HasPrefix(s, "advp v1.2.3 ") || strings.Contains(s, "(") {
		t.Fatalf("unexpected version string %q", s)
	}
}
