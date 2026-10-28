package scan

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

var epET, _ = time.LoadLocation("America/New_York")

// ms of 2026-10-20 (a Tuesday) at h:m:s ET.
func epAt(h, m, s int) int64 {
	return time.Date(2026, 10, 20, h, m, s, 0, epET).UnixMilli()
}

func epRow(sym string, sess core.Session, last, chgDay, gap, sessHigh float64) core.Row {
	r := mkRow(sym, last, 0)
	r.Sess, r.ChgDay, r.Gap, r.SessHigh = sess, chgDay, gap, sessHigh
	r.Vol, r.AvgVol, r.Rvol = 2_000_000, 1_000_000, core.Null
	return r
}

func epScanner() *Scanner {
	s := New(nil)
	// last night's earnings headline (Monday 18:00 ET) and the daily context.
	s.markEPNews([]core.NewsItem{
		{Tickers: []string{"EPS"}, Headline: "Acme Reports Q3 Results, Raises Guidance", TS: epAt(18, 0, 0) - 24*3600*1000},
		{Tickers: []string{"NOE"}, Headline: "Why NOE Shares Are Trading Higher", TS: epAt(18, 0, 0) - 24*3600*1000},
		{Tickers: []string{"OLD"}, Headline: "OLD Reports Q2 Results", TS: epAt(15, 0, 0) - 24*3600*1000}, // before Monday 16:00
	})
	for _, k := range []string{"EPS", "NOE", "OLD"} {
		s.ep.ref[k] = core.EPRef{Chg3m: 3, AdrPct: 5, AdrDays: 20}
	}
	return s
}

func epOverlay(s *Scanner, asOf int64, sess core.Session, rows ...core.Row) []core.Row {
	snap := snapOf(asOf, rows...)
	snap.Sess = sess
	s.overlayEP(snap)
	return snap.Rows
}

func TestEPWatchPremarket(t *testing.T) {
	s := epScanner()
	rows := epOverlay(s, epAt(8, 0, 0), core.SessionPre,
		epRow("EPS", core.SessionPre, 20, 12, core.Null, 0),
		epRow("NOE", core.SessionPre, 20, 12, core.Null, 0), // no earnings words
		epRow("OLD", core.SessionPre, 20, 12, core.Null, 0), // headline predates the previous 16:00 ET
		epRow("SML", core.SessionPre, 20, 8, core.Null, 0),  // gap below 10%
	)
	if rows[0].EpPass != 1 || rows[0].EpEarnHL == "" || rows[0].EpGap != 12 || rows[0].EpVolX != 2 {
		t.Fatalf("EPS: %+v", rows[0])
	}
	if want := 20 * 0.95; rows[0].EpStop != want {
		t.Fatalf("stop %v want %v", rows[0].EpStop, want)
	}
	for _, i := range []int{1, 2, 3} {
		if !core.IsNull(rows[i].EpPass) {
			t.Errorf("%s must not pass", rows[i].Sym)
		}
	}
	if !s.ep.qual["EPS"] || s.ep.qual["NOE"] {
		t.Fatalf("latch: %v", s.ep.qual)
	}
}

func TestEPNeglectNeedsRefAndLimit(t *testing.T) {
	s := epScanner()
	s.ep.ref["EPS"] = core.EPRef{Chg3m: 25, AdrPct: 5, AdrDays: 20} // rallied: not neglected
	rows := epOverlay(s, epAt(8, 0, 0), core.SessionPre, epRow("EPS", core.SessionPre, 20, 12, core.Null, 0))
	if !core.IsNull(rows[0].EpPass) || rows[0].Ep3m != 25 {
		t.Fatalf("%+v", rows[0])
	}
	delete(s.ep.ref, "EPS") // no context fetched yet: cannot verify, so no pass
	rows = epOverlay(s, epAt(8, 0, 5), core.SessionPre, epRow("EPS", core.SessionPre, 20, 12, core.Null, 0))
	if !core.IsNull(rows[0].EpPass) {
		t.Fatal("unknown 3m change must not pass")
	}
}

