package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func mkRow(sym string, last, chg5m float64) core.Row {
	return core.Row{
		Sym: sym, Last: last, Chg5m: chg5m,
		Chg1m: core.Null, Chg15m: core.Null, ChgDay: core.Null, Gap: core.Null,
		Rvol: core.Null, VsVwap: core.Null, VsHod: core.Null, Rot: core.Null,
		Brk5mAge: core.Null, VsPrev5mLow: core.Null, Prev5mLow: core.Null,
		Brk5mCloseAge: core.Null, VsPrev5mClose: core.Null, Prev5mClose: core.Null,
		Brk5mCloseUpAge: core.Null,
		BrkPmhAge:       core.Null, VsPmh: core.Null, PrevAhHigh: core.Null, BrkPrevAhAge: core.Null, VsPrevAh: core.Null,
		BrkEma9Age: core.Null, VsEma9: core.Null, Ema9: core.Null,
		C1mChg: core.Null, C1mRvol: core.Null,
		BrkPmh5mAge: core.Null, BrkPrevAh5mAge: core.Null,
		NewsAge: -1, Sess: core.SessionRegular,
	}
}

func TestPredicateNumericNullExcluded(t *testing.T) {
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{1.0, 20.0}},
		{Field: core.FieldChg5m, Cmp: core.CmpGT, Value: 2.0},
		{Field: core.FieldRvol, Cmp: core.CmpGT, Value: 3.0},
	}}
	pred, err := compile(f)
	if err != nil {
		t.Fatal(err)
	}
	r1 := mkRow("A", 5, 6) // rvol null -> excluded
	if pred(&r1) {
		t.Fatal("null rvol must fail rvol>3")
	}
	r2 := mkRow("B", 5, 6)
	r2.Rvol = 4
	if !pred(&r2) {
		t.Fatal("B should match")
	}
	r3 := mkRow("C", 5, 1)
	r3.Rvol = 4 // chg5m 1 not > 2
	if pred(&r3) {
		t.Fatal("C should fail chg5m>2")
	}
	r4 := mkRow("D", 25, 6)
	r4.Rvol = 4 // last 25 out of [1,20]
	if pred(&r4) {
		t.Fatal("D should fail last between")
	}
}

func TestPredicateSessionNewsOrNesting(t *testing.T) {
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Op: "or", Conds: []core.Filter{
			{Field: core.FieldSession, Cmp: core.CmpEq, Value: "regular"},
			{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
		}},
		{Field: core.FieldNews, Cmp: core.CmpIsTrue},
	}}
	pred, err := compile(f)
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("A", 5, 6)
	r.Sess = core.SessionRegular
	r.News = true
	if !pred(&r) {
		t.Fatal("regular+news should match")
	}
	r.News = false
	if pred(&r) {
		t.Fatal("no news should fail isTrue")
	}
	r.News = true
	r.Sess = core.SessionPost
	if pred(&r) {
		t.Fatal("post should fail the session OR")
	}
}

func TestPredicateInAndEmpty(t *testing.T) {
	all, err := compile(core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("A", 5, 6)
	if !all(&r) {
		t.Fatal("empty filter must match all")
	}

	in, err := compile(core.Filter{Field: core.FieldSession, Cmp: core.CmpIn, Value: []any{"pre", "post"}})
	if err != nil {
		t.Fatal(err)
	}
	r.Sess = core.SessionPre
	if !in(&r) {
		t.Fatal("pre should be in [pre,post]")
	}
	r.Sess = core.SessionRegular
	if in(&r) {
		t.Fatal("regular should not be in [pre,post]")
	}
}

func TestPredicateSessionScopedFields(t *testing.T) {
	// "biggest pre-market movers": chgSess > 5 AND sessVol > 10000, gated to pre.
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldSession, Cmp: core.CmpEq, Value: "pre"},
		{Field: core.FieldChgSess, Cmp: core.CmpGT, Value: 5.0},
		{Field: core.FieldSessVol, Cmp: core.CmpGT, Value: 10000.0},
	}}
	pred, err := compile(f)
	if err != nil {
		t.Fatal(err)
	}

	r := mkRow("A", 5, 6)
	r.Sess = core.SessionPre
	r.ChgSess, r.SessVol = 8, 20000
	if !pred(&r) {
		t.Fatal("pre mover with chgSess=8, sessVol=20000 should match")
	}

	// Null chgSess (session open not yet known) must exclude, never match a >.
	r.ChgSess = core.Null
	if pred(&r) {
		t.Fatal("null chgSess must fail chgSess>5")
	}

	// Right metrics but wrong session -> excluded by the gate.
	r.ChgSess = 8
	r.Sess = core.SessionRegular
	if pred(&r) {
		t.Fatal("regular session should fail the pre gate")
	}

	// fieldValue wiring: session-scoped fields serialize, null -> nil.
	r.Sess, r.SessHigh, r.SessVwap = core.SessionPre, 12.5, core.Null
	if v := fieldValue(&r, core.FieldSessHigh); v != 12.5 {
		t.Fatalf("sessHigh fieldValue want 12.5 got %v", v)
	}
	if v := fieldValue(&r, core.FieldSessVwap); v != nil {
		t.Fatalf("null sessVwap fieldValue want nil got %v", v)
	}
	if v := fieldValue(&r, core.FieldSessVol); v != float64(20000) {
		t.Fatalf("sessVol fieldValue want 20000 got %v", v)
	}
}

