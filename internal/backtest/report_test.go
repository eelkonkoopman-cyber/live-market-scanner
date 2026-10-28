package backtest

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleReport(t *testing.T) (Report, []VariantResult) {
	t.Helper()
	pd := beatDay(t)
	empty := PreparedDay{Date: "2026-10-06"}
	days := []PreparedDay{pd, empty}
	var results []VariantResult
	for _, r := range Variants() {
		results = append(results, RunVariant(days, r))
	}
	meta := RunMeta{
		GeneratedAt: "2026-10-06 12:00 ET", EndDate: "2026-10-05", Days: []string{"2026-10-05", "2026-10-06"}, Command: "backtest -days 2",
		Data: DataStats{GroupedCalls: 3, GroupedHits: 1, MinuteCalls: 5, MinuteHits: 2, RateLimited: 1, FailureCount: 1,
			Failures: []Failure{{Key: "XYZ 2026-10-05", Err: "HTTP 500"}}, Holidays: []string{"2026-09-07"}},
		Prep: []DayPrep{{Date: "2026-10-05", Fetched: 1, Admitted: 1, WithSignals: 1, Signals: 1, Prefilter: PrefilterStats{Universe: 100, Passed: 10, Dropped: 2}}},
	}
	return BuildReport(meta, results, StatOpts{Resamples: 500, Seed: 9}), results
}

func TestReportBuildsAndRenders(t *testing.T) {
	rep, results := sampleReport(t)
	if len(rep.Variants) != 5 {
		t.Fatalf("%d variants", len(rep.Variants))
	}
	v1 := rep.Variants[0]
	// BEAT 2026-10-05 is the tuned day: all = only; without it there is nothing
	if v1.All.Trades != 1 || v1.OnlyTune.Trades != 1 || v1.ExTuned.Trades != 0 {
		t.Errorf("all %d, tuned day only %d, excluding it %d", v1.All.Trades, v1.OnlyTune.Trades, v1.ExTuned.Trades)
	}
	if v1.All.Days != 2 || v1.ExTuned.Days != 1 || v1.OnlyTune.Days != 1 {
		t.Errorf("days: %d / %d / %d", v1.All.Days, v1.ExTuned.Days, v1.OnlyTune.Days)
	}
	if v1.All.TotalNet <= 0 || v1.All.TotalGross <= v1.All.TotalNet {
		t.Errorf("BEAT: net %v gross %v", v1.All.TotalNet, v1.All.TotalGross)
	}

	md := rep.Markdown()
	for _, want := range []string{
		"# 9EMA flag auto-trader", "## Expected value per trade, all days", "excluding 2026-10-05", "## Data and method",
		"## V1:", "## V2:", "## V3:", "## V4:", "## V5:", "By price band", "By entry time", "By month",
		"Signals not traded, by reason", "## Caveats, in plain language", "tuned on 2026-10-05", "split-adjusted",
		"## Appendix", "XYZ 2026-10-05: HTTP 500", "95% CI", "Profit factor",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report.md lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(md), "apikey") || strings.Contains(md, "NaN") || strings.Contains(md, "Inf") {
		t.Errorf("report.md carries a key or a non-number")
	}

	// summary.json parses back and is free of NaN
	js, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatalf("summary.json does not parse: %v", err)
	}
	if len(back.Variants) != 5 || back.Variants[0].All.Trades != 1 || back.Meta.Resamples != 500 {
		t.Errorf("round trip: %+v", back.Meta)
	}

	// trades.csv: a header and one row per trade
	var buf bytes.Buffer
	if err := WriteTradesCSV(&buf, results); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	for _, r := range results {
		want += len(r.Trades)
	}
	if len(rows) != want || rows[0][0] != "variant" || rows[1][1] != "2026-10-05" || rows[1][2] != "BEAT" {
		t.Errorf("csv has %d rows (want %d), first row %v", len(rows), want, rows[1])
	}
	for _, row := range rows {
		if len(row) != len(csvHeader) {
			t.Errorf("row of %d fields, header has %d", len(row), len(csvHeader))
		}
	}

	// the console summary
	cs := rep.ConsoleSummary()
	for _, want := range []string{"BACKTEST 2026-10-05 .. 2026-10-06", "budget:", "ALL DAYS", "EXCLUDING 2026-10-05", "V1", "V5"} {
		if !strings.Contains(cs, want) {
			t.Errorf("console summary lacks %q:\n%s", want, cs)
		}
	}
}

func TestWriteFiles(t *testing.T) {
	rep, results := sampleReport(t)
	dir := filepath.Join(t.TempDir(), "out")
	paths, err := WriteFiles(dir, rep, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("paths %v", paths)
	}
	for _, p := range paths {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, name := range []string{"report.md", "trades.csv", "summary.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written", name)
		}
	}
}
