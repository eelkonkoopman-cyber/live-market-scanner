package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const t0 = int64(1_700_000_000)

func feed(ts *tickerState, sec int64, price float64) {
	ts.applyAgg(core.Agg{
		Sym: ts.sym, Close: price, High: price, Low: price,
		Vol: 10, StartMs: (sec - 1) * 1000, EndMs: sec * 1000,
	})
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestColdStartChg5m(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 10)

	// Immediately after first print: no 5-min history -> null, never a fake %.
	if _, ok := ts.priceAgo(300); ok {
		t.Fatal("expected cold-start null at 300s")
	}
	if r := ts.toRow(0, nil, nil, nil, core.SessionRegular); !core.IsNull(r.Chg5m) {
		t.Fatalf("chg5m should be null at cold start, got %v", r.Chg5m)
	}

	// One second short of the window is still cold.
	ts.syncTo(t0 + 299)
	if _, ok := ts.priceAgo(300); ok {
		t.Fatal("still cold at 299s")
	}

	// At exactly 300s the price-5m-ago is the forward-filled first print.
	ts.syncTo(t0 + 300)
	if p, ok := ts.priceAgo(300); !ok || p != 10 {
		t.Fatalf("price 5m ago want 10 got %v ok=%v", p, ok)
	}

	feed(ts, t0+300, 11)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if core.IsNull(r.Chg5m) || abs(r.Chg5m-10) > 1e-6 {
		t.Fatalf("chg5m want 10 got %v", r.Chg5m)
	}
}

// TestChg15mNeeds960Ring proves the ring is long enough to look back 900s — a
// 900-length ring would read head (now vs now) and report 0%.
func TestChg15mNeeds960Ring(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 100)
	ts.syncTo(t0 + 900)
	feed(ts, t0+900, 110)

	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if core.IsNull(r.Chg15m) || abs(r.Chg15m-10) > 1e-6 {
		t.Fatalf("chg15m want 10 got %v (960-ring lookback)", r.Chg15m)
	}
}

func TestForwardFillSparse(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 50) // trades once...
	// ...then goes quiet; only the 1 Hz clock advances it.
	for s := t0 + 1; s <= t0+300; s++ {
		ts.syncTo(s)
	}
	// price 5m ago must be the forward-filled 50, not undefined.
	if p, ok := ts.priceAgo(300); !ok || p != 50 {
		t.Fatalf("forward-filled price want 50 got %v ok=%v", p, ok)
	}
	feed(ts, t0+300, 55)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if abs(r.Chg5m-10) > 1e-6 { // 55 vs 50
		t.Fatalf("sparse chg5m want 10 got %v", r.Chg5m)
	}
}

func TestHodLodFromBarExtremes(t *testing.T) {
	ts := &tickerState{sym: "X"}
	// bar with high above and low below the close — must drive HOD/LOD, not last.
	ts.applyAgg(core.Agg{Sym: "X", Close: 10, High: 12, Low: 9, Vol: 5, EndMs: t0 * 1000})
	if ts.hod != 12 || ts.lod != 9 {
		t.Fatalf("hod/lod from bar extremes want 12/9 got %v/%v", ts.hod, ts.lod)
	}
	ts.applyAgg(core.Agg{Sym: "X", Close: 11, High: 11, Low: 8, Vol: 5, EndMs: (t0 + 1) * 1000})
	if ts.hod != 12 || ts.lod != 8 { // hod holds, lod extends down
		t.Fatalf("hod/lod want 12/8 got %v/%v", ts.hod, ts.lod)
	}
}

