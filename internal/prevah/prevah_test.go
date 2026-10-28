package prevah

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

var ny, _ = time.LoadLocation("America/New_York")

// bar at an ET wall time on a date.
func bar(date string, hh, mm int, h float64) core.Bar {
	d, _ := time.ParseInLocation("2006-01-02", date, ny)
	t := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, ny)
	return core.Bar{T: t.UnixMilli(), O: h, H: h, L: h, C: h, V: 100}
}

func TestPostHighWindowAndDate(t *testing.T) {
	bars := []core.Bar{
		bar("2026-10-02", 15, 59, 99), // regular session: excluded
		bar("2026-10-02", 16, 0, 5.10),
		bar("2026-10-02", 18, 30, 5.40), // the after-hours high
		bar("2026-10-02", 19, 59, 5.20),
		bar("2026-10-02", 20, 0, 88),   // after 20:00: excluded
		bar("2026-10-05", 17, 0, 77),   // a DIFFERENT date's after-hours: excluded
		bar("2026-10-01", 18, 0, 66.0), // the day before: excluded
	}
	hi, ok := PostHigh(bars, "2026-10-02", ny)
	if !ok || hi != 5.40 {
		t.Fatalf("after-hours high want 5.40 got %v ok=%v", hi, ok)
	}
	if _, ok := PostHigh(bars[:1], "2026-10-02", ny); ok {
		t.Fatal("no after-hours bars must report ok=false")
	}
}

func TestPrevTradingDay(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-05": "2026-10-02", // Monday -> Friday
		"2026-10-06": "2026-10-05", // Tuesday -> Monday
		"2026-10-04": "2026-10-02", // Sunday -> Friday
	} {
		d, _ := time.ParseInLocation("2006-01-02", in, ny)
		if got := PrevTradingDay(d).Format("2006-01-02"); got != want {
			t.Errorf("PrevTradingDay(%s) = %s, want %s", in, got, want)
		}
	}
}

// harness: fakes for every Config func, recording calls.
type harness struct {
	mu       sync.Mutex
	sess     core.Session
	seeds    []core.Seed
	active   map[string]struct{}
	ahHigh   map[string]float64 // sym -> after-hours high on any requested date
	limitSym string             // this symbol returns HTTP 429
	fetched  []string
	dates    map[string]bool
	applied  map[string]float64
	seedHits int
}

func (h *harness) worker(cap int) *Worker {
	return New(Config{
		Seeds: func(context.Context) ([]core.Seed, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.seedHits++
			return h.seeds, nil
		},
		Active: func() map[string]struct{} {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.active
		},
		BarsOn: func(_ context.Context, sym, date string) ([]core.Bar, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.fetched = append(h.fetched, sym)
			h.dates[date] = true
			if sym == h.limitSym {
				return nil, &feed.HTTPError{Status: 429, Msg: "HTTP 429"}
			}
			if hi, ok := h.ahHigh[sym]; ok {
				return []core.Bar{bar(date, 17, 0, hi)}, nil
			}
			return []core.Bar{bar(date, 10, 0, 1)}, nil // traded, but not after hours
		},
		Apply: func(m map[string]float64) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.applied = m
		},
		Session: func() core.Session {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.sess
		},
		Cap:  cap,
		Pace: time.Millisecond,
	})
}

// seeds ranked by volume: AAA > BBB > CCC > DDD > EEE.
func newHarness() *harness {
	vol := map[string]int64{"AAA": 9e6, "BBB": 8e6, "CCC": 7e6, "DDD": 6e6, "EEE": 5e6}
	h := &harness{sess: core.SessionPre, dates: map[string]bool{},
		ahHigh: map[string]float64{"AAA": 5.4, "BBB": 12.1, "DDD": 3.3, "EEE": 7.7, "NEW": 2.2}}
	for s, v := range vol {
		h.seeds = append(h.seeds, core.Seed{Agg: core.Agg{Sym: s, AccVol: v}})
	}
	return h
}

func (h *harness) fetchedSorted() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.fetched...)
	sort.Strings(out)
	return out
}

// Monday 2026-10-05 in ET.
func mon(hh, mm, ss int) time.Time { return time.Date(2026, 10, 5, hh, mm, ss, 0, ny) }

func TestWorkerIdleWhileClosed(t *testing.T) {
	h := newHarness()
	h.sess = core.SessionClosed
	w := h.worker(10)
	w.tick(context.Background(), mon(3, 0, 0))
	w.tick(context.Background(), mon(3, 30, 0))
	if h.seedHits != 0 || len(h.fetched) != 0 {
		t.Fatalf("closed session must do nothing: seeds %d fetched %v", h.seedHits, h.fetched)
	}
}

func TestWorkerSettlesThenFetchesPreviousDayAndApplies(t *testing.T) {
	h := newHarness()
	w := h.worker(3)
	ctx := context.Background()

	w.tick(ctx, mon(4, 0, 2)) // session just opened: settle first (let the 04:00 reset land)
	if h.seedHits != 0 {
		t.Fatal("must wait for the settle period before fetching")
	}
	w.tick(ctx, mon(4, 0, 20))
	if got := h.fetchedSorted(); len(got) != 3 || got[0] != "AAA" || got[1] != "BBB" || got[2] != "CCC" {
		t.Fatalf("cap 3 -> the top 3 ranked: got %v", got)
	}
	if !h.dates["2026-10-02"] || len(h.dates) != 1 {
		t.Fatalf("Monday must fetch FRIDAY's bars only, got dates %v", h.dates)
	}
	// CCC traded but not after hours: no level for it.
	if len(h.applied) != 2 || h.applied["AAA"] != 5.4 || h.applied["BBB"] != 12.1 {
		t.Fatalf("applied levels want AAA=5.4 BBB=12.1, got %v", h.applied)
	}
}

