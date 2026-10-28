package flagscan

import (
	"math"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// minuteMs is the candle width the whole service works at: the Signal window's
// default 1-minute chart, and the width the golden vectors were generated at.
const minuteMs = 60_000

// ─── Per-second aggregate -> 1-minute bar ────────────────────────────────

// aggMinute is the 1-minute bar an aggregate belongs to: floor(start/60000) *
// 60000, where start is the aggregate's window start (StartMs) or, when the feed
// did not stamp one, the last millisecond of its window (EndMs-1). ok is false
// for an aggregate with neither timestamp, which can be placed on no bar.
//
// StartMs is what the live chart folds on too (store.ts foldMinute reads the
// ChartBarMsg's t, which the chart tap fills from Agg.StartMs): using the same
// key is what keeps the scan's forming bar identical to the one on the chart.
func aggMinute(a core.Agg) (int64, bool) {
	ms := a.StartMs
	if ms <= 0 {
		ms = a.EndMs - 1
	}
	if ms <= 0 {
		return 0, false
	}
	return ms / minuteMs * minuteMs, true // ms > 0, so truncation == floor
}

// goodPrice reports whether v can be a price: finite and positive. A zero or NaN
// price in a bar would poison the EMA from that bar on (a NaN never recovers),
// which reads as "warming up" forever; refusing it at the door keeps one bad
// print from silencing a symbol.
func goodPrice(v float64) bool { return v > 0 && !math.IsInf(v, 0) } // NaN > 0 is false

func goodAgg(a core.Agg) bool {
	return goodPrice(a.Open) && goodPrice(a.High) && goodPrice(a.Low) && goodPrice(a.Close)
}

// foldAgg folds one per-second aggregate into the trailing 1-minute bar series
// and reports whether the series changed. It is foldMinute in web/src/store.ts,
// rule for rule:
//
//   - same minute as the last bar: high = max, low = min, close = the latest
//     print (the aggregate's close), volume adds, open stays;
//   - a later minute: a new bar opens with the aggregate's own OHLC;
//   - an EARLIER minute than the last bar: ignored (a late second that arrives
//     after its minute was superseded cannot rewrite history).
//
// bars is modified in place and the (possibly re-allocated) slice returned, so
// the caller must own it exclusively; the service copies every REST slice it
// is handed for exactly that reason.
func foldAgg(bars []core.Bar, a core.Agg) ([]core.Bar, bool) {
	minute, ok := aggMinute(a)
	if !ok || !goodAgg(a) {
		return bars, false
	}
	if n := len(bars); n > 0 {
		last := &bars[n-1]
		if last.T == minute {
			last.H = math.Max(last.H, a.High)
			last.L = math.Min(last.L, a.Low)
			last.C = a.Close
			last.V += a.Vol
			return bars, true
		}
		if minute < last.T {
			return bars, false // stale / out-of-order second
		}
	}
	return append(bars, core.Bar{T: minute, O: a.Open, H: a.High, L: a.Low, C: a.Close, V: a.Vol}), true
}

// ─── REST history ────────────────────────────────────────────────────────

// cleanREST copies the REST bars, dropping any that cannot be right (a
// non-positive or non-finite price) and any that are not strictly newer than
// the one before (REST returns ascending, so this only ever triggers on a
// corrupt payload). The copy matters as much as the filter: the service
// appends to and rewrites its own series in place, and the slice a caller
// returned from Config.Bars may be shared with a cache or a test fixture.
func cleanREST(rest []core.Bar) []core.Bar {
	out := make([]core.Bar, 0, len(rest))
	for _, b := range rest {
		if !goodPrice(b.O) || !goodPrice(b.H) || !goodPrice(b.L) || !goodPrice(b.C) {
			continue
		}
		if n := len(out); n > 0 && b.T <= out[n-1].T {
			continue
		}
		out = append(out, b)
	}
	return out
}

// mergeREST combines a freshly fetched REST history with the series the live
// aggregates have been folding into, and returns the new series (rest and live
// are not modified). REST is authoritative for everything up to ITS last bar;
// the live-folded bars after it are kept - that is the whole rule, and it is
// the same one the first load uses, which is why aggregates that arrive while
// the first backfill is in flight need no separate buffer: they simply fold
// into a live-only series and meet the REST history here.
//
// One bar needs more care: the minute of REST's last bar, when the live series
// has that minute too. REST's copy may be a snapshot of a forming minute, and
// the live copy may hold seconds that arrived while the request was in flight,
// so the two are combined: REST's open (the live series may have started
// mid-minute), the wider of the two high/low ranges (every print is real, so an
// extreme from either side is safe), the larger volume (the live series
// counts only what it saw, REST everything it has), and the close of whichever
// is newer. The live close wins in two cases, both "REST cannot have known a
// print that arrived after it asked":
//
//   - the overlap bar is live's NEWEST bar and liveFresh says an aggregate was
//     folded into it at or after the request started (REST alone would make a
//     bar that closes right after the resync keep a close up to a few seconds
//     stale);
//   - live has moved PAST the minute (a later bar exists) and the request
//     started before that minute ended (startedMs < the minute's end): REST
//     served a snapshot of a forming minute, and live's close for it is the
//     minute's last print. Keeping REST's here would leave a CLOSED bar with a
//     mid-minute close - read by the stop, the EMA and the flag rules - until
//     the next resync, 2.5 to 5 minutes later, and a Signal window opened now
//     would not show that. A request started AFTER the minute ended got the
//     complete minute, so REST's close stays (it may hold a late print live
//     missed).
//
// startedMs is the request's start by the loop's own clock, compared with the
// bars' exchange timestamps: a skew between the two can only keep REST's close
// (the behaviour before the second rule) or pick live's for a minute both sides
// already had complete.
//
// Live bars older than REST's last bar are dropped: a symbol's REST history is
// what a Signal window opened right now would draw, and parity with it is the
// service's contract. With no REST bars at all the live series is returned
// unchanged.
func mergeREST(rest, live []core.Bar, liveFresh bool, startedMs int64) []core.Bar {
	if len(rest) == 0 {
		return live
	}
	lastRest := rest[len(rest)-1].T
	out := make([]core.Bar, 0, len(rest)+len(live))
	out = append(out, rest...)
	for i, b := range live {
		switch {
		case b.T < lastRest:
			// REST has this minute (or deliberately omitted it): it wins.
		case b.T == lastRest:
			r := &out[len(out)-1]
			r.H = math.Max(r.H, b.H)
			r.L = math.Min(r.L, b.L)
			if b.V > r.V {
				r.V = b.V
			}
			superseded := i < len(live)-1 && startedMs < lastRest+minuteMs // live left this minute after REST was asked inside it
			if (liveFresh && i == len(live)-1) || superseded {
				r.C = b.C
			}
		default:
			out = append(out, b)
		}
	}
	return out
}

// ─── ET calendar day ─────────────────────────────────────────────────────

var (
	etOnce sync.Once
	etLoc  *time.Location
)

// etLocation is America/New_York, falling back to a fixed UTC-5 if the zone
// cannot be loaded (it cannot fail with time/tzdata linked in). The zone is
// only used to tell whether a bar belongs to TODAY's ET date, and the date
// changes at 00:00 local, where the market has no bars (04:00-20:00), so an
// hour of error in summer would land inside an empty stretch of the clock.
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

// etDay is the ET calendar date of a ms timestamp as one comparable int
// (yyyymmdd).
func etDay(ms int64) int {
	y, m, d := time.UnixMilli(ms).In(etLocation()).Date()
	return y*10000 + int(m)*100 + d
}
