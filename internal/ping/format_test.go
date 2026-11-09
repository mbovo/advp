package ping

import (
	"testing"
	"time"
)

func TestFormatRTT(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Microsecond:     "0.045",
		1234 * time.Microsecond:   "1.23",
		12345 * time.Microsecond:  "12.3",
		123456 * time.Microsecond: "123",
	}
	for d, want := range cases {
		if got := formatRTT(d, false); got != want {
			t.Errorf("formatRTT(%v)=%s want %s", d, got, want)
		}
	}
	if got := formatRTT(12345*time.Microsecond, true); got != "12.345" {
		t.Errorf("precise: %s", got)
	}
}
