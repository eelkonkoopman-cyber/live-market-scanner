package scan

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Scan definitions for the golden-equivalence test: the user's 32 live scans
// (testdata/live_scan_defs.json, a copy taken 2026-10-25) plus synthetic ones
// that together exercise every comparison operator, value type, group shape,
// sort form, limit, alert mode and column kind predicate.go / row.go /
// scanner.go support. Synthetic ids start with "gold-".

// goldFields is every core.Field* name (core/metric.go), so the per-step field
// probes (goldProbe) walk the whole fieldNum / fieldValue / sortValue switch.
var goldFields = []string{
	"last", "chg1m", "chg5m", "chg15m", "chgDay", "gap", "vol", "rvol",
	"avgVol", "vwap", "vsVwap", "hod", "lod", "vsHod", "brk5mAge", "vsPrev5mLow",
	"prev5mLow", "brk5mCloseAge", "vsPrev5mClose", "prev5mClose", "brkPmhAge", "vsPmh", "prevAhHigh", "brkPrevAhAge",
	"vsPrevAh", "adv10", "pmVolX", "brkPmh5mAge", "dbLevel", "dbType", "dbPct", "dbAge",
	"pdlLevel", "pdlPct", "pdlAge", "epPass", "epGap", "epVolX", "epEarnAge", "epEarnHL",
	"ep3m", "epAdr", "epAdrDays", "epStop", "epOrh", "epBrkAge", "epRiskPct", "brkPrevAh5mAge",
	"brkEma9Age", "vsEma9", "ema9", "c1mChg", "c1mRvol", "brk5mCloseUpAge", "chgSess", "sessHigh",
	"sessLow", "sessVol", "sessVwap", "preChg", "preHigh", "preLow", "preVol", "preVolPctAdv",
	"preVwap", "preGap", "preFade", "rthChg", "rthHigh", "rthLow", "rthVol", "rthVwap",
	"rthGap", "rthFade", "float", "shsOut", "rot", "news", "newsAge", "hits",
	"dtrend", "chg20d", "flagScore", "flagState", "flagPole", "flagRange", "flagRetrace", "flagVolDry",
	"flagBars", "iflagScore", "iflagState", "iflagPole", "iflagRange", "iflagRetrace", "iflagVolDry", "iflagBars",
	"e9State", "e9Dist", "e9Trigger", "e9Stop", "e9FlagBars", "e9Age", "e9SlamAge", "e9SlamPct",
	"e9HodFlagAge", "dildoAge", "dildoBody", "dildoVolX", "dildoDvol", "dildoEarly", "dildoPbAge", "dildoPbPx",
	"dildoTouchAge", "dildoPbState", "climaxAge", "climaxDropPct", "climaxVolX", "climaxWickPct", "climaxDvol", "climaxLow",
	"climaxState", "climaxConfAge", "climaxConfPx", "bbsAge", "bbsSupport", "bbsState", "bbsDvol", "bbsDistPct",
	"pcAge", "pcBodyPct", "pcTopPct", "pcBodyX", "pcVolX", "pcDvol", "ignAge", "ignBodyPct",
	"ignVolX", "ignFresh", "ignPass", "evBullAge", "evBearAge", "evEma", "evVwap", "evDist", "evBarVol", "evBarDvol", "evSessVol",
	"halted", "session", "secType", "shortable", "shortAvail", "shortFee",
}

// goldHeadlines are the news the overlay driver publishes: the first three read
// as earnings catalysts to ep.IsEarnings, the rest do not.
var goldHeadlines = []string{
	"Acme Reports Strong Q3 Earnings, Raises Full-Year Guidance",
	"Beta Corp Beats Estimates On Record Quarter Revenue",
	"Gamma Inc EPS Tops Outlook",
	"FDA Grants Fast Track Designation To Delta Therapeutics",
	"Shares Of Epsilon Are Trading Higher On No News",
	"Zeta Announces Strategic Partnership",
}

func goldLeaf(field, cmp string, v any) core.Filter {
	return core.Filter{Field: field, Cmp: cmp, Value: v}
}
func goldAnd(c ...core.Filter) core.Filter { return core.Filter{Op: "and", Conds: c} }
func goldOr(c ...core.Filter) core.Filter  { return core.Filter{Op: "or", Conds: c} }

