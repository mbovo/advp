package tty

import (
	"slices"
	"testing"
)

func TestDecode(t *testing.T) {
	cases := map[string][]Key{
		"q":        {KeyQuit},
		"\x1b":     {KeyQuit},
		"\x1b[A":   nil, // arrow key
		"pv":       {KeyPause, KeyView},
		"r\x1b[B":  {KeyRedraw},
		"x":        nil,
		"p\tr\x0c": {KeyPause, KeyView, KeyRedraw, KeyRedraw},
	}
	for in, want := range cases {
		if got := Decode([]byte(in)); !slices.Equal(got, want) {
			t.Errorf("Decode(%q) = %v, want %v", in, got, want)
		}
	}
}