func TestToRowRvolRot(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 5)   // sessVol = 10
	feed(ts, t0+1, 5) // sessVol = 20

	var cum [core.BucketCount]float64
	var cnt [core.BucketCount]uint16
	cum[66] = 10
	cnt[66] = 10 // >= minBaselineSessions
	bt := &core.BaselineTable{
		Cum:   map[string][core.BucketCount]float64{"X": cum},
		Count: map[string][core.BucketCount]uint16{"X": cnt},
	}
	so := int64(40)
	floats := map[string]core.FloatRef{"X": {Ticker: "X", SharesOut: &so}}

	r := ts.toRow(66, bt, floats, nil, core.SessionRegular)
	if core.IsNull(r.Rvol) || abs(r.Rvol-2) > 1e-9 { // 20 / 10
		t.Fatalf("rvol want 2 got %v", r.Rvol)
	}
	if core.IsNull(r.Rot) || abs(r.Rot-0.5) > 1e-9 { // 20 / 40
		t.Fatalf("rot want 0.5 got %v", r.Rot)
	}
	if r.ShsOut == nil || *r.ShsOut != 40 {
		t.Fatalf("shsOut want 40 got %v", r.ShsOut)
	}
}

func TestRvolNullBelowMinSessions(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 5)
	var cum [core.BucketCount]float64
	var cnt [core.BucketCount]uint16
	cum[66] = 10
	cnt[66] = 3 // below minBaselineSessions
	bt := &core.BaselineTable{
		Cum:   map[string][core.BucketCount]float64{"X": cum},
		Count: map[string][core.BucketCount]uint16{"X": cnt},
	}
	if r := ts.toRow(66, bt, nil, nil, core.SessionRegular); !core.IsNull(r.Rvol) {
		t.Fatalf("rvol should be null with too few sessions, got %v", r.Rvol)
	}
}

// TestAvgVolRegularSessionOnly pins AvgVol's definition: the trailing-mean
// cumulative at 16:00 (bucket 144) MINUS the trailing-mean cumulative at
// 09:30 (bucket 66) — i.e. regular-session-only volume, deliberately
// excluding whatever pre/post-market volume is baked into the raw cumulative
// totals. A baseline confusion (e.g. using the full 144 total instead of the
// difference) would produce a different, larger number here.
func TestAvgVolRegularSessionOnly(t *testing.T) {
	ts := &tickerState{sym: "X"}
	var cum [core.BucketCount]float64
	var cnt [core.BucketCount]uint16
	cum[regularOpenBucket] = 1000  // pre-market's share of the trailing mean
	cum[regularCloseBucket] = 6000 // full day's share of the trailing mean
	cnt[regularCloseBucket] = 10   // >= minBaselineSessions
	bt := &core.BaselineTable{
		Cum:   map[string][core.BucketCount]float64{"X": cum},
		Count: map[string][core.BucketCount]uint16{"X": cnt},
	}
	r := ts.toRow(regularOpenBucket, bt, nil, nil, core.SessionRegular)
	if core.IsNull(r.AvgVol) || abs(r.AvgVol-5000) > 1e-9 {
		t.Fatalf("avgVol want 5000 (6000-1000) got %v", r.AvgVol)
	}
}

func TestAvgVolNullBelowMinSessions(t *testing.T) {
	ts := &tickerState{sym: "X"}
	var cum [core.BucketCount]float64
	var cnt [core.BucketCount]uint16
	cum[regularOpenBucket] = 1000
	cum[regularCloseBucket] = 6000
	cnt[regularCloseBucket] = 3 // below minBaselineSessions
	bt := &core.BaselineTable{
		Cum:   map[string][core.BucketCount]float64{"X": cum},
		Count: map[string][core.BucketCount]uint16{"X": cnt},
	}
	if r := ts.toRow(regularOpenBucket, bt, nil, nil, core.SessionRegular); !core.IsNull(r.AvgVol) {
		t.Fatalf("avgVol should be null with too few sessions, got %v", r.AvgVol)
	}
}

// avgVolBaseline is the shared fixture for the PreVolPctAdv tests below:
// AvgVol always resolves to 5000 (6000-1000, >= minBaselineSessions) so each
// test only has to vary the pre-market-volume side of the ratio.
func avgVolBaseline() *core.BaselineTable {
	var cum [core.BucketCount]float64
	var cnt [core.BucketCount]uint16
	cum[regularOpenBucket] = 1000
	cum[regularCloseBucket] = 6000
	cnt[regularCloseBucket] = 10
	return &core.BaselineTable{
		Cum:   map[string][core.BucketCount]float64{"X": cum},
		Count: map[string][core.BucketCount]uint16{"X": cnt},
	}
}