// TestPredicateFrozenPreChg: "pre-market movers, reviewable all day" — a
// frozen preChg matches a >= filter, while Null (pre-market not completed
// today, or the ticker never traded in it) is excluded.
func TestPredicateFrozenPreChg(t *testing.T) {
	pred, err := compile(core.Filter{Field: core.FieldPreChg, Cmp: core.CmpGTE, Value: 5.0})
	if err != nil {
		t.Fatal(err)
	}

	r := mkRow("A", 5, 6)
	r.PreChg = 6 // frozen at the 09:30 boundary
	if !pred(&r) {
		t.Fatal("frozen preChg=6 should match preChg>=5")
	}
	r.PreChg = core.Null // pre-market hasn't completed (or no pre trades)
	if pred(&r) {
		t.Fatal("null preChg must fail preChg>=5")
	}

	// fieldValue wiring: frozen floats serialize with null -> nil; frozen
	// vols ride their int64 cases (0 = session not completed).
	r.PreChg, r.PreVwap, r.PreVol = 6.5, core.Null, int64(20000)
	if v := fieldValue(&r, core.FieldPreChg); v != 6.5 {
		t.Fatalf("preChg fieldValue want 6.5 got %v", v)
	}
	if v := fieldValue(&r, core.FieldPreVwap); v != nil {
		t.Fatalf("null preVwap fieldValue want nil got %v", v)
	}
	if v := fieldValue(&r, core.FieldPreVol); v != int64(20000) {
		t.Fatalf("preVol fieldValue want 20000 got %v", v)
	}
	if v := fieldValue(&r, core.FieldRthVol); v != int64(0) {
		t.Fatalf("rthVol fieldValue want 0 got %v", v)
	}
}

// TestPredicateFrozenPreFade: the gap-fade review scan's core filter — a
// frozen preFade matches a >= threshold, while Null (pre-market not completed
// today, or the ticker never traded in it) never matches.
func TestPredicateFrozenPreFade(t *testing.T) {
	pred, err := compile(core.Filter{Field: core.FieldPreFade, Cmp: core.CmpGTE, Value: 30.0})
	if err != nil {
		t.Fatal(err)
	}

	r := mkRow("A", 5, 6)
	r.PreFade = 45 // gapped and gave 45% of it back off the high
	if !pred(&r) {
		t.Fatal("frozen preFade=45 should match preFade>=30")
	}
	r.PreFade = 12 // held most of the move
	if pred(&r) {
		t.Fatal("preFade=12 must fail preFade>=30")
	}
	r.PreFade = core.Null // pre-market hasn't completed (or no pre trades)
	if pred(&r) {
		t.Fatal("null preFade must never match preFade>=30")
	}

	// fieldValue wiring: preGap/preFade are plain frozen floats riding the
	// documented default branch — number when set, nil when Null.
	r.PreGap, r.PreFade = 8.5, core.Null
	if v := fieldValue(&r, core.FieldPreGap); v != 8.5 {
		t.Fatalf("preGap fieldValue want 8.5 got %v", v)
	}
	if v := fieldValue(&r, core.FieldPreFade); v != nil {
		t.Fatalf("null preFade fieldValue want nil got %v", v)
	}
	r.RthGap, r.RthFade = core.Null, 33.3
	if v := fieldValue(&r, core.FieldRthGap); v != nil {
		t.Fatalf("null rthGap fieldValue want nil got %v", v)
	}
	if v := fieldValue(&r, core.FieldRthFade); v != 33.3 {
		t.Fatalf("rthFade fieldValue want 33.3 got %v", v)
	}
}

