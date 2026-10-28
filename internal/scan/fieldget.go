package scan

import "github.com/eelkonkoopman/marketscanner/internal/core"

// numGetter returns fieldNum(r, f) as an accessor resolved once, so a
// compiled filter leaf does not run fieldNum's string switch for every row
// of every snapshot. Each case is fieldNum's own case body, unchanged; a
// field without a case here falls back to fieldNum itself, so the two can
// only disagree if a body is edited in one place (TestNumGetterMatchesFieldNum).
func numGetter(f string) func(*core.Row) float64 {
	switch f {
	case core.FieldLast:
		return func(r *core.Row) float64 {
			return r.Last
		}
	case core.FieldChg1m:
		return func(r *core.Row) float64 {
			return r.Chg1m
		}
	case core.FieldChg5m:
		return func(r *core.Row) float64 {
			return r.Chg5m
		}
	case core.FieldChg15m:
		return func(r *core.Row) float64 {
			return r.Chg15m
		}
	case core.FieldChgDay:
		return func(r *core.Row) float64 {
			return r.ChgDay
		}
	case core.FieldGap:
		return func(r *core.Row) float64 {
			return r.Gap
		}
	case core.FieldVol:
		return func(r *core.Row) float64 {
			return float64(r.Vol)
		}
	case core.FieldRvol:
		return func(r *core.Row) float64 {
			return r.Rvol
		}
	case core.FieldAvgVol:
		return func(r *core.Row) float64 {
			return r.AvgVol
		}
	case core.FieldVwap:
		return func(r *core.Row) float64 {
			return r.Vwap
		}
	case core.FieldVsVwap:
		return func(r *core.Row) float64 {
			return r.VsVwap
		}
	case core.FieldHod:
		return func(r *core.Row) float64 {
			return r.Hod
		}
	case core.FieldLod:
		return func(r *core.Row) float64 {
			return r.Lod
		}
	case core.FieldVsHod:
		return func(r *core.Row) float64 {
			return r.VsHod
		}
	case core.FieldBrk5mAge:
		return func(r *core.Row) float64 {
			return r.Brk5mAge
		}
	case core.FieldVsPrev5mLow:
		return func(r *core.Row) float64 {
			return r.VsPrev5mLow
		}
	case core.FieldPrev5mLow:
		return func(r *core.Row) float64 {
			return r.Prev5mLow
		}
	case core.FieldBrk5mCloseAge:
		return func(r *core.Row) float64 {
			return r.Brk5mCloseAge
		}
	case core.FieldVsPrev5mClose:
		return func(r *core.Row) float64 {
			return r.VsPrev5mClose
		}
	case core.FieldPrev5mClose:
		return func(r *core.Row) float64 {
			return r.Prev5mClose
		}
	case core.FieldBrk5mCloseUpAge:
		return func(r *core.Row) float64 {
			return r.Brk5mCloseUpAge
		}
	case core.FieldBrkPmhAge:
		return func(r *core.Row) float64 {
			return r.BrkPmhAge
		}
	case core.FieldVsPmh:
		return func(r *core.Row) float64 {
			return r.VsPmh
		}
	case core.FieldPrevAhHigh:
		return func(r *core.Row) float64 {
			return r.PrevAhHigh
		}
	case core.FieldBrkPrevAhAge:
		return func(r *core.Row) float64 {
			return r.BrkPrevAhAge
		}
	case core.FieldVsPrevAh:
		return func(r *core.Row) float64 {
			return r.VsPrevAh
		}
	case core.FieldBrkPmh5mAge:
		return func(r *core.Row) float64 {
			return r.BrkPmh5mAge
		}
	case core.FieldAdv10:
		return func(r *core.Row) float64 {
			return r.Adv10
		}
	case core.FieldPmVolX:
		return func(r *core.Row) float64 {
			return r.PmVolX
		}
	case core.FieldDbLevel:
		return func(r *core.Row) float64 {
			return r.DbLevel
		}
	case core.FieldDbType:
		return func(r *core.Row) float64 {
			return r.DbType
		}
	case core.FieldDbPct:
		return func(r *core.Row) float64 {
			return r.DbPct
		}
	case core.FieldDbAge:
		return func(r *core.Row) float64 {
			return r.DbAge
		}
	case core.FieldPdlLevel:
		return func(r *core.Row) float64 {
			return r.PdlLevel
		}
	case core.FieldPdlPct:
		return func(r *core.Row) float64 {
			return r.PdlPct
		}
	case core.FieldPdlAge:
		return func(r *core.Row) float64 {
			return r.PdlAge
		}
	case core.FieldEpPass:
		return func(r *core.Row) float64 {
			return r.EpPass
		}
	case core.FieldEpGap:
		return func(r *core.Row) float64 {
			return r.EpGap
		}
	case core.FieldEpVolX:
		return func(r *core.Row) float64 {
			return r.EpVolX
		}
	case core.FieldEpEarnAge:
		return func(r *core.Row) float64 {
			return r.EpEarnAge
		}
	case core.FieldEp3m:
		return func(r *core.Row) float64 {
			return r.Ep3m
		}
	case core.FieldEpAdr:
		return func(r *core.Row) float64 {
			return r.EpAdr
		}
	case core.FieldEpAdrDays:
		return func(r *core.Row) float64 {
			return r.EpAdrDays
		}
	case core.FieldEpStop:
		return func(r *core.Row) float64 {
			return r.EpStop
		}
	case core.FieldEpOrh:
		return func(r *core.Row) float64 {
			return r.EpOrh
		}
	case core.FieldEpBrkAge:
		return func(r *core.Row) float64 {
			return r.EpBrkAge
		}
	case core.FieldEpRiskPct:
		return func(r *core.Row) float64 {
			return r.EpRiskPct
		}
	case core.FieldBrkPrevAh5mAge:
		return func(r *core.Row) float64 {
			return r.BrkPrevAh5mAge
		}
	case core.FieldBrkEma9Age:
		return func(r *core.Row) float64 {
			return r.BrkEma9Age
		}
	case core.FieldVsEma9:
		return func(r *core.Row) float64 {
			return r.VsEma9
		}
	case core.FieldEma9:
		return func(r *core.Row) float64 {
			return r.Ema9
		}
	case core.FieldC1mChg:
		return func(r *core.Row) float64 {
			return r.C1mChg
		}
	case core.FieldC1mRvol:
		return func(r *core.Row) float64 {
			return r.C1mRvol
		}
	case core.FieldChgSess:
		return func(r *core.Row) float64 {
			return r.ChgSess
		}
	case core.FieldSessHigh:
		return func(r *core.Row) float64 {
			return r.SessHigh
		}
	case core.FieldSessLow:
		return func(r *core.Row) float64 {
			return r.SessLow
		}
	case core.FieldSessVol:
		return func(r *core.Row) float64 {
			return float64(r.SessVol)
		}
	case core.FieldSessVwap:
		return func(r *core.Row) float64 {
			return r.SessVwap
		}
	case core.FieldPreChg:
		return func(r *core.Row) float64 {
			return r.PreChg
		}
	case core.FieldPreHigh:
		return func(r *core.Row) float64 {
			return r.PreHigh
		}
	case core.FieldPreLow:
		return func(r *core.Row) float64 {
			return r.PreLow
		}
	case core.FieldPreVol:
		return func(r *core.Row) float64 {
			return float64(r.PreVol)
		}
	case core.FieldPreVolPctAdv:
		return func(r *core.Row) float64 {
			return r.PreVolPctAdv
		}
	case core.FieldPreVwap:
		return func(r *core.Row) float64 {
			return r.PreVwap
		}
	case core.FieldPreGap:
		return func(r *core.Row) float64 {
			return r.PreGap
		}
	case core.FieldPreFade:
		return func(r *core.Row) float64 {
			return r.PreFade
		}
	case core.FieldRthChg:
		return func(r *core.Row) float64 {
			return r.RthChg
		}
	case core.FieldRthHigh:
		return func(r *core.Row) float64 {
			return r.RthHigh
		}
	case core.FieldRthLow:
		return func(r *core.Row) float64 {
			return r.RthLow
		}
	case core.FieldRthVol:
		return func(r *core.Row) float64 {
			return float64(r.RthVol)
		}
	case core.FieldRthVwap:
		return func(r *core.Row) float64 {
			return r.RthVwap
		}
	case core.FieldRthGap:
		return func(r *core.Row) float64 {
			return r.RthGap
		}
	case core.FieldRthFade:
		return func(r *core.Row) float64 {
			return r.RthFade
		}
	case core.FieldFloat:
		return func(r *core.Row) float64 {
			if r.Float != nil {
				return float64(*r.Float)
			}
			return core.Null
		}
	case core.FieldShsOut:
		return func(r *core.Row) float64 {
			if r.ShsOut != nil {
				return float64(*r.ShsOut)
			}
			return core.Null
		}
	case core.FieldRot:
		return func(r *core.Row) float64 {
			return r.Rot
		}
	case core.FieldHits:
		return func(r *core.Row) float64 {
			return float64(r.Hits)
		}
	case core.FieldDTrend:
		return func(r *core.Row) float64 {
			return r.DTrend
		}
	case core.FieldChg20d:
		return func(r *core.Row) float64 {
			return r.Chg20d
		}
	case core.FieldFlagScore:
		return func(r *core.Row) float64 {
			return r.FlagScore
		}
	case core.FieldFlagState:
		return func(r *core.Row) float64 {
			return r.FlagState
		}
	case core.FieldFlagPole:
		return func(r *core.Row) float64 {
			return r.FlagPole
		}
	case core.FieldFlagRange:
		return func(r *core.Row) float64 {
			return r.FlagRange
		}
	case core.FieldFlagRetrace:
		return func(r *core.Row) float64 {
			return r.FlagRetrace
		}
	case core.FieldFlagVolDry:
		return func(r *core.Row) float64 {
			return r.FlagVolDry
		}
	case core.FieldFlagBars:
		return func(r *core.Row) float64 {
			return r.FlagBars
		}
	case core.FieldIFlagScore:
		return func(r *core.Row) float64 {
			return r.IFlagScore
		}
	case core.FieldIFlagState:
		return func(r *core.Row) float64 {
			return r.IFlagState
		}
	case core.FieldIFlagPole:
		return func(r *core.Row) float64 {
			return r.IFlagPole
		}
	case core.FieldIFlagRange:
		return func(r *core.Row) float64 {
			return r.IFlagRange
		}
	case core.FieldIFlagRetrace:
		return func(r *core.Row) float64 {
			return r.IFlagRetrace
		}
	case core.FieldIFlagVolDry:
		return func(r *core.Row) float64 {
			return r.IFlagVolDry
		}
	case core.FieldIFlagBars:
		return func(r *core.Row) float64 {
			return r.IFlagBars
		}
	case core.FieldE9State:
		return func(r *core.Row) float64 {
			return r.E9State
		}
	case core.FieldE9Dist:
		return func(r *core.Row) float64 {
			return r.E9Dist
		}
	case core.FieldE9Trigger:
		return func(r *core.Row) float64 {
			return r.E9Trigger
		}
	case core.FieldE9Stop:
		return func(r *core.Row) float64 {
			return r.E9Stop
		}
	case core.FieldE9FlagBars:
		return func(r *core.Row) float64 {
			return r.E9FlagBars
		}
	case core.FieldE9Age:
		return func(r *core.Row) float64 {
			return r.E9Age
		}
	case core.FieldE9SlamAge:
		return func(r *core.Row) float64 {
			return r.E9SlamAge
		}
	case core.FieldE9SlamPct:
		return func(r *core.Row) float64 {
			return r.E9SlamPct
		}
	case core.FieldE9HodFlagAge:
		return func(r *core.Row) float64 {
			return r.E9HodFlagAge
		}
	case core.FieldDildoAge:
		return func(r *core.Row) float64 {
			return r.DildoAge
		}
	case core.FieldDildoBody:
		return func(r *core.Row) float64 {
			return r.DildoBody
		}
	case core.FieldDildoVolX:
		return func(r *core.Row) float64 {
			return r.DildoVolX
		}
	case core.FieldDildoDvol:
		return func(r *core.Row) float64 {
			return r.DildoDvol
		}
	case core.FieldDildoEarly:
		return func(r *core.Row) float64 {
			return r.DildoEarly
		}
	case core.FieldDildoPbAge:
		return func(r *core.Row) float64 {
			return r.DildoPbAge
		}
	case core.FieldDildoPbPx:
		return func(r *core.Row) float64 {
			return r.DildoPbPx
		}
	case core.FieldDildoTouchAge:
		return func(r *core.Row) float64 {
			return r.DildoTouchAge
		}
	case core.FieldDildoPbState:
		return func(r *core.Row) float64 {
			return r.DildoPbState
		}
	case core.FieldClimaxAge:
		return func(r *core.Row) float64 {
			return r.ClimaxAge
		}
	case core.FieldClimaxDropPct:
		return func(r *core.Row) float64 {
			return r.ClimaxDropPct
		}
	case core.FieldClimaxVolX:
		return func(r *core.Row) float64 {
			return r.ClimaxVolX
		}
	case core.FieldClimaxWickPct:
		return func(r *core.Row) float64 {
			return r.ClimaxWickPct
		}
	case core.FieldClimaxDvol:
		return func(r *core.Row) float64 {
			return r.ClimaxDvol
		}
	case core.FieldClimaxLow:
		return func(r *core.Row) float64 {
			return r.ClimaxLow
		}
	case core.FieldClimaxState:
		return func(r *core.Row) float64 {
			return r.ClimaxState
		}
	case core.FieldClimaxConfAge:
		return func(r *core.Row) float64 {
			return r.ClimaxConfAge
		}
	case core.FieldClimaxConfPx:
		return func(r *core.Row) float64 {
			return r.ClimaxConfPx
		}
	case core.FieldBbsAge:
		return func(r *core.Row) float64 {
			return r.BbsAge
		}
	case core.FieldBbsSupport:
		return func(r *core.Row) float64 {
			return r.BbsSupport
		}
	case core.FieldBbsState:
		return func(r *core.Row) float64 {
			return r.BbsState
		}
	case core.FieldBbsDvol:
		return func(r *core.Row) float64 {
			return r.BbsDvol
		}
	case core.FieldBbsDistPct:
		return func(r *core.Row) float64 {
			return r.BbsDistPct
		}
	case core.FieldPcAge:
		return func(r *core.Row) float64 {
			return r.PcAge
		}
	case core.FieldPcBodyPct:
		return func(r *core.Row) float64 {
			return r.PcBodyPct
		}
	case core.FieldPcTopPct:
		return func(r *core.Row) float64 {
			return r.PcTopPct
		}
	case core.FieldPcBodyX:
		return func(r *core.Row) float64 {
			return r.PcBodyX
		}
	case core.FieldPcVolX:
		return func(r *core.Row) float64 {
			return r.PcVolX
		}
	case core.FieldPcDvol:
		return func(r *core.Row) float64 {
			return r.PcDvol
		}
	case core.FieldPsPass:
		return func(r *core.Row) float64 {
			return r.PsPass
		}
	case core.FieldPsFromOpen:
		return func(r *core.Row) float64 {
			return r.PsFromOpen
		}
	case core.FieldPsEmaSlope:
		return func(r *core.Row) float64 {
			return r.PsEmaSlope
		}
	case core.FieldPsGap:
		return func(r *core.Row) float64 {
			return r.PsGap
		}
	case core.FieldPsEntry:
		return func(r *core.Row) float64 {
			return r.PsEntry
		}
	case core.FieldPsStop:
		return func(r *core.Row) float64 {
			return r.PsStop
		}
	case core.FieldPsTarget:
		return func(r *core.Row) float64 {
			return r.PsTarget
		}
	case core.FieldPsAge:
		return func(r *core.Row) float64 {
			return r.PsAge
		}
	case core.FieldPsAplus:
		return func(r *core.Row) float64 {
			return r.PsAplus
		}
	case core.FieldPsBaseRange:
		return func(r *core.Row) float64 {
			return r.PsBaseRange
		}
	case core.FieldEvBullAge:
		return func(r *core.Row) float64 {
			return r.EvBullAge
		}
	case core.FieldEvBearAge:
		return func(r *core.Row) float64 {
			return r.EvBearAge
		}
	case core.FieldEvEma:
		return func(r *core.Row) float64 {
			return r.EvEma
		}
	case core.FieldEvVwap:
		return func(r *core.Row) float64 {
			return r.EvVwap
		}
	case core.FieldEvDist:
		return func(r *core.Row) float64 {
			return r.EvDist
		}
	case core.FieldEvBarVol:
		return func(r *core.Row) float64 {
			return r.EvBarVol
		}
	case core.FieldEvBarDvol:
		return func(r *core.Row) float64 {
			return r.EvBarDvol
		}
	case core.FieldEvSessVol:
		return func(r *core.Row) float64 {
			return r.EvSessVol
		}
	case core.FieldVbAge:
		return func(r *core.Row) float64 {
			return r.VbAge
		}
	case core.FieldVbLevel:
		return func(r *core.Row) float64 {
			return r.VbLevel
		}
	case core.FieldVbVolX:
		return func(r *core.Row) float64 {
			return r.VbVolX
		}
	case core.FieldVbEma:
		return func(r *core.Row) float64 {
			return r.VbEma
		}
	case core.FieldVbState:
		return func(r *core.Row) float64 {
			return r.VbState
		}
	case core.FieldVbEntryAge:
		return func(r *core.Row) float64 {
			return r.VbEntryAge
		}
	case core.FieldVbEntryPx:
		return func(r *core.Row) float64 {
			return r.VbEntryPx
		}
	case core.FieldIgnAge:
		return func(r *core.Row) float64 {
			return r.IgnAge
		}
	case core.FieldIgnBodyPct:
		return func(r *core.Row) float64 {
			return r.IgnBodyPct
		}
	case core.FieldIgnVolX:
		return func(r *core.Row) float64 {
			return r.IgnVolX
		}
	case core.FieldIgnFresh:
		return func(r *core.Row) float64 {
			return r.IgnFresh
		}
	case core.FieldIgnPass:
		return func(r *core.Row) float64 {
			return r.IgnPass
		}
	case core.FieldNewsAge:
		return func(r *core.Row) float64 {
			if r.NewsAge >= 0 {
				return float64(r.NewsAge)
			}
			return core.Null
		}
	case core.FieldShortAvail:
		return func(r *core.Row) float64 {
			if r.ShortAvail != nil {
				return float64(*r.ShortAvail)
			}
			return core.Null
		}
	case core.FieldShortFee:
		return func(r *core.Row) float64 {
			if r.ShortAvail != nil { // fee is only meaningful alongside a listing
				return r.ShortFee
			}
			return core.Null
		}
	}
	return func(r *core.Row) float64 { return fieldNum(r, f) }
}
