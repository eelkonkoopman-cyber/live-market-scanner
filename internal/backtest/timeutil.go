package backtest

import (
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)
)

// minuteMs is one 1-minute bar.
const minuteMs = 60_000

var (
	etOnce sync.Once
	etLoc  *time.Location
)

// etLocation is America/New_York (fixed UTC-5 only if the zone cannot be loaded,
// which time/tzdata rules out).
func etLocation() *time.Location {
	etOnce.Do(func() {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			loc = time.FixedZone("ET", -5*3600)
		}
		etLoc = loc
	})
	return etLoc
}

// ETDate is the ET calendar date ("2006-01-02") of a ms timestamp.
func ETDate(ms int64) string {
	return time.UnixMilli(ms).In(etLocation()).Format("2006-01-02")
}

// ETMinute is the ET minute of day (0..1439) of a ms timestamp.
func ETMinute(ms int64) int {
	t := time.UnixMilli(ms).In(etLocation())
	return t.Hour()*60 + t.Minute()
}

// ETClock formats a ms timestamp as "15:04" ET.
func ETClock(ms int64) string {
	return time.UnixMilli(ms).In(etLocation()).Format("15:04")
}

// ETStamp formats a ms timestamp as "2006-01-02 15:04" ET.
func ETStamp(ms int64) string {
	return time.UnixMilli(ms).In(etLocation()).Format("2006-01-02 15:04")
}

// ParseHHMM reads a strict "HH:MM" as minutes since midnight (ok=false otherwise).
func ParseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func mustHHMM(s string) int {
	m, ok := ParseHHMM(s)
	if !ok {
		panic("backtest: bad HH:MM " + s)
	}
	return m
}
