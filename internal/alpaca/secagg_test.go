package alpaca

import (
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// 2026-10-27 is a Tuesday; 08:00 ET = 12:00Z (EDT).
var preMkt = time.Date(2026, 10, 27, 8, 0, 0, 0, etLoc).UnixMilli()

func tr(sym string, ms int64, px float64, sz int64, conds ...string) Trade {
	return Trade{Sym: sym, Price: px, Size: sz, TS: ms, Conds: conds}
}

func TestSecAggBuildsOneSecond(t *testing.T) {
	a := NewSecAgg()
	base := preMkt
	if out := a.Add(tr("X", base+10, 10.00, 100, "@", "T")); len(out) != 0 {
		t.Fatalf("first trade emitted %v", out)
	}
	a.Add(tr("X", base+200, 10.20, 200, "@", "T"))
	a.Add(tr("X", base+300, 9.90, 50, "@", "T", "I")) // odd lot: volume only
	a.Add(tr("X", base+400, 9.95, 100, "@", "T"))
	out := a.Add(tr("X", base+1500, 10.10, 100, "@", "T")) // next second closes the first
	if len(out) != 1 {
		t.Fatalf("want 1 agg, got %d", len(out))
	}
	g := out[0]
	if g.Open != 10.00 || g.High != 10.20 || g.Low != 9.95 || g.Close != 9.95 {
		t.Fatalf("OHLC %v/%v/%v/%v (odd lot must not set the low)", g.Open, g.High, g.Low, g.Close)
	}
	if g.Vol != 450 {
		t.Fatalf("vol %d want 450 (odd lot counted)", g.Vol)
	}
	wantVW := (10.00*100 + 10.20*200 + 9.95*100) / 400
	if d := g.VWAP - wantVW; d > 1e-9 || d < -1e-9 {
		t.Fatalf("vwap %v want %v", g.VWAP, wantVW)
	}
	if g.StartMs != base || g.EndMs != base+1000 {
		t.Fatalf("window %d-%d", g.StartMs, g.EndMs)
	}
	if g.AccVol != 450 || g.DayOpen != 0 {
		t.Fatalf("accvol %d dayopen %v (pre-market: no official open yet)", g.AccVol, g.DayOpen)
	}
}

func TestSecAggFlushAndLatePrints(t *testing.T) {
	a := NewSecAgg()
	a.Add(tr("Y", preMkt+100, 5, 100))
	if out := a.FlushBefore(preMkt + 999); len(out) != 0 {
		t.Fatal("flushed an unfinished second")
	}
	out := a.FlushBefore(preMkt + 1000)
	if len(out) != 1 || out[0].Close != 5 {
		t.Fatalf("flush: %v", out)
	}
	// A late print for the already-emitted second folds forward, never back.
	a.Add(tr("Y", preMkt+500, 5.5, 100))
	out = a.FlushBefore(preMkt + 10_000)
	if len(out) != 1 || out[0].StartMs != preMkt+1000 || out[0].Close != 5.5 {
		t.Fatalf("late print: %+v", out)
	}
	if len(a.open) != 0 {
		t.Fatalf("open list not drained: %d", len(a.open))
	}
}

func TestSecAggVolumeOnlySeconds(t *testing.T) {
	a := NewSecAgg()
	// Odd lot with no known price: carried, not emitted.
	a.Add(tr("Z", preMkt, 3, 40, "I"))
	if out := a.FlushBefore(preMkt + 5000); len(out) != 0 {
		t.Fatalf("emitted a price-less agg: %v", out)
	}
	a.Add(tr("Z", preMkt+6000, 3.1, 100))
	out := a.FlushBefore(preMkt + 9000)
	if len(out) != 1 || out[0].Vol != 140 {
		t.Fatalf("carried volume lost: %+v", out)
	}
	// Now a known price exists: an odd-lot-only second emits at that price.
	a.Add(tr("Z", preMkt+9500, 3.5, 10, "I"))
	out = a.FlushBefore(preMkt + 20_000)
	if len(out) != 1 || out[0].Close != 3.1 || out[0].High != 3.1 || out[0].Vol != 10 {
		t.Fatalf("odd-lot second: %+v", out)
	}
}

func TestSecAggOfficialReportsAndDayOpen(t *testing.T) {
	a := NewSecAgg()
	open := time.Date(2026, 10, 27, 9, 30, 0, 0, etLoc).UnixMilli()
	a.Add(tr("O", open-5000, 7, 100, "T"))
	out := a.Add(tr("O", open+20, 7.5, 1000, "Q")) // official open report: no price, no volume
	a.Add(tr("O", open+40, 7.4, 300))
	out = append(out, a.FlushBefore(open+60_000)...)
	if len(out) != 2 {
		t.Fatalf("want 2 aggs, got %+v", out)
	}
	g := out[1]
	if g.Vol != 300 || g.Close != 7.4 || g.DayOpen != 7.4 || g.AccVol != 400 {
		t.Fatalf("after open: %+v", g)
	}
	wantDV := (7*100 + 7.4*300) / 400.0
	if d := g.DayVWAP - wantDV; d > 1e-9 || d < -1e-9 {
		t.Fatalf("day vwap %v want %v", g.DayVWAP, wantDV)
	}
	// Next ET day resets the day accumulators.
	a.Add(tr("O", open+24*3600*1000, 8, 100))
	out = a.FlushBefore(open + 25*3600*1000)
	if len(out) != 1 || out[0].AccVol != 100 || out[0].DayOpen != 8 {
		t.Fatalf("day roll: %+v", out)
	}
}

func TestSecAggSeedNeverLowersVolume(t *testing.T) {
	a := NewSecAgg()
	a.Add(tr("S", preMkt, 2, 500))
	a.Seed([]core.Seed{{Agg: core.Agg{Sym: "S", Close: 2, Vol: 100, DayVWAP: 2, EndMs: preMkt + 10}}})
	a.Seed([]core.Seed{{Agg: core.Agg{Sym: "T", Close: 4, Vol: 1000, DayVWAP: 3.9, DayOpen: 3.8, EndMs: preMkt + 10}}})
	out := a.FlushBefore(preMkt + 5000)
	if len(out) != 1 || out[0].AccVol != 500 {
		t.Fatalf("seed lowered volume: %+v", out)
	}
	a.Add(tr("T", preMkt+6000, 4.1, 100))
	out = a.FlushBefore(preMkt + 9000)
	if len(out) != 1 || out[0].AccVol != 1100 || out[0].DayOpen != 3.8 {
		t.Fatalf("seeded day state: %+v", out)
	}
}

func TestKeysNeverPrinted(t *testing.T) {
	k, err := parseKeys([]byte("\xef\xbb\xbf{\"keyId\":\"PKID123\",\"secret\":\"SECRETXYZ\"}"))
	if err != nil || !k.Valid() {
		t.Fatalf("parse: %v", err)
	}
	for _, s := range []string{k.String(), k.GoString()} {
		if strings.Contains(s, "PKID123") || strings.Contains(s, "SECRETXYZ") {
			t.Fatalf("key leaked in %q", s)
		}
	}
	if _, err := parseKeys([]byte(`{"keyId":"SECRETXYZ"`)); err == nil || strings.Contains(err.Error(), "SECRETXYZ") {
		t.Fatalf("bad json error leaks or nil: %v", err)
	}
	if _, err := parseKeys([]byte(`{"keyId":"a"}`)); err == nil {
		t.Fatal("missing secret accepted")
	}
}
