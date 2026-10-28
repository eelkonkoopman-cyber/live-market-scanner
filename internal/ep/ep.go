// Package ep holds the pure rules of the Episodic Pivot (EP) scans: the
// earnings-headline matcher, the daily-bar statistics (3-month change, ADR20)
// and the EP Watch qualification. No I/O, no clocks: everything is passed in,
// so the tests need no network. See docs/episodic-pivot.md for the rules and
// the backtest behind them.
package ep

import (
	"math"
	"regexp"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Rule constants of the tested setup.
const (
	MinGapPct     = 10.0 // gap vs previous close, %
	MinPrice      = 5.0  // open / last, $
	MaxChg3mPct   = 10.0 // "neglected": close(D-1) <= 1.10 x close ~63 sessions earlier
	PaceNowX      = 1.0  // volume so far >= 1x average daily volume ...
	PaceProjX     = 3.0  // ... OR projected (rvol) >= 3x
	NeglectBars   = 63   // sessions back for the neglect reference
	AdrWindow     = 20   // ADR20
	MinAdrDays    = 5    // fewer daily bars than this: ADR unknown
	ORMinutes     = 5    // opening range 09:30-09:35
	WatchEndMin   = 9*60 + 30
	BreakEndMin   = 11 * 60
	OREndMin      = 9*60 + 35
	RegularOpenMn = 9*60 + 30
)

// earnRe is the earnings-catalyst keyword set (case-insensitive, whole words;
// "Q1".."Q4" as a token). Deliberately NOT "shares are trading higher" style
// movers copy: those carry none of these words.
var earnRe = regexp.MustCompile(`(?i)\b(earnings|results|quarter|q[1-4]|revenue|eps|guidance|reports|beats|misses|raises|outlook)\b`)

// IsEarnings reports whether a headline looks like an earnings catalyst.
func IsEarnings(title string) bool { return earnRe.MatchString(title) }

// Stats derives the 3-month change and ADR20 from daily bars ascending by date
// and ending at the previous session (D-1). chg3m is % of close(D-1) over the
// close 63 sessions earlier (Null when fewer than 64 bars). adrPct is the mean
// of (high/low - 1) over the last 20 bars in %, using what is available when
// fewer (adrDays says how many; Null below MinAdrDays).
func Stats(bars []core.DailyBar) (chg3m, adrPct float64, adrDays int) {
	chg3m, adrPct = core.Null, core.Null
	n := len(bars)
	if n == 0 {
		return
	}
	if n > NeglectBars {
		if ref := bars[n-1-NeglectBars].C; ref > 0 && bars[n-1].C > 0 {
			chg3m = (bars[n-1].C/ref - 1) * 100
		}
	}
	var sum float64
	for i := max(0, n-AdrWindow); i < n; i++ {
		if bars[i].L > 0 && bars[i].H >= bars[i].L {
			sum += bars[i].H/bars[i].L - 1
			adrDays++
		}
	}
	if adrDays >= MinAdrDays {
		adrPct = sum / float64(adrDays) * 100
	}
	return
}

// Input is everything EP Watch judges for one symbol. Null (NaN) means unknown.
type Input struct {
	GapPct   float64 // gap vs previous close, % (pre-market: last; after the open: the open)
	Price    float64 // open (RTH) or last (pre-market)
	Vol      float64 // cumulative volume today
	AvgVol   float64 // average daily volume (available days)
	Rvol     float64 // intraday relative volume (projection)
	Earnings bool    // an earnings headline since the previous 16:00 ET
	Chg3m    float64 // % over ~63 sessions to D-1
}

// VolX is volume so far as a multiple of the average daily volume.
func VolX(vol, avg float64) float64 {
	if avg <= 0 || vol < 0 {
		return core.Null
	}
	return vol / avg
}

// Pass applies every EP Watch rule. Unknown inputs fail (never guess).
func Pass(in Input) bool {
	if math.IsNaN(in.GapPct) || in.GapPct < MinGapPct {
		return false
	}
	if math.IsNaN(in.Price) || in.Price < MinPrice {
		return false
	}
	if !in.Earnings {
		return false
	}
	if math.IsNaN(in.Chg3m) || in.Chg3m > MaxChg3mPct {
		return false
	}
	vx := VolX(in.Vol, in.AvgVol)
	paceNow := !math.IsNaN(vx) && vx >= PaceNowX
	paceProj := !math.IsNaN(in.Rvol) && in.Rvol >= PaceProjX
	return paceNow || paceProj
}

// StopPrice is the planned stop: price - 1 x ADR20 (adrPct in %).
func StopPrice(price, adrPct float64) float64 {
	if math.IsNaN(adrPct) || price <= 0 {
		return core.Null
	}
	return price * (1 - adrPct/100)
}

// PrevClose16 is the previous 16:00 ET cut-off before now: today's 16:00 when
// now is past it, otherwise the previous weekday's (no holiday calendar: a
// holiday only widens the window).
func PrevClose16(now time.Time, et *time.Location) time.Time {
	n := now.In(et)
	d := time.Date(n.Year(), n.Month(), n.Day(), 16, 0, 0, 0, et)
	if !n.Before(d) {
		return d
	}
	d = d.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// MinuteOfDay is the ET minute of day for an epoch-ms instant.
func MinuteOfDay(ms int64, et *time.Location) int {
	t := time.UnixMilli(ms).In(et)
	return t.Hour()*60 + t.Minute()
}