// TestPredicateGapFadeSeededShape compiles the seeded "PM Gap & Fade" scan's
// filter shape EXACTLY as cmd/scanner's gapFadeScans() encodes it — untyped
// Go ints inside []any for the between pair, ints for the thresholds — and
// proves the price band is inclusive on both ends: last 26 in, 24 out, the
// 250 boundary in. (After a persistence round-trip the values come back as
// json.Number/float64; toFloat accepts every one of those, this pins the
// pre-round-trip literal path the first boot's install uses.)
func TestPredicateGapFadeSeededShape(t *testing.T) {
	f := core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldLast, Cmp: core.CmpBetween, Value: []any{25, 250}},
		{Field: core.FieldPreGap, Cmp: core.CmpGTE, Value: 5},
		{Field: core.FieldPreFade, Cmp: core.CmpGTE, Value: 30},
		{Field: core.FieldPreVol, Cmp: core.CmpGTE, Value: 100000},
	}}
	pred, err := compile(f)
	if err != nil {
		t.Fatalf("seeded gap-fade filter shape must compile: %v", err)
	}

	mk := func(last float64) core.Row {
		r := mkRow("A", last, 1)
		r.PreGap, r.PreFade, r.PreVol = 8, 45, 200000
		return r
	}
	r := mk(26)
	if !pred(&r) {
		t.Fatal("last 26 with gap 8 / fade 45 must match the seeded scan")
	}
	r = mk(24)
	if pred(&r) {
		t.Fatal("last 24 is below the $25 floor and must not match")
	}
	r = mk(250)
	if !pred(&r) {
		t.Fatal("last 250 sits ON the band boundary and must match (between is inclusive)")
	}
	r = mk(26)
	r.PreFade = core.Null // pre-market not completed: must never match
	if pred(&r) {
		t.Fatal("null preFade must exclude the row from the seeded scan")
	}
	r = mk(26)
	r.PreGap = 3 // gapped, but under the 5% floor
	if pred(&r) {
		t.Fatal("preGap 3 must fail the >=5 gap floor")
	}
}

