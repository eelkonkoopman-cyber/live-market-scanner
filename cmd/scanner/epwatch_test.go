package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestSeedEPScansOnceUnderOwnKeys(t *testing.T) {
	for _, tc := range []struct {
		key, wantKey, id string
		defs             []core.ScanDef
	}{
		{epWatchSeedKey, "seeded.epwatch.v1", epWatchID, epWatchScans()},
		{epBreakSeedKey, "seeded.epbreak.v1", epBreakID, epBreakScans()},
	} {
		h := newSeedHarness()
		seedScans(context.Background(), tc.key, tc.defs, h.getMeta, h.setMeta, nil, h.save, h.install)
		if tc.key != tc.wantKey || strings.Join(h.saved, ",") != tc.id || h.meta[tc.key] != "1" {
			t.Fatalf("%s: saved=%v meta=%v", tc.key, h.saved, h.meta)
		}
		h.saved, h.installed = nil, nil
		seedScans(context.Background(), tc.key, tc.defs, h.getMeta, h.setMeta, nil, h.save, h.install)
		if len(h.saved) != 0 || len(h.installed) != 0 {
			t.Fatalf("%s re-seeded: %v %v", tc.key, h.saved, h.installed)
		}
		// A deleted scan is not resurrected: the key is set, the batch is skipped.
		h2 := newSeedHarness()
		h2.meta[tc.key] = "1"
		seedScans(context.Background(), tc.key, tc.defs, h2.getMeta, h2.setMeta, nil, h2.save, h2.install)
		if len(h2.saved) != 0 {
			t.Fatalf("%s resurrected", tc.key)
		}
	}
}

func TestEPScanShapes(t *testing.T) {
	w := epWatchScans()[0]
	if w.ID != "ep-watch" || w.Name != "EP Watch" || !w.Enabled || !w.Alert.Enabled || !w.Alert.Speak || w.Alert.On != core.AlertOnEnter {
		t.Fatalf("watch: %+v", w)
	}
	if len(w.Filter.Conds) != 2 || w.Filter.Conds[0].Field != core.FieldSession || w.Filter.Conds[0].Value != "pre" || w.Filter.Conds[1].Field != core.FieldEpPass {
		t.Fatalf("watch filter: %+v", w.Filter)
	}
	b := epBreakScans()[0]
	if b.ID != "ep-break" || b.Name != "EP Break" || !b.Alert.Enabled || !b.Alert.Speak || b.Alert.CooldownSec < 86400 {
		t.Fatalf("break: %+v", b)
	}
	if len(b.Filter.Conds) != 1 || b.Filter.Conds[0].Field != core.FieldEpBrkAge {
		t.Fatalf("break filter: %+v", b.Filter)
	}
}

func TestEPRefForFallsBackAndDropsToday(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	mk := func(n int, last string) []core.DailyBar {
		out := make([]core.DailyBar, n)
		for i := range out {
			out[i] = core.DailyBar{Date: "2026-09-01", H: 10.5, L: 10, C: 10}
		}
		out[n-1].Date = last
		return out
	}
	// REST fails: the stored bars give an ADR but no 3-month change.
	src := epSource{
		Now:        func() time.Time { return now },
		Daily:      func(context.Context, string, string, string) ([]core.DailyBar, error) { return nil, errors.New("down") },
		StoredBars: func(context.Context, string, string) ([]core.DailyBar, error) { return mk(30, "2026-10-19"), nil },
	}
	ref, ok := epRefFor(context.Background(), src, "X", "2026-10-20")
	if !ok || !core.IsNull(ref.Chg3m) || ref.AdrDays != 20 || ref.AdrPct < 4.9 || ref.AdrPct > 5.1 {
		t.Fatalf("fallback: %+v ok=%v", ref, ok)
	}
	// REST gives 70 bars incl. a partial bar for today: today is dropped.
	src.Daily = func(context.Context, string, string, string) ([]core.DailyBar, error) {
		return mk(70, "2026-10-20"), nil
	}
	ref, ok = epRefFor(context.Background(), src, "X", "2026-10-20")
	if !ok || core.IsNull(ref.Chg3m) || ref.AdrDays != 20 {
		t.Fatalf("rest: %+v", ref)
	}
}

func TestRunEPFetchesEachCandidateOncePerDay(t *testing.T) {
	oldEvery := epEvery
	epEvery = 5 * time.Millisecond
	defer func() { epEvery = oldEvery }()
	ctx, cancel := context.WithCancel(context.Background())
	calls := map[string]int{}
	pub := make(chan map[string]core.EPRef, 8)
	marked := 0
	src := epSource{
		Now:        func() time.Time { return time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC) },
		Candidates: func(int) []string { return []string{"AAA", "BBB"} },
		Daily: func(_ context.Context, sym, _, _ string) ([]core.DailyBar, error) {
			calls[sym]++
			b := make([]core.DailyBar, 70)
			for i := range b {
				b[i] = core.DailyBar{Date: "2026-09-01", H: 11, L: 10, C: 10}
			}
			return b, nil
		},
		Articles: func(context.Context, string, int) ([]core.NewsItem, error) {
			return []core.NewsItem{{Headline: "x"}}, nil
		},
		MarkNews: func([]core.NewsItem) { marked++ },
		Publish:  func(m map[string]core.EPRef) { pub <- m },
	}
	done := make(chan struct{})
	go func() { _ = runEP(ctx, src); close(done) }()
	m := <-pub
	if len(m) != 2 || m["AAA"].AdrDays != 20 {
		t.Fatalf("batch %+v", m)
	}
	time.Sleep(60 * time.Millisecond) // many more cycles: candidates are still listed, but already tried
	cancel()
	<-done
	if calls["AAA"] != 1 || calls["BBB"] != 1 || marked != 2 {
		t.Fatalf("calls %v marked %d", calls, marked)
	}
}