// TestPreVolPctAdvLiveDuringPreSession is the whole point of this field: the
// plain PreVol field stays 0 until the 09:30 freeze, so a scan meant to be
// watched THROUGH pre-market needs a live number instead. scopeVol IS that
// live number while sess == SessionPre.
func TestPreVolPctAdvLiveDuringPreSession(t *testing.T) {
	ts := &tickerState{sym: "X", scopeVol: 500}
	r := ts.toRow(regularOpenBucket, avgVolBaseline(), nil, nil, core.SessionPre)
	if core.IsNull(r.PreVolPctAdv) || abs(r.PreVolPctAdv-10) > 1e-9 { // 500/5000*100
		t.Fatalf("preVolPctAdv want 10 got %v", r.PreVolPctAdv)
	}
}

// TestPreVolPctAdvFrozenAfterBoundary: past 09:30, the field must read the
// FROZEN pre-market volume, not the current (regular-session) scopeVol —
// mixing the two would silently blend two different sessions' volume into
// one number.
func TestPreVolPctAdvFrozenAfterBoundary(t *testing.T) {
	ts := &tickerState{sym: "X", scopeVol: 9_999_999, frozenPre: frozenScope{set: true, vol: 750}}
	r := ts.toRow(regularOpenBucket, avgVolBaseline(), nil, nil, core.SessionRegular)
	if core.IsNull(r.PreVolPctAdv) || abs(r.PreVolPctAdv-15) > 1e-9 { // 750/5000*100, NOT 9999999/5000
		t.Fatalf("preVolPctAdv want 15 (frozen 750, not live scopeVol) got %v", r.PreVolPctAdv)
	}
}

// TestPreVolPctAdvNullBeforeFreezeOutsidePreSession: past 09:30 but the
// freeze never fired for this ticker (never traded pre-market) — must be
// Null, not 0-as-a-real-answer and not a stale live scopeVol read.
func TestPreVolPctAdvNullBeforeFreezeOutsidePreSession(t *testing.T) {
	ts := &tickerState{sym: "X", scopeVol: 42}
	r := ts.toRow(regularOpenBucket, avgVolBaseline(), nil, nil, core.SessionRegular)
	if !core.IsNull(r.PreVolPctAdv) {
		t.Fatalf("preVolPctAdv should be null with no frozen pre-market data, got %v", r.PreVolPctAdv)
	}
}

// TestPreVolPctAdvNullWithoutAvgVol: even with real pre-market volume on
// hand, no baseline means no denominator — must stay Null, never divide by
// zero or silently show a bogus ratio.
func TestPreVolPctAdvNullWithoutAvgVol(t *testing.T) {
	ts := &tickerState{sym: "X", scopeVol: 500}
	if r := ts.toRow(regularOpenBucket, nil, nil, nil, core.SessionPre); !core.IsNull(r.PreVolPctAdv) {
		t.Fatalf("preVolPctAdv should be null with no baseline, got %v", r.PreVolPctAdv)
	}
}

func TestResetSession(t *testing.T) {
	ts := &tickerState{sym: "X"}
	feed(ts, t0, 5)
	ts.prevClose = 4
	ts.resetSession()
	if ts.sessVol != 0 || ts.firstSec != 0 || ts.baseSec != 0 || ts.hodSet {
		t.Fatal("reset incomplete")
	}
	if ts.prevClose != 4 {
		t.Fatal("reset should preserve prevClose")
	}
	if _, ok := ts.priceAgo(60); ok {
		t.Fatal("should be cold after reset")
	}
}