// TestSortByPreFadeDescNullsLast: sorting the gap-fade review scan by preFade
// desc puts the deepest faders first and not-yet-frozen rows last.
func TestSortByPreFadeDescNullsLast(t *testing.T) {
	a, b, c := mkRow("A", 1, 1), mkRow("B", 1, 1), mkRow("C", 1, 1)
	a.PreFade = 60
	b.PreFade = 20
	c.PreFade = core.Null
	rows := []core.Row{c, b, a}

	sortRows(rows, []core.SortKey{{Field: core.FieldPreFade, Dir: "desc"}})
	if rows[0].Sym != "A" || rows[1].Sym != "B" || rows[2].Sym != "C" {
		t.Fatalf("preFade desc want [A B C] got [%s %s %s]", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}
}

// TestSortByPreChgDescNullsLast: sorting a review scan by preChg desc puts the
// biggest frozen movers first and never-traded/not-yet-frozen rows last.
func TestSortByPreChgDescNullsLast(t *testing.T) {
	a, b, c := mkRow("A", 1, 1), mkRow("B", 1, 1), mkRow("C", 1, 1)
	a.PreChg = 6
	b.PreChg = 2
	c.PreChg = core.Null
	rows := []core.Row{c, b, a}

	sortRows(rows, []core.SortKey{{Field: core.FieldPreChg, Dir: "desc"}})
	if rows[0].Sym != "A" || rows[1].Sym != "B" || rows[2].Sym != "C" {
		t.Fatalf("preChg desc want [A B C] got [%s %s %s]", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}
}

func TestPredicateSecTypeEqAndIn(t *testing.T) {
	r := mkRow("A", 5, 6)
	r.SecType = "CS"

	eq, err := compile(core.Filter{Field: core.FieldSecType, Cmp: core.CmpEq, Value: "CS"})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(&r) {
		t.Fatal("CS should match secType == CS")
	}
	r.SecType = "ETF"
	if eq(&r) {
		t.Fatal("ETF should not match secType == CS")
	}

	in, err := compile(core.Filter{Field: core.FieldSecType, Cmp: core.CmpIn, Value: []any{"CS", "ADRC"}})
	if err != nil {
		t.Fatal(err)
	}
	r.SecType = "CS"
	if !in(&r) {
		t.Fatal("CS should be in [CS,ADRC]")
	}
	r.SecType = "ADRC"
	if !in(&r) {
		t.Fatal("ADRC should be in [CS,ADRC]")
	}
	r.SecType = "ETF"
	if in(&r) {
		t.Fatal("ETF should not be in [CS,ADRC]")
	}
}

func TestSortBySymbol(t *testing.T) {
	rows := []core.Row{mkRow("C", 1, 1), mkRow("A", 1, 1), mkRow("B", 1, 1)}
	sortRows(rows, []core.SortKey{{Field: "sym", Dir: "asc"}})
	if rows[0].Sym != "A" || rows[1].Sym != "B" || rows[2].Sym != "C" {
		t.Fatalf("asc sym sort wrong: %v %v %v", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}
	sortRows(rows, []core.SortKey{{Field: "sym", Dir: "desc"}})
	if rows[0].Sym != "C" || rows[1].Sym != "B" || rows[2].Sym != "A" {
		t.Fatalf("desc sym sort wrong: %v %v %v", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}
}

// Boolean columns must be sortable — clicking the NEWS header used to be a
// no-op because fieldNum returns null for bools.
func TestSortByBooleanColumn(t *testing.T) {
	a, b, c := mkRow("A", 1, 1), mkRow("B", 1, 1), mkRow("C", 1, 1)
	b.News = true
	rows := []core.Row{a, b, c}

	sortRows(rows, []core.SortKey{{Field: core.FieldNews, Dir: "desc"}})
	if rows[0].Sym != "B" {
		t.Fatalf("desc news sort should put the news row first, got %s", rows[0].Sym)
	}
	sortRows(rows, []core.SortKey{{Field: core.FieldNews, Dir: "asc"}})
	if rows[2].Sym != "B" {
		t.Fatalf("asc news sort should put the news row last, got %s", rows[2].Sym)
	}
}

// Primary/secondary sort: the second key only breaks ties on the first.
func TestSortRowsSecondaryKey(t *testing.T) {
	// last is the tiebreak; chg5m is primary. A and C tie on chg5m=5.
	a, b, c := mkRow("A", 10, 5), mkRow("B", 99, 9), mkRow("C", 30, 5)
	rows := []core.Row{a, b, c}

	sortRows(rows, []core.SortKey{
		{Field: core.FieldChg5m, Dir: "desc"},
		{Field: core.FieldLast, Dir: "desc"},
	})
	// B(9) first; then the chg5m=5 pair ordered by last desc: C(30) before A(10).
	if got := []string{rows[0].Sym, rows[1].Sym, rows[2].Sym}; got[0] != "B" || got[1] != "C" || got[2] != "A" {
		t.Fatalf("desc secondary: want [B C A] got %v", got)
	}

	// Flipping only the secondary direction must reorder just the tied pair.
	sortRows(rows, []core.SortKey{
		{Field: core.FieldChg5m, Dir: "desc"},
		{Field: core.FieldLast, Dir: "asc"},
	})
	if got := []string{rows[0].Sym, rows[1].Sym, rows[2].Sym}; got[0] != "B" || got[1] != "A" || got[2] != "C" {
		t.Fatalf("asc secondary: want [B A C] got %v", got)
	}
}

// A null in the primary key sorts last regardless of its secondary value.
func TestSortRowsSecondaryDoesNotRescueNullPrimary(t *testing.T) {
	a := mkRow("A", 1, 3)
	n := mkRow("N", 999, 0)
	n.Chg5m = core.Null
	rows := []core.Row{n, a}
	sortRows(rows, []core.SortKey{
		{Field: core.FieldChg5m, Dir: "desc"},
		{Field: core.FieldLast, Dir: "desc"},
	})
	if rows[0].Sym != "A" || rows[1].Sym != "N" {
		t.Fatalf("null primary must stay last: got %s %s", rows[0].Sym, rows[1].Sym)
	}
}

func TestSortRowsDescNullsLast(t *testing.T) {
	rows := []core.Row{mkRow("A", 1, 3), mkRow("B", 1, 7), mkRow("C", 1, 5)}
	n := mkRow("D", 1, 0)
	n.Chg5m = core.Null
	rows = append(rows, n)

	sortRows(rows, []core.SortKey{{Field: core.FieldChg5m, Dir: "desc"}})
	want := []string{"B", "C", "A", "D"} // 7, 5, 3, null-last
	for i, w := range want {
		if rows[i].Sym != w {
			t.Fatalf("pos %d want %s got %s (%v)", i, w, rows[i].Sym, rows[i].Chg5m)
		}
	}
}

// The 5-min break is exposed as a number so the existing editor can filter it:
// "brk5mAge < 60" = broke within the last minute; Null (no break) never matches.
func TestPredicateBrk5mAge(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldBrk5mAge, Cmp: core.CmpLT, Value: 60.0},
		{Field: core.FieldVsPrev5mLow, Cmp: core.CmpLT, Value: 0.0},
	}})
	if err != nil {
		t.Fatal(err)
	}

	fresh := mkRow("FRESH", 9.76, 0)
	fresh.Brk5mAge, fresh.VsPrev5mLow, fresh.Prev5mLow = 3, -0.4, 9.80
	if !pred(&fresh) {
		t.Fatal("a 3s-old break still below the level must match")
	}

	none := mkRow("NONE", 10, 0)
	none.VsPrev5mLow, none.Prev5mLow = 2, 9.80 // level known, never broken
	if pred(&none) {
		t.Fatal("no break (Null age) must never match brk5mAge < 60")
	}

	stale := mkRow("STALE", 9.70, 0)
	stale.Brk5mAge, stale.VsPrev5mLow = 90, -1
	if pred(&stale) {
		t.Fatal("a 90s-old break must drop out of a < 60 filter")
	}

	bounced := mkRow("BOUNCE", 9.85, 0)
	bounced.Brk5mAge, bounced.VsPrev5mLow = 10, 0.5
	if pred(&bounced) {
		t.Fatal("broke but reclaimed the level must fail vsPrev5mLow < 0")
	}

	// Freshest first.
	rows := []core.Row{stale, fresh, none}
	sortRows(rows, []core.SortKey{{Field: core.FieldBrk5mAge, Dir: "asc"}})
	if rows[0].Sym != "FRESH" || rows[1].Sym != "STALE" || rows[2].Sym != "NONE" {
		t.Fatalf("asc by age want FRESH, STALE, NONE(null last), got %s %s %s", rows[0].Sym, rows[1].Sym, rows[2].Sym)
	}

	// Wire: Null serializes as JSON null (an empty cell), not 0 ("broke 0s ago").
	if v := fieldValue(&none, core.FieldBrk5mAge); v != nil {
		t.Fatalf("no break must serialize as null, got %v", v)
	}
	if v := fieldValue(&fresh, core.FieldBrk5mAge); v != float64(3) {
		t.Fatalf("brk5mAge want 3 got %v", v)
	}
}

