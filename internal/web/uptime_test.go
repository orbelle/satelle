package web

import (
	"testing"
	"time"
)

func TestFormatUptimeBoundaries(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "up 0s"},
		{45 * time.Second, "up 45s"},
		{59 * time.Second, "up 59s"},
		{time.Minute, "up 1m"},
		{90 * time.Second, "up 1m"},
		{59*time.Minute + 59*time.Second, "up 59m"},
		{time.Hour, "up 1h 0m"},
		{2*time.Hour + 5*time.Minute, "up 2h 5m"},
		{25*time.Hour + 30*time.Minute, "up 25h 30m"},
		{3*time.Hour + 59*time.Minute, "up 3h 59m"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.want {
			t.Errorf("formatUptime(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
