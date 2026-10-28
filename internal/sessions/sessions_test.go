package sessions

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSessionOf(t *testing.T) {
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	// 2026-06-22 is a Monday; 2026-06-20 a Saturday.
	cases := []struct {
		y, mo, d, h, mi int
		want            core.Session
	}{
		{2026, 6, 22, 5, 0, core.SessionPre},
		{2026, 6, 22, 10, 0, core.SessionRegular},
		{2026, 6, 22, 17, 0, core.SessionPost},
		{2026, 6, 22, 3, 0, core.SessionClosed},  // before pre
		{2026, 6, 22, 21, 0, core.SessionClosed}, // after post
		{2026, 6, 20, 10, 0, core.SessionClosed}, // weekend
	}
	for _, c := range cases {
		got := SessionOf(time.Date(c.y, time.Month(c.mo), c.d, c.h, c.mi, 0, 0, et))
		if got != c.want {
			t.Errorf("%d-%02d-%02d %02d:%02d -> %v, want %v", c.y, c.mo, c.d, c.h, c.mi, got, c.want)
		}
	}
}