func TestPredicateBrk5mCloseAge(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldBrk5mCloseAge, Cmp: core.CmpLT, Value: 60.0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	broke := mkRow("BRK", 9.86, 0)
	broke.Brk5mCloseAge, broke.VsPrev5mClose, broke.Prev5mClose = 5, -0.4, 9.90
	if !pred(&broke) {
		t.Fatal("a 5s-old close break must match")
	}
	lowOnly := mkRow("LOWONLY", 9.70, 0)
	lowOnly.Brk5mAge = 5 // broke the LOW variant only: must not leak into the close filter
	if pred(&lowOnly) {
		t.Fatal("the close filter must read brk5mCloseAge, not brk5mAge")
	}
	if v := fieldValue(&lowOnly, core.FieldBrk5mCloseAge); v != nil {
		t.Fatalf("no close break must serialize as null, got %v", v)
	}
	if v := fieldValue(&broke, core.FieldPrev5mClose); v != 9.90 {
		t.Fatalf("prev5mClose want 9.90 got %v", v)
	}
}

func TestPredicateBrk5mCloseUpAge(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldBrk5mCloseUpAge, Cmp: core.CmpLT, Value: 60.0},
		{Field: core.FieldVsPrev5mClose, Cmp: core.CmpGT, Value: 0.5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	up := mkRow("UP", 10.10, 0)
	up.Brk5mCloseUpAge, up.VsPrev5mClose = 4, 1.0
	if !pred(&up) {
		t.Fatal("a fresh long break 1% over the close must match")
	}
	shortOnly := mkRow("DOWN", 9.90, 0)
	shortOnly.Brk5mCloseAge, shortOnly.VsPrev5mClose = 4, -1.0
	if pred(&shortOnly) {
		t.Fatal("a SHORT-side break must not leak into the long filter")
	}
	if v := fieldValue(&shortOnly, core.FieldBrk5mCloseUpAge); v != nil {
		t.Fatalf("no long break must serialize as null, got %v", v)
	}
}

