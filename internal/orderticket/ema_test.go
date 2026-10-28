package orderticket

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// The IPW case (2026-10-19 ~04:15 ET): prev day flat ~0.39, today 04:01-04:14
// ran 0.46 -> 0.75 -> 0.65. The REST seed (IntradayWithPrevDay) lagged the
// newest minutes and live aggregates were dropped until the seed landed, so
// the book held prev day + a few early bars and the 9EMA read ~0.49 while the
// true one was ~0.6. This reproduces it and checks the fix against a
// reference EMA over the true minute series.
func ipwSeries(now time.Time) (prev, today []core.Bar) {
	et := now.In(etLoc())
	y, m, d := et.Date()
	open := time.Date(y, m, d, 4, 1, 0, 0, etLoc()).UnixMilli()
	prevStart := time.Date(y, m, d-1, 4, 0, 0, 0, etLoc()).UnixMilli()
	for i := 0; i < 324; i++ {
		c := 0.39 + 0.01*float64(i%2)
		prev = append(prev, core.Bar{T: prevStart + int64(i)*3*60000, O: c, H: c, L: c, C: c, V: 1000})
	}
	closes := []float64{0.46, 0.52, 0.58, 0.63, 0.69, 0.75, 0.72, 0.70, 0.68, 0.66, 0.67, 0.64, 0.66, 0.65}
	for i, c := range closes {
		today = append(today, core.Bar{T: open + int64(i)*60000, O: c, H: c, L: c, C: c, V: 1_000_000})
	}
	return prev, today
}

func refEMA(bars []core.Bar) float64 {
	cl := make([]float64, len(bars))
	for i, b := range bars {
		cl[i] = b.C
	}
	e := emaSeries(cl, EMAPeriod)
	return e[len(e)-1]
}

func TestIPWLaggingSeedRepro(t *testing.T) {
	r := newRig(t, nil)
	r.now = time.Date(2026, 10, 19, 8, 15, 21, 0, time.UTC) // 04:15:21 ET
	r.tk.settings.EMALive = false                           // the reference is over closed candles
	prev, today := ipwSeries(r.now)
	full := append(append([]core.Bar(nil), prev...), today...)
	want := refEMA(full)
	if want < 0.6 || want > 0.7 {
		t.Fatalf("reference EMA %v", want)
	}
	lagging := append(append([]core.Bar(nil), prev...), today[:3]...) // REST: only 04:01-04:03 so far
	seeds := 0
	r.tk.cfg.Seed = func(context.Context, string) ([]core.Bar, error) {
		seeds++ // the REST seed has caught up by the time Prepare re-seeds
		return full, nil
	}
	r.connect()
	r.show("111")                  // switch: tracked -> the book keeps live aggs from now on
	r.tk.SeedBars("SGRX", lagging) // the first (lagging) REST seed
	for _, b := range today[13:] { // live tap since the switch: 04:14 (+ forming 04:15)
		r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: b.T, Open: b.C, High: b.C, Low: b.C, Close: b.C, Vol: 10})
	}
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: today[13].T + 60000, Open: 0.6521, High: 0.6521, Low: 0.6521, Close: 0.6521, Vol: 10})

	// the bug: the book's EMA over prev day + 3 early bars + the live 04:14 bar
	r.tk.mu.Lock()
	bad := r.tk.quoteLocked("SGRX", r.now)
	r.tk.mu.Unlock()
	if math.Abs(bad.EMA-want) < 0.05 {
		t.Fatalf("repro did not reproduce: book %v ref %v", bad.EMA, want)
	}
	if s := r.tk.State(); s.EMAOK || s.EMANote == "" {
		t.Fatalf("gappy book usable: %+v", s)
	}
	// prepare re-seeds synchronously and computes from the complete series
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if seeds != 1 || math.Abs(p.EMA-want) > 1e-9 {
		t.Fatalf("EMA %v want %v (seeds %d)", p.EMA, want, seeds)
	}
	if s := r.tk.State(); !s.EMAOK || s.EMANote != "" {
		t.Fatalf("%+v", s)
	}
}

