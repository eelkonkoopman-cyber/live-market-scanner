package scan

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// textbookIntradayFlag is a fully-populated INTRADAY reference for a clean
// session setup: a 12% pole this morning, a 9-bucket (45-minute) base spanning
// 1.8% that gave back 22% on volume down to 55% of the pole's. Every value is
// deliberately different from textbookFlag() — including BaseHigh (13.2 vs the
// daily 12.5) — so any cross-talk between the two overlays shows up as a wrong
// number, not a coincidence.
func textbookIntradayFlag() core.FlagRef {
	return core.FlagRef{
		Score: 76.0, State: 1, PoleGain: 12.0, RangePct: 1.8, Retrace: 22.0,
		VolDry: 0.55, Bars: 9, BaseHigh: 13.2, AsOf: "2026-07-31",
	}
}

// The overlay must transfer every measurement the intraday worker produced
// onto the row, including the int->float64 widening of the bucket count.
func TestScannerIntradayFlagOverlayStampsAllFields(t *testing.T) {
	s := New(nil)
	s.SetIntradayFlags(map[string]core.FlagRef{"TIGHT": textbookIntradayFlag()})
	(<-s.ctrl)(s)

	// Last is under the intraday BaseHigh, so State passes through unpromoted.
	snap := snapOf(1000, mkRow("TIGHT", 12.9, 1))
	s.overlayFlags(snap)
	r := snap.Rows[0]

	for _, c := range []struct {
		field string
		got   float64
		want  float64
	}{
		{core.FieldIFlagScore, r.IFlagScore, 76.0},
		{core.FieldIFlagState, r.IFlagState, 1},
		{core.FieldIFlagPole, r.IFlagPole, 12.0},
		{core.FieldIFlagRange, r.IFlagRange, 1.8},
		{core.FieldIFlagRetrace, r.IFlagRetrace, 22.0},
		{core.FieldIFlagVolDry, r.IFlagVolDry, 0.55},
		{core.FieldIFlagBars, r.IFlagBars, 9},
	} {
		if c.got != c.want {
			t.Errorf("row.%s want %v got %v", c.field, c.want, c.got)
		}
		// Every one of them must also be reachable by the filter/sort layer.
		if v := fieldNum(&r, c.field); v != c.want {
			t.Errorf("fieldNum(%s) want %v got %v", c.field, c.want, v)
		}
	}

	// The two discrete fields ride the wire as ints, like flagState/flagBars.
	if v := fieldValue(&r, core.FieldIFlagState); v != 1 {
		t.Errorf("iflagState wire value want int(1), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldIFlagBars); v != 9 {
		t.Errorf("iflagBars wire value want int(9), got %v (%T)", v, v)
	}
	if v := fieldValue(&r, core.FieldIFlagScore); v != 76.0 {
		t.Errorf("iflagScore wire value want 76.0, got %v (%T)", v, v)
	}

	// No daily reference was loaded, so the DAILY set must read Null — the
	// intraday overlay never leaks into the daily fields.
	if !core.IsNull(r.FlagScore) || !core.IsNull(r.FlagState) {
		t.Errorf("no daily ref loaded: flagScore/flagState must stay Null, got %v/%v",
			r.FlagScore, r.FlagState)
	}
}

// A symbol with no intraday pattern data is UNKNOWN, not "measured and found
// wanting". All seven iflag fields must read Null, and the subtle half of that
// contract is iflagState: a zero there would be a real "no pattern" verdict,
// so an `iflagState == 0` scan would silently fill up with every ticker the
// worker has never classified.
func TestScannerIntradayFlagOverlayUnknownSymbolIsNull(t *testing.T) {
	s := New(nil)

	best := momScan()
	best.ID = "isetups"
	best.Filter = core.Filter{Field: core.FieldIFlagScore, Cmp: core.CmpGT, Value: 70.0}
	best.Columns = []string{"last", "iflagScore", "iflagState"}
	s.upsert(best)

	nopat := momScan()
	nopat.ID = "inopattern"
	nopat.Filter = core.Filter{Field: core.FieldIFlagState, Cmp: core.CmpEq, Value: 0.0}
	nopat.Columns = []string{"last", "iflagState"}
	s.upsert(nopat)

	s.SetIntradayFlags(map[string]core.FlagRef{"TIGHT": textbookIntradayFlag()})
	(<-s.ctrl)(s)

	base := time.Date(2026, 7, 31, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base, mkRow("TIGHT", 12.9, 1), mkRow("NEWIPO", 5, 1)))

	ticks := map[string]core.ScanTick{}
	for i := 0; i < 2; i++ {
		tk := readTick(t, s)
		ticks[tk.ID] = tk
	}
	if order := ticks["isetups"].Order; len(order) != 1 || order[0] != "TIGHT" {
		t.Fatalf("iflagScore > 70 want [TIGHT] (unknown NEWIPO must not pass), got %v", order)
	}
	if order := ticks["inopattern"].Order; len(order) != 0 {
		t.Fatalf("iflagState == 0 must match NOTHING — an unclassified symbol is unknown, "+
			"not a confirmed absence of a pattern — got %v", order)
	}

	row := s.last.Rows[s.last.Index["NEWIPO"]]
	for _, c := range []struct {
		field string
		got   float64
	}{
		{core.FieldIFlagScore, row.IFlagScore},
		{core.FieldIFlagState, row.IFlagState},
		{core.FieldIFlagPole, row.IFlagPole},
		{core.FieldIFlagRange, row.IFlagRange},
		{core.FieldIFlagRetrace, row.IFlagRetrace},
		{core.FieldIFlagVolDry, row.IFlagVolDry},
		{core.FieldIFlagBars, row.IFlagBars},
	} {
		if !core.IsNull(c.got) {
			t.Errorf("unknown symbol: %s want Null, got %v", c.field, c.got)
		}
		if v := fieldValue(&row, c.field); v != nil {
			t.Errorf("unknown symbol: %s wire value want nil, got %v", c.field, v)
		}
	}
}