// goldSort parses "field:dir" terms.
func goldSort(terms ...string) []core.SortKey {
	out := make([]core.SortKey, len(terms))
	for i, t := range terms {
		f, d, _ := strings.Cut(t, ":")
		out[i] = core.SortKey{Field: f, Dir: d}
	}
	return out
}

func goldCols(c ...string) []string { return c }

func goldEnter(cool int, sound string) core.AlertConfig {
	return core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: sound, CooldownSec: cool, RequireReentry: true}
}

func goldCross(field, cmp string, thr any, cool int) core.AlertConfig {
	return core.AlertConfig{Enabled: true, On: core.AlertOnCross, Sound: "tick", CooldownSec: cool,
		Trigger: &core.Filter{Field: field, Cmp: cmp, Value: thr}}
}

func goldDef(id string, f core.Filter, sort []core.SortKey, limit int, cols []string, al core.AlertConfig) core.ScanDef {
	return core.ScanDef{ID: id, Name: id, Enabled: true, Filter: f, Sort: sort, Limit: limit, Columns: cols, Alert: al}
}

// goldLiveDefs loads the user's real scans, decoded exactly as the app decodes
// them (plain encoding/json: numbers are float64, lists []any).
func goldLiveDefs(t testing.TB) []core.ScanDef {
	raw, err := os.ReadFile("testdata/live_scan_defs.json")
	if err != nil {
		t.Fatal(err)
	}
	var defs []core.ScanDef
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != 32 {
		t.Fatalf("testdata/live_scan_defs.json: want the 32 live scans, got %d", len(defs))
	}
	return defs
}