func TestPrepareRefusedWhileBarsMissing(t *testing.T) {
	r := newRig(t, nil)
	r.now = time.Date(2026, 10, 19, 8, 15, 21, 0, time.UTC)
	prev, today := ipwSeries(r.now)
	lagging := append(append([]core.Bar(nil), prev...), today[:3]...)
	r.tk.cfg.Seed = func(context.Context, string) ([]core.Bar, error) { return lagging, nil } // REST keeps lagging
	r.connect()
	r.show("111")
	if s := r.tk.State(); s.EMAOK || !strings.Contains(s.EMANote, "loading") {
		t.Fatalf("before the seed: %+v", s)
	}
	r.tk.SeedBars("SGRX", lagging)
	r.tk.FeedAgg(core.Agg{Sym: "SGRX", StartMs: today[12].T, Close: 0.66, Open: 0.66, High: 0.66, Low: 0.66, Vol: 1})
	if _, err := r.tk.Prepare("BUY", 1000, "SGRX"); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("gappy book accepted: %v", err)
	}
	if len(r.f.whatIfsCopy()) != 0 {
		t.Fatal("what-if sent on a gappy book")
	}
	// 5+ min after that live minute, a re-seed that still lacks the minutes
	// means they had no trades: usable
	r.advance(6 * time.Minute)
	r.tk.SeedBars("SGRX", lagging)
	if ok, _ := r.tk.book.ready("SGRX"); !ok {
		t.Fatal("never ready")
	}
}

func TestConfirmRebuildsWhenEMAMoved(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	r.advance(25 * time.Second) // 14:30:55
	p, err := r.tk.Prepare("BUY", 600, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	price(r, 1.70)              // the 14:30 bar closes at 1.70 ...
	r.advance(10 * time.Second) // ... at 14:31:00: EMA 1.50 -> 1.54, inside the 30 s confirm window
	err = r.tk.Confirm(p.Token, "SGRX")
	if err == nil || !strings.Contains(err.Error(), "press Confirm again") {
		t.Fatalf("stale price confirmed: %v", err)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("sent at the old price")
	}
	np := r.tk.State().Pending
	if np == nil || np.Token == p.Token || !near(np.Limit, 1.54) || !strings.Contains(np.Text, "@ 1.54") {
		t.Fatalf("rebuilt %+v", np)
	}
	if err := r.tk.Confirm(np.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	if ps := r.f.placesCopy(); len(ps) != 2 || !near(ps[0].o.LmtPrice, 1.54) {
		t.Fatalf("%+v", ps)
	}
}

func TestLiveCandleEMAMode(t *testing.T) {
	r := newRig(t, nil)
	r.tk.settings.EMALive = false
	r.ready("SGRX", "111", 1.50)
	price(r, 2.00) // forming bar at 2.00
	if q := r.tk.State().Quote; !near(q.EMA, 1.5) || q.Live {
		t.Fatalf("closed mode %+v", q)
	}
	s := r.tk.settings
	s.EMALive = true
	if err := r.tk.SetSettings(context.Background(), s); err == nil {
		t.Fatal("settings change while connected accepted") // connection settings are locked while connected
	}
	r.tk.mu.Lock()
	r.tk.settings.EMALive = true
	r.tk.mu.Unlock()
	q := r.tk.State().Quote
	if !q.Live || math.Abs(q.EMA-(1.5*0.8+2.0*0.2)) > 1e-9 { // one more EMA step with k = 0.2
		t.Fatalf("live mode %+v", q)
	}
	if err := r.tk.SetEMALive(context.Background(), false); err != nil || r.tk.State().Quote.Live {
		t.Fatalf("toggle: %v", err)
	}
}

func TestMarketableWarning(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	price(r, 1.45) // last below the EMA: a BUY at 1.50 fills now
	p, _ := r.tk.Prepare("BUY", 600, "SGRX")
	if p.Warn != "price is below the 9EMA — fills at market now" {
		t.Fatalf("%q", p.Warn)
	}
	p, _ = r.tk.Prepare("SELL", 600, "SGRX")
	if p.Warn != "" {
		t.Fatalf("short above last warned: %q", p.Warn)
	}
	price(r, 1.55)
	if p, _ = r.tk.Prepare("SELL", 600, "SGRX"); p.Warn != "price is above the 9EMA — fills at market now" {
		t.Fatalf("%q", p.Warn)
	}
}