func TestEPBreakOncePerDay(t *testing.T) {
	s := epScanner()
	s.markEPNews(nil)
	epOverlay(s, epAt(8, 0, 0), core.SessionPre, epRow("EPS", core.SessionPre, 20, 12, core.Null, 0)) // latch
	// Opening range: regular-session high 21.5 by 09:34:59.
	epOverlay(s, epAt(9, 31, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 21, 12, 12, 21.2))
	epOverlay(s, epAt(9, 34, 59), core.SessionRegular, epRow("EPS", core.SessionRegular, 21, 12, 12, 21.5))
	// 09:35:30 at 21.5: not above the ORH yet.
	r := epOverlay(s, epAt(9, 35, 30), core.SessionRegular, epRow("EPS", core.SessionRegular, 21.5, 12, 12, 21.6))[0]
	if r.EpOrh != 21.5 || !core.IsNull(r.EpBrkAge) {
		t.Fatalf("no break yet: %+v", r)
	}
	// 09:40:00 trades above the ORH: the break starts.
	r = epOverlay(s, epAt(9, 40, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 21.9, 12, 12, 21.9))[0]
	if r.EpBrkAge != 0 || r.EpOrh != 21.5 {
		t.Fatalf("break: %+v", r)
	}
	// Later: same break, age grows, ORH does not move; dipping and re-breaking does not restart it.
	epOverlay(s, epAt(9, 45, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 21.0, 12, 12, 22.5))
	r = epOverlay(s, epAt(9, 50, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 22.4, 12, 12, 22.5))[0]
	if r.EpBrkAge != 600 || r.EpOrh != 21.5 {
		t.Fatalf("once per day: %+v", r)
	}
	// Past 11:00 the EP rows are gone.
	r = epOverlay(s, epAt(11, 0, 1), core.SessionRegular, epRow("EPS", core.SessionRegular, 23, 12, 12, 23))[0]
	if !core.IsNull(r.EpBrkAge) || !core.IsNull(r.EpOrh) || !core.IsNull(r.EpPass) {
		t.Fatalf("after window: %+v", r)
	}
}

func TestEPNoORHWhenScannerStartedLate(t *testing.T) {
	s := epScanner()
	epOverlay(s, epAt(8, 0, 0), core.SessionPre, epRow("EPS", core.SessionPre, 20, 12, core.Null, 0))
	r := epOverlay(s, epAt(9, 40, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 25, 12, 12, 25))[0]
	if !core.IsNull(r.EpOrh) || !core.IsNull(r.EpBrkAge) {
		t.Fatalf("no ORH was seen, no break: %+v", r)
	}
}

func TestEPQualifiesAtTheOpen(t *testing.T) {
	s := epScanner() // never seen in pre-market
	epOverlay(s, epAt(9, 31, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 21, 12, 12, 21.2))
	epOverlay(s, epAt(9, 34, 30), core.SessionRegular, epRow("EPS", core.SessionRegular, 21, 12, 12, 21.3))
	r := epOverlay(s, epAt(9, 36, 0), core.SessionRegular, epRow("EPS", core.SessionRegular, 21.4, 12, 12, 21.4))[0]
	if r.EpBrkAge != 0 || r.EpPass != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestEPBreakFilterEnterOnce(t *testing.T) {
	pred, err := compile(core.Filter{Op: "and", Conds: []core.Filter{{Field: core.FieldEpBrkAge, Cmp: core.CmpGTE, Value: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := mkRow("A", 10, 0), mkRow("B", 10, 0)
	a.EpBrkAge, b.EpBrkAge = 30, core.Null
	if !pred(&a) || pred(&b) {
		t.Fatal("epBrkAge >= 0 must match only broken rows")
	}
}