// "Stored geometry, live breakout", intraday edition — with the decisive
// twist: the symbol carries BOTH references, whose BaseHighs differ (daily
// 12.5, intraday 13.2), and each state must promote ONLY against its own
// level. A price between the two levels is the discriminating input: it
// clears the daily base but not the intraday one (and, in the flipped
// geometry, vice versa).
func TestScannerIntradayFlagLiveBreakoutPromotion(t *testing.T) {
	daily := textbookFlag()            // State 1, BaseHigh 12.5
	intra := textbookIntradayFlag()    // State 1, BaseHigh 13.2
	intraLow := textbookIntradayFlag() // flipped geometry: intraday base BELOW the daily one
	intraLow.BaseHigh = 11.0
	intraNone := textbookIntradayFlag() // stored 0: no base, nothing to break out of
	intraNone.State = 0

	cases := []struct {
		name      string
		daily     core.FlagRef
		intra     core.FlagRef
		last      float64
		wantDaily float64
		wantIntra float64
	}{
		{name: "inside both bases stays coiling", daily: daily, intra: intra, last: 12.0, wantDaily: 1, wantIntra: 1},
		{name: "exactly at the daily base high promotes neither", daily: daily, intra: intra, last: 12.5, wantDaily: 1, wantIntra: 1},
		{name: "between the levels: daily breaks out, intraday must NOT", daily: daily, intra: intra, last: 12.8, wantDaily: 2, wantIntra: 1},
		{name: "exactly at the intraday base high stays coiling", daily: daily, intra: intra, last: 13.2, wantDaily: 2, wantIntra: 1},
		{name: "clears both levels: both break out", daily: daily, intra: intra, last: 13.3, wantDaily: 2, wantIntra: 2},
		{name: "flipped geometry: intraday breaks out below the daily level", daily: daily, intra: intraLow, last: 11.5, wantDaily: 1, wantIntra: 2},
		{name: "stored intraday 0 is never promoted", daily: daily, intra: intraNone, last: 99.0, wantDaily: 2, wantIntra: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil)
			s.SetDailyFlags(map[string]core.FlagRef{"X": tc.daily})
			(<-s.ctrl)(s)
			s.SetIntradayFlags(map[string]core.FlagRef{"X": tc.intra})
			(<-s.ctrl)(s)

			snap := snapOf(1000, mkRow("X", tc.last, 1))
			s.overlayFlags(snap)
			if got := snap.Rows[0].FlagState; got != tc.wantDaily {
				t.Fatalf("last %v, daily baseHigh %v: flagState want %v got %v",
					tc.last, tc.daily.BaseHigh, tc.wantDaily, got)
			}
			if got := snap.Rows[0].IFlagState; got != tc.wantIntra {
				t.Fatalf("last %v, intraday baseHigh %v: iflagState want %v got %v",
					tc.last, tc.intra.BaseHigh, tc.wantIntra, got)
			}
			// The promotion is live-only for both sets: neither stored reference
			// may be written back, or the next tick below the base high couldn't
			// undo it.
			if got := s.flagRef["X"].State; got != tc.daily.State {
				t.Fatalf("stored daily reference mutated: want %v got %v", tc.daily.State, got)
			}
			if got := s.iflagRef["X"].State; got != tc.intra.State {
				t.Fatalf("stored intraday reference mutated: want %v got %v", tc.intra.State, got)
			}
		})
	}
}