func TestSeedApplySetsVolume(t *testing.T) {
	ts := &tickerState{sym: "X"}
	s := core.Seed{Agg: core.Agg{Sym: "X", Close: 10, High: 11, Low: 9, Vol: 1000, DayOpen: 9.5, EndMs: t0 * 1000}, PrevClose: 8}
	ts.seedApply(s)
	if ts.sessVol != 1000 || ts.last != 10 || ts.prevClose != 8 || ts.hod != 11 || ts.lod != 9 {
		t.Fatalf("seedApply mismatch: vol=%d last=%v prev=%v hod=%v lod=%v", ts.sessVol, ts.last, ts.prevClose, ts.hod, ts.lod)
	}
	// Re-seed must SET (not add) sessVol — guards against double-count on reconnect.
	s.Vol = 1500
	ts.seedApply(s)
	if ts.sessVol != 1500 {
		t.Fatalf("re-seed should SET sessVol to 1500, got %d", ts.sessVol)
	}
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if abs(r.ChgDay-25) > 1e-9 { // (10/8 - 1)*100
		t.Fatalf("chgDay want 25 got %v", r.ChgDay)
	}
}

func TestBars(t *testing.T) {
	ts := &tickerState{sym: "X", barHead: -1}
	for i := 0; i < 60; i++ { // 60s of rising price -> several 15s candles
		feed(ts, t0+int64(i), float64(10+i))
	}
	bars := ts.bars()
	if len(bars) < 3 {
		t.Fatalf("want >=3 candles, got %d", len(bars))
	}
	if bars[len(bars)-1].C <= bars[0].C {
		t.Fatalf("rising candles should end higher: %v -> %v", bars[0].C, bars[len(bars)-1].C)
	}
	for _, b := range bars {
		if b.T == 0 || b.H < b.C || b.L > b.C {
			t.Fatalf("bad OHLC bar: %+v", b)
		}
	}
	// Cold start (no data) -> nil.
	if (&tickerState{sym: "Y", barHead: -1}).bars() != nil {
		t.Fatal("cold-start bars should be nil")
	}
}

// feedBar feeds a full OHLCV bar so session-scoped high/low/vwap can be asserted.
func feedBar(ts *tickerState, sec int64, o, h, l, c, vwap float64, vol int64) {
	ts.applyAgg(core.Agg{
		Sym: ts.sym, Open: o, High: h, Low: l, Close: c, VWAP: vwap, Vol: vol,
		StartMs: (sec - 1) * 1000, EndMs: sec * 1000,
	})
}

func TestSessionScopedMetrics(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	// Pre-market: open 10, prints up to a high of 12, low of 9, last 11.
	feedBar(ts, t0, 10, 10, 10, 10, 10, 100)
	feedBar(ts, t0+1, 11, 12, 11, 11, 11.5, 100) // high 12
	feedBar(ts, t0+2, 11, 11, 9, 11, 10, 200)    // low 9

	r := ts.toRow(0, nil, nil, nil, core.SessionPre)
	if abs(r.ChgSess-10) > 1e-6 { // 11 vs session open 10
		t.Fatalf("chgSess want 10 got %v", r.ChgSess)
	}
	if abs(r.SessHigh-12) > 1e-6 || abs(r.SessLow-9) > 1e-6 {
		t.Fatalf("sessHigh/Low want 12/9 got %v/%v", r.SessHigh, r.SessLow)
	}
	if r.SessVol != 400 {
		t.Fatalf("sessVol want 400 got %d", r.SessVol)
	}
	// VWAP = Σ(vwap*vol)/Σvol = (10*100 + 11.5*100 + 10*200) / 400 = 4150/400.
	if want := 4150.0 / 400.0; abs(r.SessVwap-want) > 1e-6 {
		t.Fatalf("sessVwap want %v got %v", want, r.SessVwap)
	}
}

