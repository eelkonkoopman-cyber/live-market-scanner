package flagscan

import (
	"math"
	"reflect"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// sec builds a per-second aggregate at (minute, second) of the epoch minute m.
func sec(sym string, m int64, s int64, o, h, l, c float64, v int64) core.Agg {
	start := m*minuteMs + s*1000
	return core.Agg{Sym: sym, Open: o, High: h, Low: l, Close: c, Vol: v, StartMs: start, EndMs: start + 1000}
}

func TestFoldAggSameMinuteMergesLikeStoreFoldMinute(t *testing.T) {
	// store.ts foldMinute: same minute -> high = max, low = min, close = latest
	// print, volume adds, open stays.
	var bars []core.Bar
	var changed bool
	bars, changed = foldAgg(bars, sec("X", 100, 0, 10, 10.5, 9.9, 10.2, 100))
	if !changed || len(bars) != 1 {
		t.Fatalf("first aggregate must open a bar, got %v changed=%v", bars, changed)
	}
	bars, _ = foldAgg(bars, sec("X", 100, 1, 10.2, 10.9, 10.1, 10.8, 50))
	bars, _ = foldAgg(bars, sec("X", 100, 2, 10.8, 10.85, 9.5, 9.7, 25))
	want := []core.Bar{{T: 100 * minuteMs, O: 10, H: 10.9, L: 9.5, C: 9.7, V: 175}}
	if !reflect.DeepEqual(bars, want) {
		t.Fatalf("bars = %+v, want %+v", bars, want)
	}
}

func TestFoldAggNewMinuteOpensABarAndOlderMinuteIsIgnored(t *testing.T) {
	bars, _ := foldAgg(nil, sec("X", 100, 30, 10, 10, 10, 10, 10))
	bars, _ = foldAgg(bars, sec("X", 101, 0, 11, 11.5, 10.9, 11.2, 20))
	if len(bars) != 2 || bars[1].T != 101*minuteMs || bars[1].O != 11 || bars[1].H != 11.5 || bars[1].L != 10.9 || bars[1].C != 11.2 {
		t.Fatalf("a later minute must open its own bar with the aggregate's OHLC: %+v", bars)
	}
	// A late second of the PREVIOUS minute arrives after the new bar exists:
	// foldMinute returns null for it, so the history is not rewritten.
	before := append([]core.Bar(nil), bars...)
	bars, changed := foldAgg(bars, sec("X", 100, 59, 99, 99, 1, 50, 999))
	if changed || !reflect.DeepEqual(bars, before) {
		t.Fatalf("a stale second must be ignored: changed=%v bars=%+v", changed, bars)
	}
}

func TestFoldAggMinuteKeyIsStartMsThenEndMsMinusOne(t *testing.T) {
	// StartMs 12:00:59.000 belongs to the 12:00 minute.
	m := int64(29_000_000) // some minute index
	a := core.Agg{Sym: "X", Open: 5, High: 5, Low: 5, Close: 5, Vol: 1, StartMs: m*minuteMs + 59_000, EndMs: m*minuteMs + 60_000}
	bars, _ := foldAgg(nil, a)
	if bars[0].T != m*minuteMs {
		t.Fatalf("StartMs key: bar T = %d, want %d", bars[0].T, m*minuteMs)
	}
	// No StartMs: the window's last millisecond (EndMs-1) decides, so a window
	// ending exactly on the minute boundary belongs to the minute that ended.
	b := core.Agg{Sym: "X", Open: 5, High: 5, Low: 5, Close: 5, Vol: 1, EndMs: (m + 1) * minuteMs}
	bars, _ = foldAgg(nil, b)
	if bars[0].T != m*minuteMs {
		t.Fatalf("EndMs-1 fallback: bar T = %d, want %d", bars[0].T, m*minuteMs)
	}
	// Neither timestamp: unplaceable, ignored.
	bars, changed := foldAgg(nil, core.Agg{Sym: "X", Open: 5, High: 5, Low: 5, Close: 5})
	if changed || len(bars) != 0 {
		t.Fatalf("an aggregate with no timestamp must be ignored, got %+v", bars)
	}
}

func TestFoldAggRefusesBadPrices(t *testing.T) {
	for name, a := range map[string]core.Agg{
		"zero open":   sec("X", 100, 0, 0, 1, 1, 1, 1),
		"nan close":   sec("X", 100, 0, 1, 1, 1, math.NaN(), 1),
		"inf high":    sec("X", 100, 0, 1, math.Inf(1), 1, 1, 1),
		"negative lo": sec("X", 100, 0, 1, 1, -1, 1, 1),
	} {
		bars, changed := foldAgg(nil, a)
		if changed || len(bars) != 0 {
			t.Errorf("%s: must be refused (a NaN poisons the EMA for good), got %+v", name, bars)
		}
	}
}

func TestCleanRESTCopiesAndFilters(t *testing.T) {
	src := []core.Bar{
		{T: 1 * minuteMs, O: 1, H: 1, L: 1, C: 1, V: 1},
		{T: 2 * minuteMs, O: 0, H: 1, L: 1, C: 1, V: 1}, // bad price
		{T: 2 * minuteMs, O: 2, H: 2, L: 2, C: 2, V: 1},
		{T: 2 * minuteMs, O: 3, H: 3, L: 3, C: 3, V: 1}, // duplicate T
		{T: 1 * minuteMs, O: 4, H: 4, L: 4, C: 4, V: 1}, // out of order
		{T: 3 * minuteMs, O: 5, H: 5, L: 5, C: 5, V: 1},
	}
	got := cleanREST(src)
	if len(got) != 3 || got[0].T != 1*minuteMs || got[1].O != 2 || got[2].T != 3*minuteMs {
		t.Fatalf("cleanREST = %+v", got)
	}
	got[0].C = 99
	if src[0].C == 99 {
		t.Fatal("cleanREST must copy: the caller's slice may be a shared cache")
	}
}

func bar(m int64, o, h, l, c float64, v int64) core.Bar {
	return core.Bar{T: m * minuteMs, O: o, H: h, L: l, C: c, V: v}
}

func TestMergeRESTRestWinsUpToItsLastBarAndLiveAfterItIsKept(t *testing.T) {
	rest := []core.Bar{bar(1, 1, 1, 1, 1, 10), bar(2, 2, 2, 2, 2, 10), bar(3, 3, 3, 3, 3, 10)}
	live := []core.Bar{
		bar(2, 20, 20, 20, 20, 99), // older than REST's last: REST wins
		bar(4, 4, 4.4, 3.9, 4.2, 7),
		bar(5, 5, 5, 5, 5, 8),
	}
	restCopy := append([]core.Bar(nil), rest...)
	liveCopy := append([]core.Bar(nil), live...)
	got := mergeREST(rest, live, false, 0)
	want := []core.Bar{bar(1, 1, 1, 1, 1, 10), bar(2, 2, 2, 2, 2, 10), bar(3, 3, 3, 3, 3, 10), bar(4, 4, 4.4, 3.9, 4.2, 7), bar(5, 5, 5, 5, 5, 8)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge = %+v\nwant   %+v", got, want)
	}
	if !reflect.DeepEqual(rest, restCopy) || !reflect.DeepEqual(live, liveCopy) {
		t.Fatal("mergeREST must not modify its inputs")
	}
}

func TestMergeRESTOverlapBarCombinesBothViews(t *testing.T) {
	rest := []core.Bar{bar(1, 1, 1, 1, 1, 10), bar(2, 2.00, 2.10, 1.95, 2.05, 500)}
	live := []core.Bar{bar(2, 2.02, 2.20, 1.90, 2.15, 300)} // started mid-minute, saw a wider range
	// Live was touched after the request started: its close is newer.
	got := mergeREST(rest, live, true, 0)
	want := bar(2, 2.00, 2.20, 1.90, 2.15, 500) // REST open, widest range, larger volume, live close
	if got[1] != want {
		t.Fatalf("overlap bar (live fresh) = %+v, want %+v", got[1], want)
	}
	// Live last touched before the request: REST (asked later) has the newer close.
	got = mergeREST(rest, live, false, 0)
	want = bar(2, 2.00, 2.20, 1.90, 2.05, 500)
	if got[1] != want {
		t.Fatalf("overlap bar (live stale) = %+v, want %+v", got[1], want)
	}
	// liveFresh describes only the NEWEST live bar: an overlap bar that live has
	// already left behind keeps REST's close when REST was asked AFTER that minute
	// ended (REST's copy of it is complete)...
	live2 := []core.Bar{bar(2, 2.02, 2.20, 1.90, 2.15, 300), bar(3, 2.2, 2.2, 2.2, 2.2, 5)}
	got = mergeREST(rest, live2, true, 3*minuteMs)
	if got[1].C != 2.05 || len(got) != 3 {
		t.Fatalf("an overlap bar that ended before the request must keep REST's close, got %+v", got)
	}
	// ... but when REST was asked INSIDE that minute and live has since moved on,
	// REST's copy is a mid-minute snapshot and live's close is the minute's last
	// print. Without this the closed bar kept a stale close until the next resync.
	for _, asked := range []int64{2 * minuteMs, 2*minuteMs + 30_000, 3*minuteMs - 1} {
		got = mergeREST(rest, live2, false, asked)
		if got[1].C != 2.15 || got[1].H != 2.20 || got[1].L != 1.90 || got[1].O != 2.00 || got[1].V != 500 || len(got) != 3 {
			t.Fatalf("asked at %d inside the minute, live moved on: want live's close 2.15 on the merged bar, got %+v", asked, got)
		}
	}
	// The newest live bar is never "moved on from": liveFresh alone decides.
	if got = mergeREST(rest, live, false, 2*minuteMs+30_000); got[1].C != 2.05 {
		t.Fatalf("the newest live bar is still forming: REST's close stays unless liveFresh, got %+v", got)
	}
}

// The finding as a probe: REST asked inside minute M, a snapshot of M; live
// folded the rest of M plus the first second of M+1 before the answer arrived.
func TestMergeRESTMinuteEndedInFlightTakesTheLiveClose(t *testing.T) {
	const m = 100
	rest := []core.Bar{bar(m-1, 10, 10, 10, 10, 5), bar(m, 10, 10.3, 9.9, 10.2, 5)}
	live := []core.Bar{bar(m, 10.1, 10.4, 10.0, 10.4, 3), bar(m+1, 10.4, 10.45, 10.4, 10.45, 1)}
	startedMs := int64(m)*minuteMs + 20_000
	if got := mergeREST(rest, live, true, startedMs); got[1].C != 10.4 || len(got) != 3 {
		t.Fatalf("merged minute close = %v, want the minute's last live print 10.4 (got %+v)", got[1].C, got)
	}
}

func TestMergeRESTWithNoRestKeepsLive(t *testing.T) {
	live := []core.Bar{bar(4, 4, 4, 4, 4, 1)}
	if got := mergeREST(nil, live, true, 0); !reflect.DeepEqual(got, live) {
		t.Fatalf("got %+v", got)
	}
}

func TestEtDayUsesTheNewYorkDate(t *testing.T) {
	// 2026-10-05 03:30 UTC is 2026-10-04 23:30 EDT: still Sunday in New York.
	utc := int64(1_791_171_000_000) // 2026-10-05T03:30:00Z
	if got := etDay(utc); got != 20261004 {
		t.Fatalf("etDay(03:30Z in EDT) = %d, want 20261004", got)
	}
	if got := etDay(utc + 3_600_000*5); got != 20261005 { // 08:30Z = 04:30 EDT
		t.Fatalf("etDay(08:30Z) = %d, want 20261005", got)
	}
}
