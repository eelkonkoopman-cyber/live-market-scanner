package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// g2Feed is a one-day market: VWP crosses at 09:41 and is stopped out; SPY (not in
// the grouped bars, so the prefilter never asks for it) has minute bars too.
func g2Feed(t *testing.T) *fake {
	fk := &fake{grouped: map[string]map[string]core.DailyBar{}, bars: map[string][]core.Bar{}}
	for _, d := range []string{"2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"} {
		fk.grouped[d] = map[string]core.DailyBar{"VWP": db(100, 101, 2_000_000)}
	}
	fk.grouped["2026-10-05"] = map[string]core.DailyBar{"VWP": db(101, 102, 3_000_000)}
	bars := etBars(t, "2026-10-02", "15:50", 10, 100, 100_000)
	bars = append(bars, etBars(t, "2026-10-05", "08:50", 51, 100, 100_000)...)
	cross := etBars(t, "2026-10-05", "09:41", 3, 100, 100_000)
	cross[0].H, cross[0].C = 101, 101
	cross[1].O, cross[1].H, cross[1].L, cross[1].C = 100.5, 100.6, 99.5, 100
	bars = append(bars, cross...)
	fk.bars["VWP"] = bars
	spy := etBars(t, "2026-10-02", "15:50", 10, 400, 1000)
	spy = append(spy, etBars(t, "2026-10-05", "08:50", 60, 400, 1000)...)
	fk.bars["SPY"] = spy
	return fk
}

