//go:build linux || darwin

package tty

import (
	"os"
	"testing"
)

func TestCbreakRejectsNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if restore, err := Cbreak(int(r.Fd())); err == nil {
		restore()
		t.Fatal("cbreak on a pipe must fail")
	}
}