// goldSyntheticDefs are the synthetic scans that run from the first snapshot.
// goldEditDef / goldAddDef / goldToggleDef are the ones the driver changes
// mid-run.
func goldSyntheticDefs() []core.ScanDef {
	none := core.AlertConfig{}
	var d []core.ScanDef
	add := func(x ...core.ScanDef) { d = append(d, x...) }

	// --- comparison operators and value types ---
	add(
		// int / int64 / float32 / json.Number thresholds go through toFloat.
		goldDef("gold-num-int", goldAnd(goldLeaf("vol", ">", 50000), goldLeaf("last", ">=", int64(2))),
			goldSort("vol:desc"), 30, goldCols("last", "vol", "chg5m"), goldEnter(30, "a")),
		goldDef("gold-num-f32", goldAnd(goldLeaf("chg5m", ">", float32(0.25)), goldLeaf("vol", "<=", int64(900000))),
			goldSort("chg5m:asc"), 25, goldCols("last", "chg5m"), goldEnter(0, "b")),
		goldDef("gold-num-jn", goldLeaf("rvol", ">=", json.Number("1.5")),
			goldSort("rvol:desc"), 25, goldCols("last", "rvol", "vol"), none),
		goldDef("gold-lt-lte", goldAnd(goldLeaf("chg1m", "<", -0.5), goldLeaf("last", "<=", 30.0), goldLeaf("vol", ">=", 1)),
			goldSort("chg1m:asc"), 20, goldCols("last", "chg1m"), goldEnter(60, "c")),
		// between: every accepted pair shape, an inverted range, a bad shape.
		goldDef("gold-between", goldAnd(goldLeaf("last", "between", []any{5.0, 50.0}), goldLeaf("chg5m", "between", []float64{-1, 1})),
			goldSort("last:asc"), 40, goldCols("last", "chg5m"), none),
		goldDef("gold-between-int", goldAnd(goldLeaf("rvol", "between", []int{1, 3}), goldLeaf("vol", "between", []any{json.Number("1000"), 5e6})),
			goldSort("rvol:desc"), 30, goldCols("last", "rvol"), none),
		goldDef("gold-between-inv", goldLeaf("last", "between", []any{50.0, 5.0}),
			goldSort("last:asc"), 10, nil, goldEnter(0, "x")),
		goldDef("gold-between-bad", goldLeaf("last", "between", []any{1.0}),
			goldSort("last:asc"), 10, nil, none),
		// == on numbers (exact ties), strings, the empty string, a string on a
		// numeric field (never true) and a bool value (a compile error).
		goldDef("gold-eq-num", goldAnd(goldLeaf("dtrend", "==", 1), goldOr(goldLeaf("flagState", "==", 1.0), goldLeaf("iflagState", "==", 2.0))),
			goldSort("flagScore:desc", "iflagScore:desc"), 20, goldCols("last", "dtrend", "flagState", "flagScore", "iflagState"), goldEnter(20, "d")),
		goldDef("gold-eq-zero", goldLeaf("vol", "==", 0.0),
			goldSort("sym:asc"), 25, goldCols("last", "vol"), none),
		goldDef("gold-eq-c1m", goldAnd(goldLeaf("c1mChg", "==", 0.0), goldLeaf("last", ">", 5)),
			goldSort("last:desc"), 30, goldCols("last", "c1mChg", "c1mRvol"), none),
		goldDef("gold-eq-str", goldAnd(goldLeaf("session", "==", "regular"), goldLeaf("secType", "==", "ETF")),
			goldSort("vol:desc"), 25, goldCols("last", "vol", "secType", "session"), goldEnter(15, "e")),
		goldDef("gold-eq-str-empty", goldLeaf("secType", "==", ""),
			goldSort("sym:asc"), 0, goldCols("last", "secType"), none),
		goldDef("gold-eq-hl", goldLeaf("epEarnHL", "==", goldHeadlines[0]),
			goldSort("epGap:desc"), 10, goldCols("last", "epEarnHL", "epGap", "epEarnAge"), goldEnter(0, "f")),
		goldDef("gold-eq-strnum", goldLeaf("last", "==", "10"), nil, 10, nil, none),
		goldDef("gold-eq-bool", goldLeaf("news", "==", true), nil, 10, nil, none),
		// in: []any / []string / []float64 lists, mixed lists, empty, []int (not
		// a list toList knows: never matches).
		goldDef("gold-in-any", goldLeaf("secType", "in", []any{"ETF", "ADRC"}),
			goldSort("vol:desc"), 25, goldCols("last", "secType", "vol"), none),
		goldDef("gold-in-strs", goldAnd(goldLeaf("session", "in", []string{"pre", "post"}), goldLeaf("last", ">", 3)),
			goldSort("chgDay:desc"), 25, goldCols("last", "chgDay", "session"), none),
		goldDef("gold-in-num", goldLeaf("dtrend", "in", []any{1.0, -1.0}),
			goldSort("chg20d:desc", "sym:asc"), 25, goldCols("last", "dtrend", "chg20d"), none),
		goldDef("gold-in-nums", goldLeaf("flagState", "in", []float64{1, 2}),
			goldSort("flagScore:desc"), 25, goldCols("last", "flagState", "flagScore", "flagBars"), none),
		goldDef("gold-in-mixed", goldAnd(goldLeaf("secType", "in", []any{"CS", 5.0}), goldLeaf("vol", ">", 1e6)),
			goldSort("vol:desc"), 20, goldCols("last", "vol"), none),
		goldDef("gold-in-empty", goldLeaf("last", "in", []any{}), nil, 10, nil, none),
		goldDef("gold-in-int", goldLeaf("dtrend", "in", []int{1}), nil, 10, nil, none),
		// isTrue on each bool field and on a non-bool one (never true).
		goldDef("gold-true-news", goldLeaf("news", "isTrue", nil),
			goldSort("newsAge:asc"), 30, goldCols("last", "news", "newsAge"), goldEnter(10, "g")),
		goldDef("gold-true-halted", goldLeaf("halted", "isTrue", nil),
			goldSort("sym:asc"), 0, goldCols("last", "halted"), goldEnter(0, "h")),
		goldDef("gold-true-short", goldAnd(goldLeaf("shortable", "isTrue", nil), goldLeaf("last", ">", 3)),
			goldSort("shortAvail:desc"), 30, goldCols("last", "shortable", "shortAvail", "shortFee"), none),
		goldDef("gold-true-last", goldLeaf("last", "isTrue", nil), nil, 10, nil, none),
	)

	// --- group shapes and broken filters ---
	add(
		goldDef("gold-nest-or", goldAnd(goldLeaf("session", "==", "regular"),
			goldOr(goldLeaf("chg5m", ">", 2.0), goldAnd(goldLeaf("rvol", ">", 3.0), goldLeaf("newsAge", "<", 3600.0)), goldLeaf("news", "isTrue", nil))),
			goldSort("chg5m:desc"), 35, goldCols("last", "chg5m", "rvol", "news"), goldEnter(30, "i")),
		goldDef("gold-or-root", goldOr(goldLeaf("chgDay", ">", 10.0), goldLeaf("chgDay", "<", -10.0), goldLeaf("vol", ">", 8e6)),
			goldSort("chgDay:desc"), 40, goldCols("last", "chgDay", "vol"), none),
		goldDef("gold-deep", goldAnd(
			goldOr(goldAnd(goldLeaf("last", ">", 5.0), goldLeaf("last", "<", 40.0)), goldOr(goldLeaf("halted", "isTrue", nil), goldLeaf("rvol", ">=", 6.0))),
			goldAnd(), goldAnd(goldAnd(goldLeaf("vol", ">", 100.0)))),
			goldSort("vol:desc"), 30, goldCols("last", "vol"), none),
		goldDef("gold-deep-emptyor", goldAnd(goldLeaf("last", ">", 1.0), goldOr()), nil, 10, nil, none),
		goldDef("gold-empty-and", core.Filter{Op: "and"}, goldSort("vol:desc"), 5, nil, none),
		goldDef("gold-zero-filter", core.Filter{}, goldSort("chgDay:desc"), 10, nil, none),
		goldDef("gold-single", goldAnd(goldLeaf("chgDay", ">", 6.0)), goldSort("chgDay:desc"), 12, nil, none),
		goldDef("gold-bad-op", core.Filter{Op: "xor", Conds: []core.Filter{goldLeaf("last", ">", 1.0)}}, nil, 10, nil, none),
		goldDef("gold-bad-cmp", goldLeaf("last", "~=", 1.0), nil, 10, nil, none),
		goldDef("gold-bad-val", goldLeaf("last", ">", "5"), nil, 10, nil, none),
		goldDef("gold-bad-field", goldLeaf("bogus", ">", 0.0), nil, 10, nil, none),
	)

	// --- recurrence (hits) ---
	add(
		// A bare hits leaf and an or whose only child is hits: both count against
		// match-all. The flickering tickers re-enter that set, so they accrue hits.
		goldDef("gold-hits-leaf", goldLeaf("hits", ">=", 2.0),
			goldSort("hits:desc", "sym:asc"), 20, goldCols("last", "hits"), goldEnter(0, "j")),
		goldDef("gold-hits-or-only", goldOr(goldLeaf("hits", ">=", 3.0)),
			goldSort("hits:desc"), 20, goldCols("last", "hits"), none),
		goldDef("gold-hits-and", goldAnd(goldLeaf("chg5m", ">", 1.0), goldLeaf("hits", ">=", 2.0)),
			goldSort("hits:desc", "chg5m:desc"), 30, goldCols("last", "chg5m", "hits"), goldEnter(30, "k")),
		goldDef("gold-hits-or-mixed", goldOr(goldLeaf("hits", ">=", 3.0), goldLeaf("chgDay", ">", 15.0)),
			goldSort("hits:desc", "chgDay:desc"), 30, goldCols("last", "chgDay", "hits"), none),
		goldDef("gold-hits-nested", goldAnd(goldLeaf("session", "==", "regular"),
			goldOr(goldLeaf("hits", ">=", 2.0), goldAnd(goldLeaf("rvol", ">", 2.0), goldLeaf("hits", "<", 5.0)))),
			goldSort("hits:desc", "rvol:desc"), 30, goldCols("last", "rvol", "hits"), goldEnter(0, "l")),
		goldDef("gold-hits-sort", goldLeaf("chgDay", ">", 3.0),
			goldSort("hits:desc", "chgDay:desc"), 25, goldCols("hits", "chgDay", "last"), goldEnter(45, "m")),
		goldDef("gold-hits-bad", goldAnd(goldLeaf("chg5m", ">", 1.0), goldLeaf("hits", ">=", "x")),
			goldSort("hits:desc"), 10, goldCols("last", "hits"), goldEnter(0, "n")),
	)

	// --- sort forms ---
	add(
		goldDef("gold-sort-none", goldLeaf("chgDay", ">", 0.0), nil, 15, nil, none),
		goldDef("gold-sort-multi", goldLeaf("dbType", ">=", 1.0),
			goldSort("dbType:asc", "vol:desc", "sym:asc"), 30, goldCols("last", "dbType", "dbLevel", "vol"), none),
		goldDef("gold-sort-nan", goldAnd(goldLeaf("vol", ">", 0.0), goldLeaf("last", ">", 1.0)),
			goldSort("shortFee:desc", "float:asc"), 40, goldCols("last", "shortFee", "float", "shsOut"), none),
		goldDef("gold-sort-symasc", goldLeaf("last", ">", 40.0), goldSort("sym:asc"), 25, nil, none),
		goldDef("gold-sort-bool", goldLeaf("vol", ">", 3e5),
			goldSort("news:desc", "halted:desc", "newsAge:asc"), 30, goldCols("last", "news", "halted", "newsAge"), none),
		goldDef("gold-sort-bogus", goldLeaf("chgDay", ">", 8.0), goldSort("bogus:asc"), 20, nil, none),
		goldDef("gold-sort-baddir", goldLeaf("chg5m", ">", 2.0),
			[]core.SortKey{{Field: "chg5m", Dir: ""}, {Field: "vol", Dir: "ASC"}}, 20, nil, none),
		goldDef("gold-sort-str", goldLeaf("chgDay", ">", 6.0),
			goldSort("session:desc", "secType:asc", "chgDay:desc"), 20, goldCols("last", "session", "secType", "chgDay"), none),
		goldDef("gold-sort-grain", goldLeaf("last", "<", 10.0), goldSort("chg1m:asc"), 40, goldCols("last", "chg1m"), none),
		goldDef("gold-sort-symfirst", goldLeaf("chgDay", ">", 4.0), goldSort("sym:desc", "chg5m:desc"), 15, nil, none),
		goldDef("gold-sort-dup", goldLeaf("chg5m", ">", 1.5), goldSort("chg5m:desc", "chg5m:asc", "vol:desc"), 20, nil, none),
	)

	// --- limits ---
	add(
		goldDef("gold-limit0", goldLeaf("chgDay", ">", 12.0), goldSort("chgDay:desc"), 0, goldCols("last", "chgDay", "vol"), goldEnter(0, "o")),
		goldDef("gold-limit1", goldLeaf("chg5m", ">", 0.5), goldSort("chg5m:desc"), 1, goldCols("last", "chg5m"), goldEnter(0, "p")),
		goldDef("gold-limit3", goldLeaf("session", "==", "regular"), goldSort("vol:desc"), 3, goldCols("last", "vol"), goldEnter(0, "q")),
		goldDef("gold-limit-neg", goldLeaf("chgDay", ">", 14.0), goldSort("chgDay:desc"), -5, nil, none),
		goldDef("gold-all300", core.Filter{}, goldSort("sym:asc"), 300, nil, none),
	)

	// --- alerts ---
	add(
		goldDef("gold-al-enter0", goldLeaf("chg5m", ">", 3.0), goldSort("chg5m:desc"), 15, goldCols("last", "chg5m"), goldEnter(0, "r")),
		goldDef("gold-al-cool", goldLeaf("chg5m", ">", 2.0), goldSort("vol:desc"), 15, goldCols("last", "chg5m", "vol"),
			core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "s", CooldownSec: 45, Speak: true, SendToTWS: true}),
		goldDef("gold-al-off", goldLeaf("chg5m", ">", 2.0), goldSort("chg5m:desc"), 20, nil,
			core.AlertConfig{Enabled: false, On: core.AlertOnEnter, Sound: "t"}),
		goldDef("gold-al-above", goldLeaf("chg5m", "between", []any{0.5, 3.5}), goldSort("chg5m:desc"), 60, goldCols("last", "chg5m"),
			goldCross("chg5m", core.CmpCrossAbove, 2.0, 0)),
		goldDef("gold-al-below", goldLeaf("vsVwap", "between", []any{-1.0, 1.0}), goldSort("vsVwap:asc"), 80, goldCols("last", "vsVwap"),
			goldCross("vsVwap", core.CmpCrossBelow, 0.0, 20)),
		goldDef("gold-al-nan", goldLeaf("vol", ">", 0.0), goldSort("vol:desc"), 200, goldCols("last", "rvol"),
			goldCross("rvol", core.CmpCrossAbove, 1.0, 0)),
		goldDef("gold-al-hits", goldLeaf("chgDay", ">", 2.0), goldSort("hits:desc", "chgDay:desc"), 50, goldCols("last", "hits"),
			goldCross("hits", core.CmpCrossAbove, 1.5, 0)),
		goldDef("gold-al-int", goldLeaf("last", "between", []any{5.0, 20.0}), goldSort("last:asc"), 60, goldCols("last"),
			goldCross("last", core.CmpCrossAbove, 10, 0)),
		goldDef("gold-al-strthr", goldLeaf("chg1m", "between", []any{-3.0, 3.0}), goldSort("chg1m:asc"), 60, goldCols("last", "chg1m"),
			goldCross("chg1m", core.CmpCrossAbove, "x", 0)),
		goldDef("gold-al-group", goldLeaf("chgDay", ">", 5.0), goldSort("chgDay:desc"), 30, nil, core.AlertConfig{
			Enabled: true, On: core.AlertOnCross, Sound: "u",
			Trigger: &core.Filter{Op: "and", Conds: []core.Filter{goldLeaf("chg5m", core.CmpCrossAbove, 1.0)}}}),
		goldDef("gold-al-notrig", goldLeaf("chgDay", ">", 5.0), goldSort("chgDay:desc"), 30, nil,
			core.AlertConfig{Enabled: true, On: core.AlertOnCross, Sound: "v"}),
		goldDef("gold-al-gt", goldLeaf("chgDay", ">", 5.0), goldSort("chgDay:desc"), 30, nil,
			goldCross("chg5m", ">", 1.0, 0)),
		goldDef("gold-al-bogus", goldLeaf("chg5m", ">", 3.0), goldSort("chg5m:desc"), 15, nil,
			core.AlertConfig{Enabled: true, On: "bogus", Sound: "w", CooldownSec: 10}),
		goldDef("gold-al-empty", goldLeaf("chg5m", ">", 3.0), goldSort("chg5m:desc"), 15, nil,
			core.AlertConfig{Enabled: true, Sound: "y", CooldownSec: 10}),
		goldDef("gold-al-xcool", goldLeaf("chg5m", ">", 3.0), goldSort("chg5m:desc"), 15, nil,
			core.AlertConfig{Enabled: true, On: core.AlertOnEnter, Sound: "z", CooldownSec: 86400}),
		// A cross alert switched off: prevVal still tracks (see detectAlerts).
		goldDef("gold-al-xoff", goldLeaf("chg5m", "between", []any{0.0, 4.0}), goldSort("chg5m:desc"), 60, nil,
			core.AlertConfig{Enabled: false, On: core.AlertOnCross, Trigger: &core.Filter{Field: "chg5m", Cmp: core.CmpCrossAbove, Value: 1.0}}),
	)

	// --- columns ---
	add(
		goldDef("gold-cols-default", goldLeaf("chgDay", ">", 5.0), goldSort("chgDay:desc"), 25, nil, none),
		goldDef("gold-cols-chart", goldLeaf("chgDay", ">", 2.0), goldSort("chgDay:desc"), 40, goldCols("last", "chart", "chgDay"), goldEnter(0, "ca")),
		goldDef("gold-cols-dildo", goldLeaf("dildoAge", "<=", 3600.0), goldSort("dildoAge:asc"), 25, goldCols("last", "dildoAge", "dildoDvol"), goldEnter(60, "cb")),
		goldDef("gold-cols-ign", goldLeaf("ignAge", "<=", 3600.0), goldSort("ignAge:asc"), 25, goldCols("last", "ignAge", "ignVolX"), goldEnter(60, "cc")),
		goldDef("gold-cols-all", goldLeaf("chgDay", ">=", 4.0), goldSort("chgDay:desc"), 6,
			append(append([]string{}, goldFields...), "bogus", "chart", "sym", "bars", "shortCapped"), goldEnter(0, "cd")),
		goldDef("gold-cols-dup", goldLeaf("chgDay", ">", 5.0), goldSort("chgDay:desc"), 10,
			goldCols("last", "last", "chg5m", "chg5m", "chart", "chart", "dildoAge", "dildoAge"), none),
		goldDef("gold-cols-special", goldLeaf("chgDay", ">", 3.0), goldSort("chgDay:desc"), 25, goldCols(
			"dtrend", "flagState", "flagBars", "iflagState", "iflagBars", "e9State", "e9FlagBars", "dildoPbState", "climaxState", "bbsState",
			"dbType", "preVol", "rthVol", "float", "shsOut", "epEarnHL", "newsAge", "hits", "session", "secType", "halted", "news", "shortable", "shortAvail", "shortFee"), none),
		goldDef("gold-sess-closed", goldLeaf("session", "==", "closed"), goldSort("vol:desc"), 10, goldCols("last", "session", "vol"), goldEnter(0, "ce")),
	)

	// --- disabled for good ---
	off := goldDef("gold-disabled", goldLeaf("chgDay", ">", 1.0), goldSort("chgDay:desc"), 10, nil, goldEnter(0, "cf"))
	off.Enabled = false
	add(off)

	// --- the ones the driver edits mid-run start here ---
	add(goldEditDef(0), goldToggleDef(false))
	return d
}