func TestPredicateLevelBreaks(t *testing.T) {
	pmh, err := compile(core.Filter{Op: "and", Conds: []core.Filter{{Field: core.FieldBrkPmhAge, Cmp: core.CmpLT, Value: 60.0}}})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := compile(core.Filter{Op: "and", Conds: []core.Filter{{Field: core.FieldBrkPrevAhAge, Cmp: core.CmpLT, Value: 60.0}}})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("LVL", 10.05, 0)
	r.BrkPmhAge, r.VsPmh = 3, 0.5
	if !pmh(&r) || ah(&r) {
		t.Fatal("a PMH break must match the PMH filter only")
	}
	r.BrkPmhAge, r.BrkPrevAhAge, r.PrevAhHigh = core.Null, 7, 10.00
	if pmh(&r) || !ah(&r) {
		t.Fatal("a prev-AH break must match the AH filter only")
	}
	if v := fieldValue(&r, core.FieldBrkPmhAge); v != nil {
		t.Fatalf("no PMH break must serialize as null, got %v", v)
	}
	if v := fieldValue(&r, core.FieldPrevAhHigh); v != 10.00 {
		t.Fatalf("prevAhHigh want 10 got %v", v)
	}
}

func TestPredicateEma9(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldBrkEma9Age, Cmp: core.CmpLT, Value: 60.0},
		{Field: core.FieldVsEma9, Cmp: core.CmpLT, Value: 0.0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("EMA", 9.9, 0)
	r.BrkEma9Age, r.VsEma9, r.Ema9 = 12, -0.4, 9.94
	if !pred(&r) {
		t.Fatal("a fresh 9 EMA loss still under the EMA must match")
	}
	r.VsEma9 = 0.2 // reclaimed it
	if pred(&r) {
		t.Fatal("back above the EMA must fail vsEma9 < 0")
	}
	r.BrkEma9Age = core.Null
	if v := fieldValue(&r, core.FieldBrkEma9Age); v != nil {
		t.Fatalf("no loss must serialize as null, got %v", v)
	}
}

func TestPredicateLevel5m(t *testing.T) {
	pmh5, err := compile(core.Filter{Field: core.FieldBrkPmh5mAge, Cmp: core.CmpLT, Value: 300.0})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("L5", 10.1, 0)
	r.BrkPmhAge = 4 // a wick broke it at trade level only
	if pmh5(&r) {
		t.Fatal("a trade-level break alone must not match the 5-minute filter")
	}
	r.BrkPmh5mAge = 120
	if !pmh5(&r) {
		t.Fatal("a 5-minute close break 2 minutes ago must match < 300")
	}
	if v := fieldValue(&r, core.FieldBrkPrevAh5mAge); v != nil {
		t.Fatalf("no 5m AH break must serialize as null, got %v", v)
	}
}

func TestPredicateC1mFields(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{
		{Field: core.FieldC1mChg, Cmp: core.CmpGTE, Value: 5.0},
		{Field: core.FieldC1mRvol, Cmp: core.CmpGTE, Value: 3.0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := mkRow("A", 10, 0)
	if pred(&r) {
		t.Fatal("Null c1m fields must not match")
	}
	r.C1mChg, r.C1mRvol = 6.1, 3.4
	if !pred(&r) {
		t.Fatal("6.1% on 3.4x must match")
	}
	r.C1mRvol = 2.9
	if pred(&r) {
		t.Fatal("2.9x must fail rvol >= 3")
	}
	if v := fieldValue(&r, core.FieldC1mChg); v != 6.1 {
		t.Fatalf("wire value %v", v)
	}
	r.C1mChg = core.Null
	if v := fieldValue(&r, core.FieldC1mChg); v != nil {
		t.Fatalf("Null must go on the wire as nil, got %v", v)
	}
}