func TestWorkerTopsUpNewNamesWithinTheDailyCap(t *testing.T) {
	h := newHarness()
	w := h.worker(4)
	ctx := context.Background()
	w.tick(ctx, mon(4, 0, 0))
	w.tick(ctx, mon(4, 0, 20)) // pass 1: AAA BBB CCC DDD (cap 4 used)
	h.mu.Lock()
	h.active = map[string]struct{}{"NEW": {}}
	h.mu.Unlock()
	w.tick(ctx, mon(4, 5, 0)) // before Every: no new pass
	w.tick(ctx, mon(4, 11, 0))
	if got := h.fetchedSorted(); len(got) != 4 {
		t.Fatalf("daily cap 4 is spent: no further fetches, got %v", got)
	}

	h2 := newHarness()
	w2 := h2.worker(6)
	w2.tick(ctx, mon(4, 0, 0))
	w2.tick(ctx, mon(4, 0, 20)) // pass 1: all 5 seeds
	h2.mu.Lock()
	h2.active = map[string]struct{}{"NEW": {}, "AAA": {}}
	h2.mu.Unlock()
	w2.tick(ctx, mon(4, 11, 0)) // pass 2: only NEW (AAA already tried today)
	got := h2.fetchedSorted()
	if len(got) != 6 || got[len(got)-1] != "NEW" {
		t.Fatalf("top-up must fetch only the new name: got %v", got)
	}
	if h2.applied["NEW"] != 2.2 || h2.applied["AAA"] != 5.4 {
		t.Fatalf("levels must accumulate across passes: %v", h2.applied)
	}
}

func TestWorkerStopsForTheDayOn429(t *testing.T) {
	h := newHarness()
	h.limitSym = "BBB"
	w := h.worker(10)
	ctx := context.Background()
	w.tick(ctx, mon(4, 0, 0))
	w.tick(ctx, mon(4, 0, 20))
	n := len(h.fetchedSorted())
	h.mu.Lock()
	h.active = map[string]struct{}{"NEW": {}}
	h.mu.Unlock()
	w.tick(ctx, mon(4, 11, 0))
	if got := len(h.fetchedSorted()); got != n {
		t.Fatalf("after a 429 nothing more is fetched today: %d -> %d", n, got)
	}
	if _, ok := h.applied["AAA"]; !ok {
		t.Fatalf("levels fetched before the 429 are still applied: %v", h.applied)
	}

	// The next ET day starts fresh, against ITS previous trading day.
	h.mu.Lock()
	h.limitSym, h.dates = "", map[string]bool{}
	delete(h.ahHigh, "AAA") // Monday: AAA didn't trade after hours
	h.mu.Unlock()
	tue := func(hh, mm, ss int) time.Time { return time.Date(2026, 10, 6, hh, mm, ss, 0, ny) }
	h.sess = core.SessionClosed
	w.tick(ctx, tue(3, 0, 0))
	h.sess = core.SessionPre
	w.tick(ctx, tue(4, 0, 0))
	w.tick(ctx, tue(4, 0, 20))
	if !h.dates["2026-10-05"] {
		t.Fatalf("Tuesday must fetch MONDAY's after-hours, got %v", h.dates)
	}
	lv := w.Levels()
	if len(lv) == 0 {
		t.Fatal("new day must fetch again after yesterday's 429")
	}
	if _, stale := lv["AAA"]; stale {
		t.Fatalf("Friday's AAA level must not survive into Tuesday: %v", lv)
	}
}

func TestEnvCapZeroDisables(t *testing.T) {
	t.Setenv("SCANNER_PREVAH_CAP", "0")
	w := New(Config{Session: func() core.Session { return core.SessionPre }})
	if err := w.Run(context.Background()); err != nil || w.cfg.Cap >= 0 {
		t.Fatalf("SCANNER_PREVAH_CAP=0 must disable: cap %d err %v", w.cfg.Cap, err)
	}
}

var _ = errors.New

func TestWorkerKeepsOnlyUniverseSymbols(t *testing.T) {
	h := newHarness()
	h.seeds = append(h.seeds, core.Seed{Agg: core.Agg{Sym: "AAAW", AccVol: 99e6}}) // a warrant: top "mover"
	h.active = map[string]struct{}{"BBBR": {}}                                     // a right in a scan
	w := h.worker(10)
	w.cfg.Keep = func(sym string) bool { return sym != "AAAW" && sym != "BBBR" }
	w.tick(context.Background(), mon(4, 0, 0))
	w.tick(context.Background(), mon(4, 0, 20))
	for _, s := range h.fetchedSorted() {
		if s == "AAAW" || s == "BBBR" {
			t.Fatalf("non-universe symbol %s must not cost a fetch: %v", s, h.fetchedSorted())
		}
	}
	if len(h.fetchedSorted()) != 5 {
		t.Fatalf("all 5 universe symbols should be fetched, got %v", h.fetchedSorted())
	}
}
