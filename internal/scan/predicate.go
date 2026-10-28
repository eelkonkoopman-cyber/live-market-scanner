package scan

import (
	"encoding/json"
	"fmt"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// predicate is a compiled filter: true if a row matches.
type predicate func(*core.Row) bool

// compile turns a ScanDef filter tree into a predicate. An empty filter (zero
// value) matches everything. Numeric comparisons against a null (NaN) metric
// always fail, so a scan filtering on rvol simply shows nothing until the
// baseline is ready.
func compile(f core.Filter) (predicate, error) {
	if f.IsLeaf() {
		return compileLeaf(f)
	}
	subs := make([]predicate, 0, len(f.Conds))
	for _, c := range f.Conds {
		p, err := compile(c)
		if err != nil {
			return nil, err
		}
		subs = append(subs, p)
	}
	switch f.Op {
	case "", "and": // empty op with no conds == match-all
		return func(r *core.Row) bool {
			for _, p := range subs {
				if !p(r) {
					return false
				}
			}
			return true
		}, nil
	case "or":
		return func(r *core.Row) bool {
			for _, p := range subs {
				if p(r) {
					return true
				}
			}
			return false
		}, nil
	default:
		return nil, fmt.Errorf("scan: unknown filter op %q", f.Op)
	}
}

func compileLeaf(f core.Filter) (predicate, error) {
	field := f.Field
	num := numGetter(field)
	switch f.Cmp {
	case core.CmpIsTrue:
		return func(r *core.Row) bool { b, ok := fieldBool(r, field); return ok && b }, nil

	case core.CmpEq:
		if s, ok := f.Value.(string); ok {
			return func(r *core.Row) bool { v, ok := fieldStr(r, field); return ok && v == s }, nil
		}
		x, err := toFloat(f.Value)
		if err != nil {
			return nil, fmt.Errorf("scan: == on %s: %w", field, err)
		}
		return func(r *core.Row) bool { v := num(r); return !core.IsNull(v) && v == x }, nil

	case core.CmpIn:
		strs, nums := toList(f.Value)
		return func(r *core.Row) bool {
			if len(strs) > 0 {
				if v, ok := fieldStr(r, field); ok {
					for _, s := range strs {
						if s == v {
							return true
						}
					}
				}
			}
			if len(nums) > 0 {
				if v := num(r); !core.IsNull(v) {
					for _, n := range nums {
						if n == v {
							return true
						}
					}
				}
			}
			return false
		}, nil

	case core.CmpBetween:
		lo, hi, err := toPair(f.Value)
		if err != nil {
			return nil, fmt.Errorf("scan: between on %s: %w", field, err)
		}
		return func(r *core.Row) bool {
			v := num(r)
			return !core.IsNull(v) && v >= lo && v <= hi
		}, nil

	case core.CmpGT, core.CmpGTE, core.CmpLT, core.CmpLTE:
		x, err := toFloat(f.Value)
		if err != nil {
			return nil, fmt.Errorf("scan: %s on %s: %w", f.Cmp, field, err)
		}
		cmp := f.Cmp
		return func(r *core.Row) bool {
			v := num(r)
			if core.IsNull(v) {
				return false // null never satisfies a numeric comparison
			}
			switch cmp {
			case core.CmpGT:
				return v > x
			case core.CmpGTE:
				return v >= x
			case core.CmpLT:
				return v < x
			default: // CmpLTE
				return v <= x
			}
		}, nil

	default:
		return nil, fmt.Errorf("scan: unknown cmp %q on %s", f.Cmp, field)
	}
}

// fieldNum returns the numeric value of a metric field, or core.Null if the
// field is non-numeric or unset.
// filterUsesHits reports whether any leaf references the per-scan hits field.
func filterUsesHits(f core.Filter) bool {
	if f.IsLeaf() {
		return f.Field == core.FieldHits
	}
	for _, c := range f.Conds {
		if filterUsesHits(c) {
			return true
		}
	}
	return false
}

// filterUsesFlags reports whether any leaf references one of the nightly
// bull-flag fields (FieldFlagScore … FieldFlagBars). Same recursive shape as
// filterUsesHits, and used for the same kind of question: "does this scan's
// filter reference field X".
//
// The answer gates the Claude setup reader (internal/flagread): a scan the
// user built AROUND the flag geometry is one whose entrants are worth a paid
// read, while a plain momentum scan that happens to catch a coiling ticker is
// not. Checking the FILTER — not the row — is what keeps that intent explicit.
func filterUsesFlags(f core.Filter) bool {
	if f.IsLeaf() {
		return isFlagField(f.Field)
	}
	for _, c := range f.Conds {
		if filterUsesFlags(c) {
			return true
		}
	}
	return false
}

// isFlagField reports whether a field name is one of the seven flag metrics.
func isFlagField(field string) bool {
	switch field {
	case core.FieldFlagScore, core.FieldFlagState, core.FieldFlagPole,
		core.FieldFlagRange, core.FieldFlagRetrace, core.FieldFlagVolDry,
		core.FieldFlagBars:
		return true
	default:
		return false
	}
}

// stripHits removes hits leaves from a filter group tree, yielding the BASE
// criteria hits are counted against (see scanState.basePred for why a hits
// condition can't count toward itself).
//
// A nested group that empties out once its hits leaves are gone is PRUNED
// from its parent entirely, not kept as an empty group — an empty "or" group
// compiles to a hard false (compile()'s or-branch, correct default boolean-
// algebra for a genuine empty OR), which would make a hits-only OR branch
// silently force basePred to match nothing forever. Dropping it instead means
// "this branch had nothing left to say," which is the correct reading: the
// branch existed to express a hits condition, and hits isn't part of base.
// The caller (scanner.go upsert) still special-cases a filter that reduces to
// nothing at all (bare hits leaf, or every branch was hits-only) as match-all.
func stripHits(f core.Filter) core.Filter {
	if f.IsLeaf() {
		return f
	}
	out := f
	out.Conds = nil
	for _, c := range f.Conds {
		if c.IsLeaf() && c.Field == core.FieldHits {
			continue
		}
		stripped := stripHits(c)
		if !stripped.IsLeaf() && len(stripped.Conds) == 0 {
			continue // branch had nothing left after stripping; prune, don't propagate as false
		}
		out.Conds = append(out.Conds, stripped)
	}
	return out
}

func fieldNum(r *core.Row, f string) float64 {
	switch f {
	case core.FieldLast:
		return r.Last
	case core.FieldChg1m:
		return r.Chg1m
	case core.FieldChg5m:
		return r.Chg5m
	case core.FieldChg15m:
		return r.Chg15m
	case core.FieldChgDay:
		return r.ChgDay
	case core.FieldGap:
		return r.Gap
	case core.FieldVol:
		return float64(r.Vol)
	case core.FieldRvol:
		return r.Rvol
	case core.FieldAvgVol:
		return r.AvgVol
	case core.FieldVwap:
		return r.Vwap
	case core.FieldVsVwap:
		return r.VsVwap
	case core.FieldHod:
		return r.Hod
	case core.FieldLod:
		return r.Lod
	case core.FieldVsHod:
		return r.VsHod
	case core.FieldBrk5mAge:
		return r.Brk5mAge
	case core.FieldVsPrev5mLow:
		return r.VsPrev5mLow
	case core.FieldPrev5mLow:
		return r.Prev5mLow
	case core.FieldBrk5mCloseAge:
		return r.Brk5mCloseAge
	case core.FieldVsPrev5mClose:
		return r.VsPrev5mClose
	case core.FieldPrev5mClose:
		return r.Prev5mClose
	case core.FieldBrk5mCloseUpAge:
		return r.Brk5mCloseUpAge
	case core.FieldBrkPmhAge:
		return r.BrkPmhAge
	case core.FieldVsPmh:
		return r.VsPmh
	case core.FieldPrevAhHigh:
		return r.PrevAhHigh
	case core.FieldBrkPrevAhAge:
		return r.BrkPrevAhAge
	case core.FieldVsPrevAh:
		return r.VsPrevAh
	case core.FieldBrkPmh5mAge:
		return r.BrkPmh5mAge
	case core.FieldAdv10:
		return r.Adv10
	case core.FieldPmVolX:
		return r.PmVolX
	case core.FieldDbLevel:
		return r.DbLevel
	case core.FieldDbType:
		return r.DbType
	case core.FieldDbPct:
		return r.DbPct
	case core.FieldDbAge:
		return r.DbAge
	case core.FieldPdlLevel:
		return r.PdlLevel
	case core.FieldPdlPct:
		return r.PdlPct
	case core.FieldPdlAge:
		return r.PdlAge
	case core.FieldEpPass:
		return r.EpPass
	case core.FieldEpGap:
		return r.EpGap
	case core.FieldEpVolX:
		return r.EpVolX
	case core.FieldEpEarnAge:
		return r.EpEarnAge
	case core.FieldEp3m:
		return r.Ep3m
	case core.FieldEpAdr:
		return r.EpAdr
	case core.FieldEpAdrDays:
		return r.EpAdrDays
	case core.FieldEpStop:
		return r.EpStop
	case core.FieldEpOrh:
		return r.EpOrh
	case core.FieldEpBrkAge:
		return r.EpBrkAge
	case core.FieldEpRiskPct:
		return r.EpRiskPct
	case core.FieldBrkPrevAh5mAge:
		return r.BrkPrevAh5mAge
	case core.FieldBrkEma9Age:
		return r.BrkEma9Age
	case core.FieldVsEma9:
		return r.VsEma9
	case core.FieldEma9:
		return r.Ema9
	case core.FieldC1mChg:
		return r.C1mChg
	case core.FieldC1mRvol:
		return r.C1mRvol
	case core.FieldChgSess:
		return r.ChgSess
	case core.FieldSessHigh:
		return r.SessHigh
	case core.FieldSessLow:
		return r.SessLow
	case core.FieldSessVol:
		return float64(r.SessVol)
	case core.FieldSessVwap:
		return r.SessVwap
	case core.FieldPreChg:
		return r.PreChg
	case core.FieldPreHigh:
		return r.PreHigh
	case core.FieldPreLow:
		return r.PreLow
	case core.FieldPreVol:
		return float64(r.PreVol)
	case core.FieldPreVolPctAdv:
		return r.PreVolPctAdv
	case core.FieldPreVwap:
		return r.PreVwap
	case core.FieldPreGap:
		return r.PreGap
	case core.FieldPreFade:
		return r.PreFade
	case core.FieldRthChg:
		return r.RthChg
	case core.FieldRthHigh:
		return r.RthHigh
	case core.FieldRthLow:
		return r.RthLow
	case core.FieldRthVol:
		return float64(r.RthVol)
	case core.FieldRthVwap:
		return r.RthVwap
	case core.FieldRthGap:
		return r.RthGap
	case core.FieldRthFade:
		return r.RthFade
	case core.FieldFloat:
		if r.Float != nil {
			return float64(*r.Float)
		}
		return core.Null
	case core.FieldShsOut:
		if r.ShsOut != nil {
			return float64(*r.ShsOut)
		}
		return core.Null
	case core.FieldRot:
		return r.Rot
	case core.FieldHits:
		return float64(r.Hits)
	case core.FieldDTrend:
		return r.DTrend
	case core.FieldChg20d:
		return r.Chg20d
	case core.FieldFlagScore:
		return r.FlagScore
	case core.FieldFlagState:
		return r.FlagState
	case core.FieldFlagPole:
		return r.FlagPole
	case core.FieldFlagRange:
		return r.FlagRange
	case core.FieldFlagRetrace:
		return r.FlagRetrace
	case core.FieldFlagVolDry:
		return r.FlagVolDry
	case core.FieldFlagBars:
		return r.FlagBars
	case core.FieldIFlagScore:
		return r.IFlagScore
	case core.FieldIFlagState:
		return r.IFlagState
	case core.FieldIFlagPole:
		return r.IFlagPole
	case core.FieldIFlagRange:
		return r.IFlagRange
	case core.FieldIFlagRetrace:
		return r.IFlagRetrace
	case core.FieldIFlagVolDry:
		return r.IFlagVolDry
	case core.FieldIFlagBars:
		return r.IFlagBars
	case core.FieldE9State:
		return r.E9State
	case core.FieldE9Dist:
		return r.E9Dist
	case core.FieldE9Trigger:
		return r.E9Trigger
	case core.FieldE9Stop:
		return r.E9Stop
	case core.FieldE9FlagBars:
		return r.E9FlagBars
	case core.FieldE9Age:
		return r.E9Age
	case core.FieldE9SlamAge:
		return r.E9SlamAge
	case core.FieldE9SlamPct:
		return r.E9SlamPct
	case core.FieldE9HodFlagAge:
		return r.E9HodFlagAge
	case core.FieldDildoAge:
		return r.DildoAge
	case core.FieldDildoBody:
		return r.DildoBody
	case core.FieldDildoVolX:
		return r.DildoVolX
	case core.FieldDildoDvol:
		return r.DildoDvol
	case core.FieldDildoEarly:
		return r.DildoEarly
	case core.FieldDildoPbAge:
		return r.DildoPbAge
	case core.FieldDildoPbPx:
		return r.DildoPbPx
	case core.FieldDildoTouchAge:
		return r.DildoTouchAge
	case core.FieldDildoPbState:
		return r.DildoPbState
	case core.FieldClimaxAge:
		return r.ClimaxAge
	case core.FieldClimaxDropPct:
		return r.ClimaxDropPct
	case core.FieldClimaxVolX:
		return r.ClimaxVolX
	case core.FieldClimaxWickPct:
		return r.ClimaxWickPct
	case core.FieldClimaxDvol:
		return r.ClimaxDvol
	case core.FieldClimaxLow:
		return r.ClimaxLow
	case core.FieldClimaxState:
		return r.ClimaxState
	case core.FieldClimaxConfAge:
		return r.ClimaxConfAge
	case core.FieldClimaxConfPx:
		return r.ClimaxConfPx
	case core.FieldBbsAge:
		return r.BbsAge
	case core.FieldBbsSupport:
		return r.BbsSupport
	case core.FieldBbsState:
		return r.BbsState
	case core.FieldBbsDvol:
		return r.BbsDvol
	case core.FieldBbsDistPct:
		return r.BbsDistPct
	case core.FieldPcAge:
		return r.PcAge
	case core.FieldPcBodyPct:
		return r.PcBodyPct
	case core.FieldPcTopPct:
		return r.PcTopPct
	case core.FieldPcBodyX:
		return r.PcBodyX
	case core.FieldPcVolX:
		return r.PcVolX
	case core.FieldPcDvol:
		return r.PcDvol
	case core.FieldPsPass:
		return r.PsPass
	case core.FieldPsFromOpen:
		return r.PsFromOpen
	case core.FieldPsEmaSlope:
		return r.PsEmaSlope
	case core.FieldPsGap:
		return r.PsGap
	case core.FieldPsEntry:
		return r.PsEntry
	case core.FieldPsStop:
		return r.PsStop
	case core.FieldPsTarget:
		return r.PsTarget
	case core.FieldPsAge:
		return r.PsAge
	case core.FieldPsAplus:
		return r.PsAplus
	case core.FieldPsBaseRange:
		return r.PsBaseRange
	case core.FieldEvBullAge:
		return r.EvBullAge
	case core.FieldEvBearAge:
		return r.EvBearAge
	case core.FieldEvEma:
		return r.EvEma
	case core.FieldEvVwap:
		return r.EvVwap
	case core.FieldEvDist:
		return r.EvDist
	case core.FieldEvBarVol:
		return r.EvBarVol
	case core.FieldEvBarDvol:
		return r.EvBarDvol
	case core.FieldEvSessVol:
		return r.EvSessVol
	case core.FieldVbAge:
		return r.VbAge
	case core.FieldVbLevel:
		return r.VbLevel
	case core.FieldVbVolX:
		return r.VbVolX
	case core.FieldVbEma:
		return r.VbEma
	case core.FieldVbState:
		return r.VbState
	case core.FieldVbEntryAge:
		return r.VbEntryAge
	case core.FieldVbEntryPx:
		return r.VbEntryPx
	case core.FieldIgnAge:
		return r.IgnAge
	case core.FieldIgnBodyPct:
		return r.IgnBodyPct
	case core.FieldIgnVolX:
		return r.IgnVolX
	case core.FieldIgnFresh:
		return r.IgnFresh
	case core.FieldIgnPass:
		return r.IgnPass
	case core.FieldNewsAge:
		if r.NewsAge >= 0 {
			return float64(r.NewsAge)
		}
		return core.Null
	case core.FieldShortAvail:
		if r.ShortAvail != nil {
			return float64(*r.ShortAvail)
		}
		return core.Null
	case core.FieldShortFee:
		if r.ShortAvail != nil { // fee is only meaningful alongside a listing
			return r.ShortFee
		}
		return core.Null
	default:
		return core.Null
	}
}

func fieldBool(r *core.Row, f string) (bool, bool) {
	switch f {
	case core.FieldNews:
		return r.News, true
	case core.FieldHalted:
		return r.Halted, true
	case core.FieldShortable:
		// Unlisted at IBKR counts as not shortable, which is the honest answer
		// for a "can I borrow this" filter.
		return r.ShortAvail != nil && *r.ShortAvail > 0, true
	default:
		return false, false
	}
}

func fieldStr(r *core.Row, f string) (string, bool) {
	switch f {
	case core.FieldSession:
		return r.Sess.String(), true
	case core.FieldSecType:
		return r.SecType, true
	case core.FieldEpEarnHL:
		return r.EpEarnHL, true
	default:
		return "", false
	}
}

// maxLastCeiling walks a filter tree for a price ceiling on FieldLast — "<"
// or "<=" directly, or the high end of a "between" range — and returns the
// largest one found anywhere in the tree. Used to prioritize time-sensitive
// external fetches (e.g. RTPR article fetches) toward tickers a "cheap
// stocks" scan cares about; it is a heuristic on the SHAPE of the filter, not
// a claim that a row currently matches every other condition in that scan.
func maxLastCeiling(f core.Filter) (float64, bool) {
	if f.IsLeaf() {
		if f.Field != core.FieldLast {
			return 0, false
		}
		switch f.Cmp {
		case core.CmpLT, core.CmpLTE:
			v, err := toFloat(f.Value)
			if err != nil {
				return 0, false
			}
			return v, true
		case core.CmpBetween:
			_, hi, err := toPair(f.Value)
			if err != nil {
				return 0, false
			}
			return hi, true
		default:
			return 0, false
		}
	}
	found := false
	var best float64
	for _, c := range f.Conds {
		if v, ok := maxLastCeiling(c); ok && (!found || v > best) {
			best, found = v, true
		}
	}
	return best, found
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	default:
		return 0, fmt.Errorf("not a number: %T", v)
	}
}

func toPair(v any) (float64, float64, error) {
	switch arr := v.(type) {
	case []any:
		if len(arr) == 2 {
			lo, e1 := toFloat(arr[0])
			hi, e2 := toFloat(arr[1])
			if e1 == nil && e2 == nil {
				return lo, hi, nil
			}
		}
	case []float64:
		if len(arr) == 2 {
			return arr[0], arr[1], nil
		}
	case []int:
		if len(arr) == 2 {
			return float64(arr[0]), float64(arr[1]), nil
		}
	}
	return 0, 0, fmt.Errorf("between needs [lo,hi]")
}

func toList(v any) (strs []string, nums []float64) {
	switch arr := v.(type) {
	case []any:
		for _, e := range arr {
			if s, ok := e.(string); ok {
				strs = append(strs, s)
				continue
			}
			if f, err := toFloat(e); err == nil {
				nums = append(nums, f)
			}
		}
	case []string:
		strs = arr
	case []float64:
		nums = arr
	}
	return strs, nums
}