// TestSessionScopeResetKeepsDayState proves a sub-session boundary resets the
// scoped metrics while the 04:00-anchored day volume carries through.
func TestSessionScopeResetKeepsDayState(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	feedBar(ts, t0, 10, 10, 10, 10, 10, 100)
	feedBar(ts, t0+1, 11, 13, 11, 12, 12, 100) // pre high 13, last 12

	ts.resetSessionScope() // pre -> regular boundary

	// Day volume survives; session scope is wiped pending the first regular print.
	if ts.sessVol != 200 {
		t.Fatalf("day sessVol should survive scope reset, got %d", ts.sessVol)
	}
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if !core.IsNull(r.ChgSess) || !core.IsNull(r.SessHigh) || !core.IsNull(r.SessLow) || !core.IsNull(r.SessVwap) {
		t.Fatalf("scoped metrics should be null right after reset: %+v", r)
	}
	if r.SessVol != 0 {
		t.Fatalf("sessVol should be 0 after scope reset, got %d", r.SessVol)
	}

	// First regular-session bar: chgSess now anchors to the official day open (15),
	// not the pre-market open. DayOpen (the "op" field) carries the official open.
	ts.applyAgg(core.Agg{
		Sym: "X", Open: 15, High: 16, Low: 15, Close: 16.5, VWAP: 16, Vol: 50,
		DayOpen: 15, StartMs: (t0 + 1) * 1000, EndMs: (t0 + 2) * 1000,
	})
	r = ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if abs(r.ChgSess-10) > 1e-6 { // 16.5 vs dayOpen 15
		t.Fatalf("regular chgSess want 10 (vs dayOpen) got %v", r.ChgSess)
	}
	if r.SessVol != 50 {
		t.Fatalf("regular sessVol want 50 got %d", r.SessVol)
	}
}

// frozenAllUnset asserts all fourteen frozen review fields are at their
// "session not completed" defaults: Null floats, 0 vols.
func frozenAllUnset(t *testing.T, r core.Row) {
	t.Helper()
	if !core.IsNull(r.PreChg) || !core.IsNull(r.PreHigh) || !core.IsNull(r.PreLow) ||
		!core.IsNull(r.PreVwap) || !core.IsNull(r.PreGap) || !core.IsNull(r.PreFade) || r.PreVol != 0 ||
		!core.IsNull(r.RthChg) || !core.IsNull(r.RthHigh) || !core.IsNull(r.RthLow) ||
		!core.IsNull(r.RthVwap) || !core.IsNull(r.RthGap) || !core.IsNull(r.RthFade) || r.RthVol != 0 {
		t.Fatalf("frozen fields should all be Null/0, got pre=(%v %v %v %v %v %v %d) rth=(%v %v %v %v %v %v %d)",
			r.PreChg, r.PreHigh, r.PreLow, r.PreVwap, r.PreGap, r.PreFade, r.PreVol,
			r.RthChg, r.RthHigh, r.RthLow, r.RthVwap, r.RthGap, r.RthFade, r.RthVol)
	}
}

// TestToRowFrozenNullBeforeBoundary proves the ten pre*/rth* fields stay
// Null/0 while a session is still in progress (no boundary has fired).
func TestToRowFrozenNullBeforeBoundary(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 9, 11, 10.5, 100)
	frozenAllUnset(t, ts.toRow(0, nil, nil, nil, core.SessionPre))
}

// TestFreezeScopeNoTradesStaysUnset proves a ticker that traded earlier in the
// day but not in the sub-session being frozen keeps an unset slot (Null/0):
// the freeze requires scopeOpenSet, i.e. actual trades in THAT session.
func TestFreezeScopeNoTradesStaysUnset(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 9, 11, 10.5, 100)
	ts.resetSessionScope() // an earlier boundary wiped the scope; no trades since
	ts.freezeScope(core.SessionPre)
	frozenAllUnset(t, ts.toRow(0, nil, nil, nil, core.SessionRegular))
}