// An "iflagState cross above 2" alert is the live intraday-breakout
// notification. It must fire on the coiling->breakout transition and exactly
// once — and the DAILY base being cleared along the way must not trip it,
// which is why the symbol carries both references here.
func TestScannerIntradayFlagBreakoutAlertFiresOnce(t *testing.T) {
	s := New(nil)
	def := momScan()
	def.ID = "ibrk"
	def.Filter = core.Filter{} // match all, so membership doesn't gate the cross
	def.Columns = []string{"last", "iflagState", "iflagScore"}
	def.Alert = core.AlertConfig{
		Enabled: true, On: core.AlertOnCross, Sound: "ping",
		Trigger: &core.Filter{Field: core.FieldIFlagState, Cmp: core.CmpCrossAbove, Value: 2.0},
	}
	s.upsert(def)
	s.SetDailyFlags(map[string]core.FlagRef{"X": textbookFlag()}) // daily BaseHigh 12.5
	(<-s.ctrl)(s)
	s.SetIntradayFlags(map[string]core.FlagRef{"X": textbookIntradayFlag()}) // intraday BaseHigh 13.2
	(<-s.ctrl)(s)

	// Coiling under both levels: establishes prevVal, no cross on first sight.
	s.process(snapOf(1000, mkRow("X", 12.0, 1)))
	if gotAlert(s) {
		t.Fatal("no cross on first sight")
	}
	drain(s)

	// Price clears the DAILY base high only: flagState goes 2, but the
	// intraday base is intact — the iflagState alert must stay quiet.
	s.process(snapOf(1250, mkRow("X", 12.9, 1)))
	if gotAlert(s) {
		t.Fatal("clearing the DAILY base must not fire the INTRADAY breakout alert")
	}
	drain(s)

	// Price clears 13.2 -> iflagState 1 -> 2 -> the intraday breakout alert.
	s.process(snapOf(1500, mkRow("X", 13.3, 1)))
	a := readAlert(t, s)
	if a.Sym != "X" || a.Kind != "cross" || a.ScanID != "ibrk" {
		t.Fatalf("expected an intraday breakout cross alert for X, got %+v", a)
	}
	if a.Row["iflagState"] != 2 {
		t.Fatalf("the alert's row must carry the promoted state 2, got %v", a.Row["iflagState"])
	}
	drain(s)

	// Extended further above the base: still a breakout, but not a NEW one.
	s.process(snapOf(1750, mkRow("X", 14.0, 1)))
	if gotAlert(s) {
		t.Fatal("breakout alert must not repeat while price stays above the base high")
	}
	drain(s)
	s.process(snapOf(2000, mkRow("X", 15.0, 1)))
	if gotAlert(s) {
		t.Fatal("breakout alert must not repeat on a second tick above the base high")
	}
}