func TestGrid2EndToEndWithBenchmark(t *testing.T) {
	fk := g2Feed(t)
	dir := t.TempDir()
	f, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	var out, errb bytes.Buffer
	code := runVwapcross(context.Background(), f, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, Resamples: 100, Seed: 1,
		Grid2: true, Bench: "spy", GeneratedAt: "t", Command: "t"}, filepath.Join(dir, "out"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if n := fk.barsN.Load(); n != 2 {
		t.Errorf("%d minute-bar requests, want 2 (VWP + the benchmark)", n)
	}
	js, err := os.ReadFile(filepath.Join(dir, "out", "grid2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Version   string   `json:"gridVersion"`
		Bench     string   `json:"benchmark"`
		BenchDays int      `json:"benchmarkDaysWithBars"`
		Missing   []string `json:"benchmarkDaysMissing"`
		Variants  []struct {
			ID    string
			Stats struct{ Trades int }
			Extra struct {
				Excess struct{ N, Missing int } `json:"excessVsBenchmark"`
			}
		}
	}
	if err := json.Unmarshal(js, &rep); err != nil || len(rep.Variants) != 13 || rep.Version != "grid v2" || rep.Bench != "SPY" || rep.BenchDays != 1 || len(rep.Missing) != 0 {
		t.Fatalf("%v %d %s %s %d %v", err, len(rep.Variants), rep.Version, rep.Bench, rep.BenchDays, rep.Missing)
	}
	for i := 1; i < len(rep.Variants); i++ {
		if rep.Variants[i-1].ID >= rep.Variants[i].ID {
			t.Errorf("not sorted by id: %s then %s", rep.Variants[i-1].ID, rep.Variants[i].ID)
		}
	}
	for _, v := range rep.Variants {
		switch v.ID {
		case "R0":
			if v.Stats.Trades != 1 || v.Extra.Excess.N != 1 {
				t.Errorf("R0 %+v", v)
			}
		case "H1", "H3", "H4":
			if v.Stats.Trades > 1 {
				t.Errorf("%s %+v", v.ID, v)
			}
		case "D1", "D2", "D3", "D4", "D5", "D6", "D7", "P1":
			if v.Stats.Trades != 0 { // the day is up +1%, not a down day
				t.Errorf("%s traded on an up day", v.ID)
			}
		}
	}
	for _, n := range []string{"grid2.md", "grid2.csv", "grid2_trades_D5.csv", "grid2_trades_P1.csv"} {
		if _, err := os.Stat(filepath.Join(dir, "out", n)); err != nil {
			t.Error(err)
		}
	}
}

// -dry reads what is cached: no benchmark bars means the days are reported missing, nothing is fetched.
func TestGrid2DryWithoutBenchmarkReportsMissing(t *testing.T) {
	fk := g2Feed(t)
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	f, _ := newFetcher(fk, cache)
	var out, errb bytes.Buffer
	opts := vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, Resamples: 100, Seed: 1, Grid2: true, Bench: "SPY", GeneratedAt: "t", Command: "t"}
	if code := runVwapcross(context.Background(), f, opts, filepath.Join(dir, "o1"), &out, &errb); code != 0 {
		t.Fatalf("fill: %d %s", code, errb.String())
	}
	// remove the cached benchmark bars, rerun dry
	if err := os.Remove(filepath.Join(cache, "bars", "SPY_2026-10-05.json")); err != nil {
		t.Fatal(err)
	}
	before := fk.barsN.Load()
	fd, _ := newFetcher(nil, cache)
	out.Reset()
	if code := runVwapcross(context.Background(), fd, opts, filepath.Join(dir, "o2"), &out, &errb); code != 0 {
		t.Fatalf("dry: %d %s", code, errb.String())
	}
	if fk.barsN.Load() != before {
		t.Error("a dry run fetched")
	}
	js, _ := os.ReadFile(filepath.Join(dir, "o2", "grid2.json"))
	var rep struct {
		Missing  []string `json:"benchmarkDaysMissing"`
		Variants []struct {
			ID    string
			Extra struct {
				Excess struct{ N, Missing int } `json:"excessVsBenchmark"`
			}
		}
	}
	if err := json.Unmarshal(js, &rep); err != nil || len(rep.Missing) != 1 || rep.Missing[0] != "2026-10-05" {
		t.Fatalf("%v %v", err, rep.Missing)
	}
	for _, v := range rep.Variants {
		if v.ID == "R0" && (v.Extra.Excess.N != 0 || v.Extra.Excess.Missing != 1) {
			t.Errorf("R0 excess %+v", v.Extra.Excess)
		}
	}
}

// -benchonly asks for the benchmark's minute bars only (no other symbol, no splits).
func TestBenchOnlyFetchesOnlyTheBenchmark(t *testing.T) {
	fk := g2Feed(t)
	dir := t.TempDir()
	f, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	var out, errb bytes.Buffer
	code := runVwapcross(context.Background(), f, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, BenchOnly: true, Bench: "SPY"}, filepath.Join(dir, "out"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if n := fk.barsN.Load(); n != 1 || fk.perSym["SPY"] != 1 || fk.splitsN.Load() != 0 {
		t.Errorf("bars %d perSym %v splits %d", n, fk.perSym, fk.splitsN.Load())
	}
	if !strings.Contains(out.String(), "benchonly SPY") || !strings.Contains(out.String(), "1 with bars, 0 missing") {
		t.Errorf("out %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); err == nil {
		t.Error("benchonly wrote an output directory")
	}
	// a second run is served from the cache
	out.Reset()
	f2, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	if code := runVwapcross(context.Background(), f2, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, BenchOnly: true, Bench: "SPY"}, filepath.Join(dir, "out"), &out, &errb); code != 0 || fk.barsN.Load() != 1 {
		t.Errorf("rerun: exit %d bars %d", code, fk.barsN.Load())
	}
}

func TestGrid2FlagsAndEmptyCache(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := realMain([]string{"-dry", "-grid2", "-from", "2026-10-01", "-end", "2026-10-28", "-cache", filepath.Join(dir, "c"), "-out", filepath.Join(dir, "o")}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "nothing cached") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	for _, args := range [][]string{
		{"-dry", "-grid2", "-grid"}, {"-dry", "-grid2", "-drift"}, {"-dry", "-benchonly", "-grid2"}, {"-dry", "-grid2", "-strategy", "flag"},
	} {
		errb.Reset()
		if code := realMain(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d %q", args, code, errb.String())
		}
	}
}