// TestFreezeScopeUnknownPrevCloseChgNull proves the chosen representation for
// "traded, but prevClose unknown": the slot freezes with hi/lo/vol/vwap
// populated while chg stays Null via its own validity flag — never a stored
// float32 NaN widened into a fake core.Null. Gap and fade gate independently:
// gap needs prevClose (Null here), fade only needs the session high and the
// end price, so it still freezes.
func TestFreezeScopeUnknownPrevCloseChgNull(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1} // prevClose never seeded
	feedBar(ts, t0, 10, 12, 9, 11, 10.5, 100)
	ts.freezeScope(core.SessionPre)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if !core.IsNull(r.PreChg) {
		t.Fatalf("preChg should be Null without prevClose, got %v", r.PreChg)
	}
	if abs(r.PreHigh-12) > 1e-3 || abs(r.PreLow-9) > 1e-3 || r.PreVol != 100 || abs(r.PreVwap-10.5) > 1e-3 {
		t.Fatalf("pre hi/lo/vol/vwap want 12/9/100/10.5 got %v/%v/%d/%v", r.PreHigh, r.PreLow, r.PreVol, r.PreVwap)
	}
	if !core.IsNull(r.PreGap) {
		t.Fatalf("preGap should be Null without prevClose, got %v", r.PreGap)
	}
	if wantFade := (12.0 - 11.0) / 12.0 * 100; core.IsNull(r.PreFade) || abs(r.PreFade-wantFade) > 1e-3 {
		t.Fatalf("preFade should freeze without prevClose (gates independently): want %v got %v", wantFade, r.PreFade)
	}
}

// TestFreezeScopeGapFade pins the gap and fade definitions against each other
// and against chg with THREE DIFFERENT numbers: prevClose 10, session open 10,
// high 14, last 11 →
//
//	preGap  = pct(high 14, prevClose 10) = +40  (peak gain off the HIGH — a
//	          gap computed from last would give chg's +10 instead),
//	preFade = (14-11)/14*100 ≈ 21.43     (% off the HIGH — a fade computed
//	          off the session open 10 would clamp to 0 instead),
//	preChg  = pct(last 11, prevClose 10) = +10.
//
// Each is asserted against its own definition, so gap≠chg and
// fade-off-high≠fade-off-open are both load-bearing.
func TestFreezeScopeGapFade(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 10, 10, 10, 10, 100)     // session open 10
	feedBar(ts, t0+1, 14, 14, 13, 14, 13.5, 100) // runs to a high of 14
	feedBar(ts, t0+2, 11, 11, 11, 11, 11, 100)   // fades to 11 by the boundary
	ts.freezeScope(core.SessionPre)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)

	if core.IsNull(r.PreGap) || abs(r.PreGap-40) > 1e-3 { // pct(14, 10), NOT pct(11, 10)
		t.Fatalf("preGap want +40 (high 14 vs prevClose 10) got %v", r.PreGap)
	}
	wantFade := (14.0 - 11.0) / 14.0 * 100 // ≈ 21.43, off the HIGH, not off the open
	if core.IsNull(r.PreFade) || abs(r.PreFade-wantFade) > 1e-3 {
		t.Fatalf("preFade want %v (off the high) got %v", wantFade, r.PreFade)
	}
	if core.IsNull(r.PreChg) || abs(r.PreChg-10) > 1e-3 { // pct(11, 10)
		t.Fatalf("preChg want +10 got %v", r.PreChg)
	}
	// The three-different-numbers guarantee itself.
	if abs(r.PreGap-r.PreChg) < 1 || abs(r.PreFade-r.PreGap) < 1 || abs(r.PreFade-r.PreChg) < 1 {
		t.Fatalf("gap/fade/chg must be three different numbers: %v/%v/%v", r.PreGap, r.PreFade, r.PreChg)
	}
}

// TestFreezeScopeFadeZeroAtHigh proves a session that ENDS at its high freezes
// fade == 0 exactly — set, not Null (zero fade is a real, filterable answer).
func TestFreezeScopeFadeZeroAtHigh(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 10, 12, 11, 100) // closes the session AT the high 12
	ts.freezeScope(core.SessionPre)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if core.IsNull(r.PreFade) || r.PreFade != 0 {
		t.Fatalf("preFade want exactly 0 (set) when last == high, got %v", r.PreFade)
	}
	if core.IsNull(r.PreGap) || abs(r.PreGap-20) > 1e-3 { // pct(12, 10)
		t.Fatalf("preGap want +20 got %v", r.PreGap)
	}
}

