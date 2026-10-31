package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func etBars(t testing.TB, date, start string, n int, px float64, v int64) []core.Bar {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	t0, err := time.ParseInLocation("2006-01-02 15:04", date+" "+start, loc)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]core.Bar, n)
	for i := range out {
		out[i] = core.Bar{T: t0.UnixMilli() + int64(i)*60_000, O: px, H: px, L: px, C: px, V: v}
	}
	return out
}

// -dry on an empty cache: a clean exit 1 with a "nothing cached" message and no network.
func TestVwapcrossDryEmptyCache(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := realMain([]string{"-dry", "-strategy", "vwapcross", "-from", "2026-10-01", "-end", "2026-10-28",
		"-cache", filepath.Join(dir, "c"), "-out", filepath.Join(dir, "o")}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "nothing cached") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "o")); err == nil {
		t.Error("an output directory was created for nothing")
	}
}

func TestVwapcrossRejectsFlagStrategyWithFrom(t *testing.T) {
	var out, errb bytes.Buffer
	if code := realMain([]string{"-dry", "-from", "2026-10-01"}, &out, &errb); code != 2 {
		t.Errorf("exit %d %q", code, errb.String())
	}
}

// A whole run on the in-memory feed: a cross on VWP is found and traded by both variants.
func TestVwapcrossEndToEnd(t *testing.T) {
	fk := &fake{grouped: map[string]map[string]core.DailyBar{}, bars: map[string][]core.Bar{}}
	for _, d := range []string{"2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"} {
		fk.grouped[d] = map[string]core.DailyBar{"VWP": db(100, 101, 2_000_000), "CHEAP": db(5, 5.1, 9_000_000)}
	}
	fk.grouped["2026-10-05"] = map[string]core.DailyBar{"VWP": db(101, 102, 3_000_000), "CHEAP": db(5, 5.1, 9_000_000), "QUIET": db(100, 101, 100)}
	bars := etBars(t, "2026-10-02", "15:50", 10, 100, 100_000)
	bars = append(bars, etBars(t, "2026-10-05", "08:50", 51, 100, 100_000)...)
	cross := etBars(t, "2026-10-05", "09:41", 3, 100, 100_000)
	cross[0].H, cross[0].C = 101, 101                                        // cross bar: closes above the VWAP
	cross[1].O, cross[1].H, cross[1].L, cross[1].C = 100.5, 100.6, 99.5, 100 // entry bar, stops out
	bars = append(bars, cross...)
	fk.bars["VWP"] = bars

	dir := t.TempDir()
	f, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	var out, errb bytes.Buffer
	code := runVwapcross(context.Background(), f, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, Resamples: 200, Seed: 1, GeneratedAt: "t", Command: "t"}, filepath.Join(dir, "out"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if n := fk.barsN.Load(); n != 1 {
		t.Errorf("%d minute-bar requests, want 1 (only VWP passes the prefilter)", n)
	}
	js, err := os.ReadFile(filepath.Join(dir, "out", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Variants []struct {
			Variant string
			Stats   struct{ Trades int }
		}
	}
	if err := json.Unmarshal(js, &rep); err != nil || len(rep.Variants) != 2 {
		t.Fatalf("%v %s", err, js)
	}
	for _, v := range rep.Variants {
		if v.Stats.Trades != 1 {
			t.Errorf("%s: %d trades, want 1", v.Variant, v.Stats.Trades)
		}
	}
}

// -plan reads the grouped days only: not one minute-bar request.
func TestVwapcrossPlanFetchesNoMinuteBars(t *testing.T) {
	fk := &fake{grouped: map[string]map[string]core.DailyBar{}, bars: map[string][]core.Bar{}}
	for _, d := range []string{"2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25", "2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01", "2026-10-02"} {
		fk.grouped[d] = map[string]core.DailyBar{"VWP": db(100, 101, 2_000_000)}
	}
	fk.grouped["2026-10-05"] = map[string]core.DailyBar{"VWP": db(101, 102, 3_000_000)}
	dir := t.TempDir()
	f, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	var out, errb bytes.Buffer
	code := runVwapcross(context.Background(), f, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, Plan: true}, filepath.Join(dir, "out"), &out, &errb)
	if code != 0 || fk.barsN.Load() != 0 || !strings.Contains(out.String(), "1 minute-bar requests") {
		t.Fatalf("exit %d bars %d out %q err %q", code, fk.barsN.Load(), out.String(), errb.String())
	}
}

// -dry -grid on an empty cache: the same clean "nothing cached" exit, nothing written.
func TestGridDryEmptyCache(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := realMain([]string{"-dry", "-grid", "-from", "2026-10-01", "-end", "2026-10-28",
		"-cache", filepath.Join(dir, "c"), "-out", filepath.Join(dir, "o")}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "nothing cached") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "o")); err == nil {
		t.Error("an output directory was created for nothing")
	}
	if code := realMain([]string{"-dry", "-grid", "-strategy", "flag"}, &out, &errb); code != 2 {
		t.Errorf("-grid with -strategy flag: exit %d", code)
	}
}

// A whole -grid run on the in-memory feed writes grid.md/csv/json with all 19 variants.
func TestGridEndToEnd(t *testing.T) {
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

	dir := t.TempDir()
	f, _ := newFetcher(fk, filepath.Join(dir, "cache"))
	var out, errb bytes.Buffer
	code := runVwapcross(context.Background(), f, vwOptions{Days: 1, HistDays: 10, End: "2026-10-05", AsOf: "2026-10-06", MaxCand: 100000, Resamples: 100, Seed: 1, Grid: true, GeneratedAt: "t", Command: "t"}, filepath.Join(dir, "out"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	js, err := os.ReadFile(filepath.Join(dir, "out", "grid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Version  string `json:"gridVersion"`
		Variants []struct {
			ID    string
			Stats struct{ Trades int }
		}
	}
	if err := json.Unmarshal(js, &rep); err != nil || len(rep.Variants) != 19 || rep.Version != "grid v1" {
		t.Fatalf("%v %d %s", err, len(rep.Variants), rep.Version)
	}
	for i := 1; i < len(rep.Variants); i++ {
		if rep.Variants[i-1].ID >= rep.Variants[i].ID {
			t.Errorf("not sorted by id: %s then %s", rep.Variants[i-1].ID, rep.Variants[i].ID)
		}
	}
	for _, v := range rep.Variants {
		if v.ID == "B0" && v.Stats.Trades != 1 {
			t.Errorf("B0 trades %d", v.Stats.Trades)
		}
	}
	for _, n := range []string{"grid.md", "grid.csv"} {
		if _, err := os.Stat(filepath.Join(dir, "out", n)); err != nil {
			t.Error(err)
		}
	}
}
