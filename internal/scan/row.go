package scan

import (
	"slices"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// defaultColumns are streamed when a scan specifies none.
var defaultColumns = []string{
	core.FieldLast, core.FieldChg5m, core.FieldChgDay,
	core.FieldVol, core.FieldRvol, core.FieldNews,
}

func columnsOf(def core.ScanDef) []string {
	if len(def.Columns) == 0 {
		return defaultColumns
	}
	return def.Columns
}

// sideBoxFields ride along with EVERY scan's tick rows, whatever its columns:
// the charts window's gainers/decliners boxes (web SideBoxes) show them for
// whichever scan feeds a box, and a box must not depend on that scan's column
// choice. The grids render only the scan's own columns, so the extras never
// show up there. Keep in step with SideBoxes' columns and metrics.
var sideBoxFields = []string{
	core.FieldLast, core.FieldVol, core.FieldChgDay, core.FieldChg5m, core.FieldPmVolX,
}

// streamColumnsOf is what a scan's tick rows carry: its columns plus any
// sideBoxFields they lack.
func streamColumnsOf(def core.ScanDef) []string {
	cols := columnsOf(def)
	out := make([]string, len(cols), len(cols)+len(sideBoxFields))
	copy(out, cols)
	for _, f := range sideBoxFields {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// fieldValue returns the JSON-ready value for a field: a number, bool, string,
// or nil for null/unset.
func fieldValue(r *core.Row, f string) any {
	switch f {
	case core.FieldNews:
		return r.News
	case core.FieldHalted:
		return r.Halted
	case core.FieldSession:
		return r.Sess.String()
	case core.FieldSecType:
		return r.SecType
	case core.FieldEpEarnHL:
		if r.EpEarnHL == "" {
			return nil
		}
		return r.EpEarnHL
	case core.FieldNewsAge:
		if r.NewsAge >= 0 {
			return r.NewsAge
		}
		return nil
	case core.FieldHits:
		return r.Hits
	case core.FieldDTrend:
		if core.IsNull(r.DTrend) {
			return nil
		}
		return int(r.DTrend)
	// flagState (0/1/2) and flagBars (a bar count) are discrete, so they go on
	// the wire as ints like dtrend/hits do, even though the Row carries them as
	// float64 for fieldNum's sake. The other five flag fields are genuine
	// percentages/ratios and need no case — the default branch below already
	// does the Null -> nil mapping for them (chg20d rides the same path).
	case core.FieldFlagState:
		if core.IsNull(r.FlagState) {
			return nil
		}
		return int(r.FlagState)
	case core.FieldFlagBars:
		if core.IsNull(r.FlagBars) {
			return nil
		}
		return int(r.FlagBars)
	// The intraday set mirrors the daily one: iflagState and iflagBars are
	// discrete and go on the wire as ints; the other five iflag fields ride
	// the default Null -> nil branch below.
	case core.FieldIFlagState:
		if core.IsNull(r.IFlagState) {
			return nil
		}
		return int(r.IFlagState)
	case core.FieldIFlagBars:
		if core.IsNull(r.IFlagBars) {
			return nil
		}
		return int(r.IFlagBars)
	// The 9EMA-flag scan's e9State (0..4) and e9FlagBars (a bar count) are
	// discrete too, so they ride the wire as ints -- the UI's badge renderer
	// switches on the integer. e9Dist/e9Trigger/e9Stop/e9Age (and the SLAM's
	// e9SlamAge/e9SlamPct) are genuine floats/second counts and take the
	// default Null -> nil branch below.
	case core.FieldE9State:
		if core.IsNull(r.E9State) {
			return nil
		}
		return int(r.E9State)
	case core.FieldE9FlagBars:
		if core.IsNull(r.E9FlagBars) {
			return nil
		}
		return int(r.E9FlagBars)
	// The two signal states (dildoPbState 0..5, climaxState 1..4) are discrete
	// like e9State and ride the wire as ints for the badge renderers. The other
	// dildo*/climax* fields are ages, prices, % and multiples: the default
	// branch's Null -> nil mapping.
	case core.FieldDildoPbState:
		if core.IsNull(r.DildoPbState) {
			return nil
		}
		return int(r.DildoPbState)
	case core.FieldVbState:
		if core.IsNull(r.VbState) {
			return nil
		}
		return int(r.VbState)
	case core.FieldClimaxState:
		if core.IsNull(r.ClimaxState) {
			return nil
		}
		return int(r.ClimaxState)
	case core.FieldBbsState:
		if core.IsNull(r.BbsState) {
			return nil
		}
		return int(r.BbsState)
	case core.FieldDbType:
		switch r.DbType {
		case 1:
			return "14D HIGH"
		case 2:
			return "PIVOT"
		case 3:
			return "PDH"
		}
		return nil
	case core.FieldShortable:
		return r.ShortAvail != nil && *r.ShortAvail > 0
	case core.FieldShortAvail:
		if r.ShortAvail != nil {
			// Capped rows are sent negative-flagged via ShortCapped on the row
			// itself; the wire value stays the numeric floor so sorting works.
			return *r.ShortAvail
		}
		return nil
	case core.FieldShortFee:
		if r.ShortAvail != nil {
			return r.ShortFee
		}
		return nil
	// Frozen prior-session vols are counts and go on the wire as int64 like
	// float/shsOut do (0 = that session hasn't completed today / no trades).
	// The twelve frozen floats (preChg/preHigh/preLow/preVwap/preGap/preFade
	// and the rth six) need no case — the default branch below already does
	// the Null -> nil mapping for them, exactly like sessHigh/sessVwap.
	case core.FieldPreVol:
		return r.PreVol
	case core.FieldRthVol:
		return r.RthVol
	case core.FieldFloat:
		if r.Float != nil {
			return *r.Float
		}
		return nil
	case core.FieldShsOut:
		if r.ShsOut != nil {
			return *r.ShsOut
		}
		return nil
	default:
		v := fieldNum(r, f)
		if core.IsNull(v) {
			return nil
		}
		return v
	}
}

// chartColumn is the scan column that turns on the inline candlestick (the UI's
// "chart" column); it is the only reader of a row's bars.
const chartColumn = "chart"

// rowJSON projects a row into a wire RowJSON over the given columns. The inline
// chart series (bars) travels only with scans that show the chart column: 24
// bars are ~1.5 KB of JSON per row — several times the rest of the row — and
// nothing else on the wire reads them.
func rowJSON(r *core.Row, cols []string) core.RowJSON {
	m := make(core.RowJSON, len(cols)+1)
	for _, c := range cols {
		m[c] = fieldValue(r, c)
	}
	if len(r.Bars) > 0 && slices.Contains(cols, chartColumn) {
		m["bars"] = r.Bars
	}
	// dildoEarly rides along with every scan that shows the green dildo
	// (dildoAge): the alert's speech ("early green dildo") and the EARLY badge
	// read it. 1 early / 0 not; absent (null) when unknown.
	if slices.Contains(cols, core.FieldDildoAge) && !core.IsNull(r.DildoEarly) {
		m[core.FieldDildoEarly] = int(r.DildoEarly)
	}
	// ignFresh rides along with every scan that shows the ignition (ignAge):
	// the speech ("fresh ignition") and the FRESH badge read it.
	if slices.Contains(cols, core.FieldIgnAge) && !core.IsNull(r.IgnFresh) {
		m[core.FieldIgnFresh] = int(r.IgnFresh)
	}
	// The ">10000000" cap can't ride on shortAvail itself without breaking
	// numeric sorting/filtering, so the flag travels alongside it and the UI
	// renders ">10M".
	if r.ShortCapped {
		if _, ok := m[core.FieldShortAvail]; ok {
			m["shortCapped"] = true
		}
	}
	return m
}

// rowChanged reports whether a row should be re-sent. A price move always counts
// (it drives both the chart and most metrics); "spark" itself is skipped because
// slices aren't comparable.
func rowChanged(prev, cur core.Row, cols []string) bool { return rowChangedP(&prev, &cur, cols) }

// rowChangedP is rowChanged without copying the two rows.
func rowChangedP(prev, cur *core.Row, cols []string) bool {
	if prev.Last != cur.Last {
		return true
	}
	for _, c := range cols {
		if c == "bars" {
			continue
		}
		if fieldValue(prev, c) != fieldValue(cur, c) {
			return true
		}
	}
	return false
}

// sortValue is the orderable value for a field. Numeric fields sort naturally;
// boolean columns (news, halted) sort as 1/0 so "has news" groups to the top on
// a desc sort — clicking those headers used to be a no-op because fieldNum
// returns null for them.
func sortValue(r *core.Row, f string) float64 {
	if b, ok := fieldBool(r, f); ok {
		if b {
			return 1
		}
		return 0
	}
	return fieldNum(r, f)
}

// sortRows orders rows by the scan's sort keys (default chg5m desc), nulls last,
// with a symbol tiebreak for stability.
// defaultSort is the order of a scan that names none.
var defaultSort = []core.SortKey{{Field: core.FieldChg5m, Dir: "desc"}}

func sortRows(rows []core.Row, keys []core.SortKey) {
	if len(keys) == 0 {
		keys = defaultSort
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range keys {
			if k.Field == "sym" {
				if rows[i].Sym == rows[j].Sym {
					continue
				}
				if k.Dir == "asc" {
					return rows[i].Sym < rows[j].Sym
				}
				return rows[i].Sym > rows[j].Sym
			}
			a := sortValue(&rows[i], k.Field)
			b := sortValue(&rows[j], k.Field)
			an, bn := core.IsNull(a), core.IsNull(b)
			if an && bn {
				continue
			}
			if an {
				return false // nulls sort last
			}
			if bn {
				return true
			}
			if a == b {
				continue
			}
			if k.Dir == "asc" {
				return a < b
			}
			return a > b
		}
		return rows[i].Sym < rows[j].Sym
	})
}

// sortEntKeys is how many sort values a sortEnt carries precomputed; a scan
// with more keys reads the rest off the row.
const sortEntKeys = 4

// sortEnt is a matched row with its sort values resolved once (sortValue is
// a string switch per call: computed once per row instead of once per
// comparison).
type sortEnt struct {
	r *core.Row
	k [sortEntKeys]float64
}

// sortEnts orders ents exactly as sortRows orders the rows: the comparison
// ends in a symbol tiebreak and a snapshot's symbols are unique, so it is a
// strict total order and any correct sort, stable or not, gives the same
// result.
func sortEnts(ents []sortEnt, keys []core.SortKey) {
	if len(keys) == 0 {
		keys = defaultSort
	}
	if len(ents) < 2 {
		return
	}
	for i := range ents {
		e := &ents[i]
		for k := 0; k < len(keys) && k < sortEntKeys; k++ {
			if keys[k].Field != "sym" {
				e.k[k] = sortValue(e.r, keys[k].Field)
			}
		}
	}
	slices.SortFunc(ents, func(a, b sortEnt) int { return cmpEnts(&a, &b, keys) })
}

// cmpEnts is sortRows' comparison as a three-way compare: <0 when a sorts
// first.
func cmpEnts(a, b *sortEnt, keys []core.SortKey) int {
	for i, k := range keys {
		if k.Field == "sym" {
			if a.r.Sym == b.r.Sym {
				continue
			}
			if (a.r.Sym < b.r.Sym) == (k.Dir == "asc") {
				return -1
			}
			return 1
		}
		var x, y float64
		if i < sortEntKeys {
			x, y = a.k[i], b.k[i]
		} else {
			x, y = sortValue(a.r, k.Field), sortValue(b.r, k.Field)
		}
		xn, yn := core.IsNull(x), core.IsNull(y)
		if xn && yn {
			continue
		}
		if xn {
			return 1 // nulls sort last
		}
		if yn {
			return -1
		}
		if x == y {
			continue
		}
		if (x < y) == (k.Dir == "asc") {
			return -1
		}
		return 1
	}
	switch {
	case a.r.Sym < b.r.Sym:
		return -1
	case a.r.Sym > b.r.Sym:
		return 1
	}
	return 0
}