// TestFreezeScopeFadeClampsNegative proves a last ABOVE the recorded session
// high (possible only via odd data: seed the scope high, then a bare close-only
// print higher) clamps fade to 0, never a negative.
func TestFreezeScopeFadeClampsNegative(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 10, 11, 11, 100) // scope high 12
	// A close-only agg (High 0) moves last above the recorded high without
	// updating scopeHi.
	ts.applyAgg(core.Agg{Sym: "X", Close: 13, Vol: 10, EndMs: (t0 + 1) * 1000})
	ts.freezeScope(core.SessionPre)
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if core.IsNull(r.PreFade) || r.PreFade != 0 {
		t.Fatalf("preFade must clamp to 0 when last > recorded high, got %v", r.PreFade)
	}
}

// TestFreezeScopeIgnoresClosedAndPost proves the freeze is a no-op for
// outgoing sessions that have no review slot (Closed, Post).
func TestFreezeScopeIgnoresClosedAndPost(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 9, 11, 10.5, 100)
	ts.freezeScope(core.SessionClosed)
	ts.freezeScope(core.SessionPost)
	frozenAllUnset(t, ts.toRow(0, nil, nil, nil, core.SessionRegular))
}

// TestResetSessionClearsFrozen proves the day rollover wipes both frozen
// slots — yesterday's pre-market must not survive into today.
func TestResetSessionClearsFrozen(t *testing.T) {
	ts := &tickerState{sym: "X", curveBkt: -1, barHead: -1}
	ts.prevClose = 10
	feedBar(ts, t0, 10, 12, 9, 11, 10.5, 100)
	ts.freezeScope(core.SessionPre)
	ts.resetSessionScope()
	feedBar(ts, t0+1, 15, 16, 14, 15, 15, 200)
	ts.freezeScope(core.SessionRegular)
	if !ts.frozenPre.set || !ts.frozenRth.set {
		t.Fatal("both frozen slots should be set before the rollover")
	}
	ts.resetSession()
	if ts.frozenPre.set || ts.frozenRth.set {
		t.Fatal("resetSession must clear both frozen slots")
	}
	frozenAllUnset(t, ts.toRow(0, nil, nil, nil, core.SessionPre))
}

func TestChgDayAndGap(t *testing.T) {
	ts := &tickerState{sym: "X"}
	ts.prevClose = 10
	ts.applyAgg(core.Agg{Sym: "X", Close: 12, High: 12, Low: 11, DayOpen: 11, Vol: 1, EndMs: t0 * 1000})
	r := ts.toRow(0, nil, nil, nil, core.SessionRegular)
	if abs(r.ChgDay-20) > 1e-9 { // 12 vs 10
		t.Fatalf("chgDay want 20 got %v", r.ChgDay)
	}
	if abs(r.Gap-10) > 1e-9 { // open 11 vs close 10
		t.Fatalf("gap want 10 got %v", r.Gap)
	}
}

// bars() is memoized: every snapshot asks every ticker for its bars, so the
// projection is rebuilt only after the ring or the in-progress bar changed.
func TestBarsMemoized(t *testing.T) {
	ts := &tickerState{barHead: -1}
	ts.updateBar(core.Agg{EndMs: 1 * barIntervalMs, Open: 1, High: 1, Low: 1, Close: 1, Vol: 1})
	ts.updateBar(core.Agg{EndMs: 2 * barIntervalMs, Open: 2, High: 2, Low: 2, Close: 2, Vol: 1})
	a := ts.bars()
	if len(a) != 2 {
		t.Fatalf("want 2 bars got %d", len(a))
	}
	if b := ts.bars(); len(b) != 2 || &a[0] != &b[0] {
		t.Fatal("unchanged ring should return the cached slice")
	}
	// A fold into the in-progress bar invalidates: new slice, new close.
	ts.updateBar(core.Agg{EndMs: 2*barIntervalMs + 1000, Open: 3, High: 3, Low: 3, Close: 3, Vol: 1})
	c := ts.bars()
	if &a[0] == &c[0] {
		t.Fatal("a mutated bar must yield a fresh slice (the old one is shared by snapshots)")
	}
	if c[1].C != 3 || a[1].C != 2 {
		t.Fatalf("new slice should carry the fold (got %v) and the old one must be untouched (got %v)", c[1].C, a[1].C)
	}
}