// goldEditDef is "gold-edit" in its n-th form; the driver upserts the next form
// at steps 200, 330, 420 and 600.
func goldEditDef(n int) core.ScanDef {
	switch n {
	case 0:
		return goldDef("gold-edit", goldLeaf("chgDay", ">", 2.0), goldSort("chgDay:desc"), 20, nil, core.AlertConfig{})
	case 1: // filter, sort, limit, columns and alert all change
		return goldDef("gold-edit", goldAnd(goldLeaf("chg5m", ">", 1.0), goldLeaf("vol", ">", 20000.0)),
			goldSort("vol:desc"), 10, goldCols("last", "vol", "chg5m", "rvol"), goldEnter(30, "e1"))
	case 2: // the alert becomes a cross trigger
		return goldDef("gold-edit", goldAnd(goldLeaf("chg5m", ">", 0.0), goldLeaf("vol", ">", 20000.0)),
			goldSort("vol:desc"), 25, goldCols("last", "vol", "chg5m"), goldCross("chg5m", core.CmpCrossAbove, 1.0, 0))
	case 3: // the same cross alert, switched off
		d := goldEditDef(2)
		d.Alert.Enabled = false
		return d
	default: // and back on, with a different trigger
		d := goldEditDef(2)
		d.Alert = goldCross("chg5m", core.CmpCrossBelow, 1.0, 15)
		return d
	}
}