// The reason the intraday fields exist as SEVEN SEPARATE fields: a daily-flag
// scan and an intraday-flag scan over the same universe, same tick, must each
// see only their own geometry. One symbol carries both references (every value
// different), one carries only the daily one, one only the intraday one.
func TestScannerDailyAndIntradayFlagsCoexist(t *testing.T) {
	s := New(nil)

	dailyScan := momScan()
	dailyScan.ID = "dsetups"
	dailyScan.Filter = core.Filter{Field: core.FieldFlagScore, Cmp: core.CmpGT, Value: 80.0}
	dailyScan.Columns = []string{"last", "flagScore", "flagState"}
	s.upsert(dailyScan)

	intraScan := momScan()
	intraScan.ID = "isetups"
	intraScan.Filter = core.Filter{Field: core.FieldIFlagScore, Cmp: core.CmpGT, Value: 70.0}
	intraScan.Columns = []string{"last", "iflagScore", "iflagState"}
	s.upsert(intraScan)

	s.SetDailyFlags(map[string]core.FlagRef{
		"BOTH":      textbookFlag(), // Score 82.5, BaseHigh 12.5
		"DAILYONLY": textbookFlag(),
	})
	(<-s.ctrl)(s)
	s.SetIntradayFlags(map[string]core.FlagRef{
		"BOTH":      textbookIntradayFlag(), // Score 76.0, BaseHigh 13.2
		"INTRAONLY": textbookIntradayFlag(),
	})
	(<-s.ctrl)(s)

	// Last 12.0 sits inside BOTH bases, so neither state promotes.
	base := time.Date(2026, 7, 31, 15, 0, 0, 0, s.etLoc).UnixMilli()
	s.process(snapOf(base,
		mkRow("BOTH", 12.0, 1), mkRow("DAILYONLY", 12.0, 1), mkRow("INTRAONLY", 12.0, 1)))

	ticks := map[string]core.ScanTick{}
	for i := 0; i < 2; i++ {
		tk := readTick(t, s)
		ticks[tk.ID] = tk
	}
	inOrder := func(tk core.ScanTick) map[string]bool {
		m := map[string]bool{}
		for _, sym := range tk.Order {
			m[sym] = true
		}
		return m
	}
	d := inOrder(ticks["dsetups"])
	if !d["BOTH"] || !d["DAILYONLY"] || d["INTRAONLY"] {
		t.Fatalf("flagScore > 80 want {BOTH DAILYONLY}, got %v", ticks["dsetups"].Order)
	}
	i := inOrder(ticks["isetups"])
	if !i["BOTH"] || !i["INTRAONLY"] || i["DAILYONLY"] {
		t.Fatalf("iflagScore > 70 want {BOTH INTRAONLY}, got %v", ticks["isetups"].Order)
	}

	// The symbol carrying both references: all 14 fields, each from its own set.
	both := s.last.Rows[s.last.Index["BOTH"]]
	daily, intra := textbookFlag(), textbookIntradayFlag()
	for _, c := range []struct {
		field string
		got   float64
		want  float64
	}{
		{core.FieldFlagScore, both.FlagScore, daily.Score},
		{core.FieldFlagState, both.FlagState, daily.State},
		{core.FieldFlagPole, both.FlagPole, daily.PoleGain},
		{core.FieldFlagRange, both.FlagRange, daily.RangePct},
		{core.FieldFlagRetrace, both.FlagRetrace, daily.Retrace},
		{core.FieldFlagVolDry, both.FlagVolDry, daily.VolDry},
		{core.FieldFlagBars, both.FlagBars, float64(daily.Bars)},
		{core.FieldIFlagScore, both.IFlagScore, intra.Score},
		{core.FieldIFlagState, both.IFlagState, intra.State},
		{core.FieldIFlagPole, both.IFlagPole, intra.PoleGain},
		{core.FieldIFlagRange, both.IFlagRange, intra.RangePct},
		{core.FieldIFlagRetrace, both.IFlagRetrace, intra.Retrace},
		{core.FieldIFlagVolDry, both.IFlagVolDry, intra.VolDry},
		{core.FieldIFlagBars, both.IFlagBars, float64(intra.Bars)},
	} {
		if c.got != c.want {
			t.Errorf("BOTH row.%s want %v got %v (cross-talk between the overlays)", c.field, c.want, c.got)
		}
	}

	// Only a daily reference: every intraday field Null, and vice versa.
	donly := s.last.Rows[s.last.Index["DAILYONLY"]]
	if donly.FlagScore != daily.Score {
		t.Errorf("DAILYONLY flagScore want %v got %v", daily.Score, donly.FlagScore)
	}
	for _, c := range []struct {
		field string
		got   float64
	}{
		{core.FieldIFlagScore, donly.IFlagScore},
		{core.FieldIFlagState, donly.IFlagState},
		{core.FieldIFlagPole, donly.IFlagPole},
		{core.FieldIFlagRange, donly.IFlagRange},
		{core.FieldIFlagRetrace, donly.IFlagRetrace},
		{core.FieldIFlagVolDry, donly.IFlagVolDry},
		{core.FieldIFlagBars, donly.IFlagBars},
	} {
		if !core.IsNull(c.got) {
			t.Errorf("DAILYONLY: %s want Null, got %v", c.field, c.got)
		}
	}
	ionly := s.last.Rows[s.last.Index["INTRAONLY"]]
	if ionly.IFlagScore != intra.Score {
		t.Errorf("INTRAONLY iflagScore want %v got %v", intra.Score, ionly.IFlagScore)
	}
	for _, c := range []struct {
		field string
		got   float64
	}{
		{core.FieldFlagScore, ionly.FlagScore},
		{core.FieldFlagState, ionly.FlagState},
		{core.FieldFlagPole, ionly.FlagPole},
		{core.FieldFlagRange, ionly.FlagRange},
		{core.FieldFlagRetrace, ionly.FlagRetrace},
		{core.FieldFlagVolDry, ionly.FlagVolDry},
		{core.FieldFlagBars, ionly.FlagBars},
	} {
		if !core.IsNull(c.got) {
			t.Errorf("INTRAONLY: %s want Null, got %v", c.field, c.got)
		}
	}
}

// The Claude setup reader (internal/flagread) is gated on the DAILY flag
// fields only — an intraday-flag scan is a different, faster product and must
// not drag its entrants into paid daily setup reads.
func TestFilterUsesFlagsIgnoresIntradayFields(t *testing.T) {
	for _, field := range []string{
		core.FieldIFlagScore, core.FieldIFlagState, core.FieldIFlagPole,
		core.FieldIFlagRange, core.FieldIFlagRetrace, core.FieldIFlagVolDry,
		core.FieldIFlagBars,
	} {
		if isFlagField(field) {
			t.Errorf("%s is an INTRADAY flag field -- it must not gate the daily setup reader", field)
		}
		if filterUsesFlags(core.Filter{Field: field, Cmp: core.CmpGT, Value: 1.0}) {
			t.Errorf("a filter on %s must not count as a daily-flag scan", field)
		}
	}
}
