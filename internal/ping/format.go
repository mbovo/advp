package ping

import (
	"fmt"
	"time"
)

// formatRTT reproduces iputils' rounding of the per-reply time.
func formatRTT(d time.Duration, precise bool) string {
	us := d.Microseconds()
	if precise {
		return fmt.Sprintf("%d.%03d", us/1000, us%1000)
	}
	switch {
	case us >= 100000-50:
		return fmt.Sprintf("%d", (us+500)/1000)
	case us >= 10000-5:
		return fmt.Sprintf("%d.%01d", (us+50)/1000, ((us+50)%1000)/100)
	case us >= 1000:
		return fmt.Sprintf("%d.%02d", (us+5)/1000, ((us+5)%1000)/10)
	default:
		return fmt.Sprintf("%d.%03d", us/1000, us%1000)
	}
}

// ms3 formats a duration as milliseconds with microsecond precision.
func ms3(d time.Duration) string {
	us := d.Microseconds()
	return fmt.Sprintf("%d.%03d", us/1000, us%1000)
}