// goldToggleDef is "gold-toggle", enabled or not.
func goldToggleDef(on bool) core.ScanDef {
	d := goldDef("gold-toggle", goldLeaf("chgDay", ">", 1.0), goldSort("chgDay:desc"), 15, goldCols("last", "chgDay"), goldEnter(20, "tg"))
	d.Enabled = on
	return d
}

// goldAddDef is "gold-add", upserted for the first time mid-run.
func goldAddDef() core.ScanDef {
	return goldDef("gold-add", goldAnd(goldLeaf("rvol", ">", 1.0), goldLeaf("chg5m", ">", 0.2)),
		goldSort("rvol:desc"), 20, goldCols("last", "rvol", "chg5m"), goldEnter(0, "ad"))
}

// goldProbeField is the field probe scan k watches at the given step: the run
// walks the whole field list, three fields a step, so every branch of the
// fieldNum / fieldValue / sortValue switches meets real rows many times.
func goldProbeField(k, step int) string { return goldFields[(step*3+k)%len(goldFields)] }

// goldProbeDef is probe scan k around a filter the driver chose: sorted on the
// probe's field (the direction flips each pass over the list), streaming just
// that field, with a cross alert on it. Re-upserting it every step also keeps
// the (re)load paths (baseline suppression, full resend, the stale prevVal a
// def edit leaves behind) busy all run long.
func goldProbeDef(k, step int, f core.Filter) core.ScanDef {
	field := goldProbeField(k, step)
	dir := [...]string{"desc", "asc"}[(step/len(goldFields)+k)%2]
	al := core.AlertConfig{Enabled: true, On: core.AlertOnCross, Sound: "pr", Trigger: &core.Filter{Field: field, Cmp: core.CmpCrossAbove, Value: 1.0}}
	return goldDef("gold-probe"+string(rune('0'+k)), f, goldSort(field+":"+dir), 4, goldCols(field), al)
}
