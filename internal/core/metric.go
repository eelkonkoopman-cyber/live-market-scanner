package core

import "math"

// Session is the current trading session in ET.
type Session uint8

const (
	SessionClosed  Session = iota
	SessionPre             // 04:00–09:30 ET
	SessionRegular         // 09:30–16:00 ET
	SessionPost            // 16:00–20:00 ET
)

func (s Session) String() string {
	switch s {
	case SessionPre:
		return "pre"
	case SessionRegular:
		return "regular"
	case SessionPost:
		return "post"
	default:
		return "closed"
	}
}

// ParseSession is String's inverse: it maps a wire/DB session label back to the
// Session value. ok=false for anything that isn't exactly one of the four
// labels ("closed" included — it parses, even though no snapshot is ever
// stored for it, so a caller can round-trip any String() output).
func ParseSession(s string) (Session, bool) {
	switch s {
	case "pre":
		return SessionPre, true
	case "regular":
		return SessionRegular, true
	case "post":
		return SessionPost, true
	case "closed":
		return SessionClosed, true
	default:
		return SessionClosed, false
	}
}

// Null is the sentinel for "no value" in the float metric fields of Row. It is a
// quiet NaN; use IsNull to test. The WS layer encodes it as JSON null, and any
// numeric scan predicate (>, <, between, …) is defined to fail against it, so a
// scan that filters on rvol simply excludes tickers whose baseline isn't ready.
var Null = math.NaN()

// IsNull reports whether a metric float is the null sentinel.
func IsNull(f float64) bool { return math.IsNaN(f) }

// Canonical metric field names — the single source of truth referenced by scan
// filters, sort keys, and column lists.
const (
	FieldLast   = "last"
	FieldChg1m  = "chg1m"
	FieldChg5m  = "chg5m"
	FieldChg15m = "chg15m"
	FieldChgDay = "chgDay"
	FieldGap    = "gap"
	FieldVol    = "vol"
	FieldRvol   = "rvol"
	// FieldAvgVol is the trailing 20-session average REGULAR-SESSION
	// (09:30-16:00) volume — the conventional "average daily volume" most
	// scanners mean by that term. Derived from the SAME per-bucket cumulative
	// RVOL baseline (no new data collection): the trailing-mean cumulative
	// volume at 16:00 minus the trailing-mean cumulative volume at 09:30.
	// Deliberately EXCLUDES pre/post-market volume, so FieldPreVolPctAdv below
	// (pre-market volume as a % of this) isn't self-referential against a
	// baseline that itself partly consists of past pre-market volume. Null
	// until the baseline has enough trailing sessions (same gate as Rvol).
	FieldAvgVol = "avgVol"
	FieldVwap   = "vwap"
	FieldVsVwap = "vsVwap"
	FieldHod    = "hod"
	FieldLod    = "lod"
	FieldVsHod  = "vsHod"

	// 5-minute candle structure. Candles are clock-aligned (09:30, 09:35, …) and
	// built from each aggregate's OWN start time, so a print is never attributed
	// to the wrong candle at a boundary. "Previous candle" follows chart
	// convention: the last completed 5-min candle that had trades.
	FieldBrk5mAge    = "brk5mAge"    // seconds since the current 5-min candle traded below the previous candle's low; Null = not broken
	FieldVsPrev5mLow = "vsPrev5mLow" // % of last vs the previous 5-min candle's low (negative = below it)
	FieldPrev5mLow   = "prev5mLow"   // the previous 5-min candle's low, $

	// Close-based variant: the same break, measured against the previous
	// candle's CLOSE (its last print) instead of its low.
	FieldBrk5mCloseAge = "brk5mCloseAge" // seconds since the current 5-min candle traded below the previous candle's close; Null = not broken
	FieldVsPrev5mClose = "vsPrev5mClose" // % of last vs the previous 5-min candle's close (negative = below it)
	FieldPrev5mClose   = "prev5mClose"   // the previous 5-min candle's close, $

	// Long side of the close break: the current 5-min candle traded strictly
	// ABOVE the previous candle's close. vsPrev5mClose / prev5mClose serve both
	// sides (positive vs = above).
	// Level breaks. A break is the first trade ABOVE the level since price was
	// last at or below it (a reclaim after a failed breakout counts again).
	// Pre-market high: today's 04:00-09:30 high, live from 09:30 and only when
	// complete. Previous after-hours high: the previous trading day's
	// 16:00-20:00 ET high, seeded from Massive minute bars by the prevah loader.
	FieldBrkPmhAge    = "brkPmhAge"    // seconds since the latest break above today's pre-market high; Null = none
	FieldVsPmh        = "vsPmh"        // % of last vs today's pre-market high (positive = above)
	FieldPrevAhHigh   = "prevAhHigh"   // previous trading day's after-hours high, $
	FieldBrkPrevAhAge = "brkPrevAhAge" // seconds since the latest break above the previous after-hours high; Null = none
	FieldVsPrevAh     = "vsPrevAh"     // % of last vs the previous after-hours high (positive = above)
	// The same breaks on the 5-minute timeframe: a 5-minute candle CLOSES above
	// the level (a wick that closes back under does not count).
	// Pre-market volume vs the 10-day average DAILY volume (Massive daily bars,
	// which include extended hours). pmVolX = pre-market volume so far / adv10:
	// live during pre-market (the day volume, re-synced from the snapshot so a
	// restart loses nothing), frozen at 09:30 when that freeze is complete.
	FieldAdv10  = "adv10"  // average daily volume over the last 10 trading days, shares
	FieldPmVolX = "pmVolX" // pre-market volume so far as a multiple of adv10

	FieldBrkPmh5mAge = "brkPmh5mAge" // seconds since a 5-min candle closed above today's pre-market high; Null = none

	// Daily level breakout (internal/dlevel): the highest daily level (14-day high, swing-high pivot, previous day high) that today's trading crossed and last is still above. All Null when none.
	FieldDbLevel = "dbLevel" // the level price, $
	FieldDbType  = "dbType"  // "14D HIGH" / "PIVOT" / "PDH" on the wire; sorts by rank 1/2/3 (14D HIGH first)
	FieldDbPct   = "dbPct"   // % of last above the level
	FieldDbAge   = "dbAge"   // seconds since the cross; Null when the moment is unknown

	// Previous-day-low break (internal/dlevel LowTracker): price traded at/above the previous day's low and crossed below it today, still below. All Null when none.
	FieldPdlLevel = "pdlLevel" // the previous day's low, $
	FieldPdlPct   = "pdlPct"   // % of last BELOW the PDL (positive)
	FieldPdlAge   = "pdlAge"   // seconds since the break; Null when the moment is unknown

	// Episodic Pivot (internal/ep; docs/episodic-pivot.md). Scanner-side overlay; all Null when the symbol is not a >=10% gapper.
	FieldEpPass         = "epPass"         // 1 = every EP Watch rule passes (04:00-11:00 ET), else Null
	FieldEpGap          = "epGap"          // gap vs previous close, % (pre-market: last; after the open: the open)
	FieldEpVolX         = "epVolX"         // volume so far / average daily volume (available days)
	FieldEpEarnAge      = "epEarnAge"      // seconds since the matched earnings headline (published since the previous 16:00 ET)
	FieldEpEarnHL       = "epEarnHL"       // that headline (string)
	FieldEp3m           = "ep3m"           // % change of the previous close over ~63 sessions
	FieldEpAdr          = "epAdr"          // ADR20, % (of the available days when fewer than 20)
	FieldEpAdrDays      = "epAdrDays"      // daily bars the ADR used
	FieldEpStop         = "epStop"         // planned stop: open-or-last - 1 x ADR20, $
	FieldEpOrh          = "epOrh"          // opening-range high (09:30-09:35), $; Null before 09:35 or when the scanner missed it
	FieldEpBrkAge       = "epBrkAge"       // seconds since the first break above ORH after 09:35 (EP Break); Null = none
	FieldEpRiskPct      = "epRiskPct"      // distance from last to the stop, % of last
	FieldBrkPrevAh5mAge = "brkPrevAh5mAge" // seconds since a 5-min candle closed above the previous after-hours high; Null = none

	// 1-minute 9 EMA, the same EMA the charts window draws on 1m cells. The
	// event is a 1-minute candle CLOSING below the EMA right after one closed at
	// or above it (losing the 9 EMA). Null until the scanner has folded enough
	// candles that its EMA agrees with the chart's (see engine emaWarm).
	FieldBrkEma9Age = "brkEma9Age" // seconds since a 1-min candle closed under the 1-min 9 EMA; Null = none
	FieldVsEma9     = "vsEma9"     // % of last vs the 1-min 9 EMA (positive = above)
	FieldEma9       = "ema9"       // the 1-min 9 EMA, $

	// 1-minute candle spike. Both describe the SAME displayed candle: the live
	// forming 1-minute candle, or the one that just closed (for the next minute)
	// while it is the stronger of the two, so an AND filter over them is coherent.
	FieldC1mChg  = "c1mChg"  // % of the displayed 1-min candle's close vs the previous traded candle's close
	FieldC1mRvol = "c1mRvol" // that candle's volume / the mean volume of the 9 minutes before it (empty minutes = 0); Null until 9 minutes were observed

	FieldBrk5mCloseUpAge = "brk5mCloseUpAge" // seconds since the current 5-min candle traded above the previous candle's close; Null = not broken

	// Session-scoped metrics: measured within the *current* sub-session (pre /
	// regular / post) and reset at each 04:00 → 09:30 → 16:00 boundary, unlike the
	// fields above which anchor to the full extended session at 04:00 ET.
	FieldChgSess  = "chgSess"  // % change vs session-open price
	FieldSessHigh = "sessHigh" // high within the session
	FieldSessLow  = "sessLow"  // low within the session
	FieldSessVol  = "sessVol"  // volume within the session
	FieldSessVwap = "sessVwap" // VWAP within the session

	// Frozen prior-session metrics: the OUTGOING sub-session's scoped values,
	// captured once at the 09:30 (pre*) and 16:00 (rth*) ET boundaries so
	// "what moved in the pre-market" stays reviewable all day after the live
	// session-scoped fields above reset. *Chg is % vs the PREVIOUS CLOSE at the
	// moment the session ended (a chgDay snapshot at the boundary), NOT vs the
	// session's first print. Null (floats) / 0 (vols) until that session has
	// completed today. Memory-only: a restart after the boundary loses them.
	FieldPreChg  = "preChg"  // % vs prev close when pre-market ended (09:30)
	FieldPreHigh = "preHigh" // pre-market session high
	FieldPreLow  = "preLow"  // pre-market session low
	FieldPreVol  = "preVol"  // pre-market session volume
	// FieldPreVolPctAdv is pre-market volume so far as a % of FieldAvgVol —
	// unlike FieldPreVol (frozen at 09:30, 0 all through pre-market itself),
	// this one is LIVE during the pre-market session (today's session-scoped
	// volume so far, the same counter that later becomes the frozen PreVol)
	// so it's actually usable in the exact window it exists for, then holds
	// its final value once pre-market ends. Null when AvgVol isn't known yet.
	FieldPreVolPctAdv = "preVolPctAdv"
	FieldPreVwap      = "preVwap" // pre-market session VWAP
	FieldPreGap       = "preGap"  // gap = pre-market session HIGH vs prev close, % (peak gain, not close-vs-prevClose)
	FieldPreFade      = "preFade" // fade = % off the pre-market session high at session end (09:30), clamped >= 0; off the HIGH, so a single wick print can overstate the tradeable fade
	FieldRthChg       = "rthChg"  // % vs prev close when the regular session ended (16:00)
	FieldRthHigh      = "rthHigh" // regular-session high
	FieldRthLow       = "rthLow"  // regular-session low
	FieldRthVol       = "rthVol"  // regular-session volume
	FieldRthVwap      = "rthVwap" // regular-session VWAP
	FieldRthGap       = "rthGap"  // gap = regular-session HIGH vs prev close, % (peak gain)
	FieldRthFade      = "rthFade" // fade = % off the regular-session high at session end (16:00), clamped >= 0; off the HIGH, so a single wick print can overstate the tradeable fade

	FieldFloat   = "float"
	FieldShsOut  = "shsOut"
	FieldRot     = "rot"
	FieldNews    = "news"
	FieldNewsAge = "newsAge"
	FieldHits    = "hits" // per-scan re-entry count today (recurring movers)

	// Daily-chart trend context.
	FieldDTrend = "dtrend" // 1 up / 0 flat / -1 down / Null unknown
	FieldChg20d = "chg20d" // % change over ~20 trading days

	// Bull-flag / tight-consolidation setup, from the nightly pattern pass (same
	// cadence as the trend fields above). Null when the symbol has no pattern.
	FieldFlagScore   = "flagScore"   // 0-100 composite setup quality
	FieldFlagState   = "flagState"   // 0 none / 1 coiling / 2 breakout (2 assigned live)
	FieldFlagPole    = "flagPole"    // % advance of the pole
	FieldFlagRange   = "flagRange"   // base tightness, (hi-lo)/mid as a %
	FieldFlagRetrace = "flagRetrace" // % of the pole given back
	FieldFlagVolDry  = "flagVolDry"  // base avg volume / pole avg volume (<1 = drying up)
	FieldFlagBars    = "flagBars"    // length of the base, in daily bars

	// Intraday bull-flag / tight-consolidation setup: same semantics as the
	// daily flag block above, but computed over TODAY'S session at 5-minute-
	// bucket resolution by a ~60s worker (not nightly). Null when unclassified;
	// iflagState 2 is stamped live by the overlay when Last clears the stored
	// intraday base high. Separate fields so a daily-flag scan and an
	// intraday-flag scan can run side by side.
	FieldIFlagScore   = "iflagScore"   // 0-100 composite setup quality
	FieldIFlagState   = "iflagState"   // 0 none / 1 coiling / 2 breakout (2 assigned live)
	FieldIFlagPole    = "iflagPole"    // % advance of the pole
	FieldIFlagRange   = "iflagRange"   // base tightness, (hi-lo)/mid as a %
	FieldIFlagRetrace = "iflagRetrace" // % of the pole given back
	FieldIFlagVolDry  = "iflagVolDry"  // base avg volume / pole avg volume (<1 = drying up)
	FieldIFlagBars    = "iflagBars"    // length of the base, in 5-minute buckets

	// 9EMA flag market scan: the Signal window's 1-minute 9 EMA flag state
	// machine (web/src/signals/emaFlag.ts, ported to internal/emaflag) run
	// server-side over every pre-market volume leader (pmVolX >= 1) by the
	// flagscan service, and overlaid onto rows by the scanner. All of them are
	// Null for a symbol that is not currently a candidate (or whose bars have not
	// been evaluated yet) -- Null, never 0, because e9State 0 is a real verdict
	// ("evaluated, no setup") and an unevaluated name must not read as one.
	//
	// e9State is the Signal window's FlagState order: 0 idle / 1 impulse /
	// 2 flat (sideways above the 9 EMA) / 3 ready (EMA within the ready band) /
	// 4 long (the flag high broke once ready: the breakout).
	FieldE9State    = "e9State"    // 0 idle / 1 impulse / 2 flat / 3 ready / 4 long; Null = not a candidate / not evaluated
	FieldE9Dist     = "e9Dist"     // % of the latest 1-min close vs the 1-min 9 EMA (positive = above)
	FieldE9Trigger  = "e9Trigger"  // the entry trigger price, $ (Null when the machine has none yet)
	FieldE9Stop     = "e9Stop"     // the protective stop price, $ (Null when the machine has none yet)
	FieldE9FlagBars = "e9FlagBars" // length of the flag, in 1-minute bars
	FieldE9Age      = "e9Age"      // seconds since the setup entered its current state; Null while idle

	// 9 EMA SLAM, the same engine's one bearish event: a CLOSED 1-minute candle
	// that, after the run-up and the sideways flag (or the position out of it),
	// opens at/above the 9 EMA and closes well under it on a big, high-volume
	// body (emaFlag.ts / internal/emaflag KindSlam). An alert only -- it changes
	// no other e9* field. Both describe the LATEST slam of the symbol's current
	// ET day and are Null when it has none today, and (like every e9* field)
	// for a symbol that is not a candidate -- never 0, which `e9SlamAge <= 300`
	// would read as "just slammed".
	FieldE9SlamAge = "e9SlamAge" // seconds since the latest SLAM's bar began (its start, like e9Age); Null = no slam today / not a candidate
	FieldE9SlamPct = "e9SlamPct" // % the slam bar CLOSED vs its own 9 EMA (negative: under it); Null with e9SlamAge

	// HOD FLAG: the same engine's flag-forming (FLAT) bar of a flag whose run-up
	// made a NEW HIGH OF THE DAY (pre-market included). Seconds since that FLAT
	// bar began; Null = no such flag today / not a candidate -- never 0.
	FieldE9HodFlagAge = "e9HodFlagAge"

	// GREEN DILDO -> 9 EMA PULLBACK (internal/dildopb, a port of
	// web/src/signals/dildoPb.ts): two sequential ALERTS, a "look now" prompt,
	// not a system (no edge after costs in calibration). Alert 1 is the engine's:
	// every closed 1-minute candle of every ticker is judged, and the latest green
	// dildo of TODAY (04:00 ET on) rides on the row as dildoAge..dildoDvol (Null
	// when none today). Alert 2 is flagscan's: the dildo admits the symbol as a
	// HOT name and the pullback to the chart's 9 EMA is judged on the chart's own
	// REST + tap bars, overlaid by the scanner as dildoPbAge..dildoPbState (Null
	// for a symbol flagscan is not evaluating).
	FieldDildoAge  = "dildoAge"  // seconds since the latest green dildo bar BEGAN (first seen ~63 s); Null = none today
	FieldDildoBody = "dildoBody" // that candle's body, % of its open
	FieldDildoVolX = "dildoVolX" // its volume / the mean volume of the up to 10 traded bars before it; Null = infinite (a mean of 0)
	FieldDildoDvol = "dildoDvol" // its dollar volume (close x volume), $
	// FieldDildoEarly: that dildo is an EARLY one (dildopb.JudgeEarly, the
	// auto-trader's rule): 1 early / 0 not early / Null unknown (too few bars
	// seen before it). Not a column: it rides with the rows of every scan that
	// shows dildoAge (scan.rowJSON), for the alert's speech and the EARLY badge.
	FieldDildoEarly    = "dildoEarly"
	FieldDildoPbAge    = "dildoPbAge"    // seconds since the confirming 9 EMA pullback bar BEGAN; Null = none
	FieldDildoPbPx     = "dildoPbPx"     // the pullback bar's close (the entry the calibration measured), $
	FieldDildoTouchAge = "dildoTouchAge" // seconds since the latest bar that touched the 9 EMA band began; Null = none
	FieldDildoPbState  = "dildoPbState"  // the latest dildo's pullback: 0 none / 1 watching / 2 touching / 3 confirmed / 4 cancelled / 5 expired

	// VOLUME CLIMAX -> BOTTOM CONFIRMED (internal/climax, a port of
	// web/src/signals/climax.ts): the fourth signal, two sequential ALERTS, a
	// "look now" prompt, not a system (no edge after costs in calibration). Both
	// are engine-native: every closed traded 1-minute candle of every ticker is
	// judged against the 10 prior traded bars of the same ET day and the judge's
	// own 9 EMA; the latest climax of TODAY (04:00 ET on) and its confirmation
	// latch ride on the row. All Null when the ticker has no climax today.
	FieldClimaxAge     = "climaxAge"     // seconds since the latest climax bar BEGAN (first seen ~63 s); Null = none today
	FieldClimaxDropPct = "climaxDropPct" // its low under the 10 prior bars' highest high, %
	FieldClimaxVolX    = "climaxVolX"    // its volume / the 10 prior bars' mean volume; Null = infinite (a mean of 0)
	FieldClimaxWickPct = "climaxWickPct" // its lower wick, % of its range
	FieldClimaxDvol    = "climaxDvol"    // its dollar volume (close x volume), $
	FieldClimaxLow     = "climaxLow"     // its low: the level that must hold, $
	FieldClimaxState   = "climaxState"   // 1 watching / 2 bottom confirmed / 3 failed (new low first) / 4 expired
	FieldClimaxConfAge = "climaxConfAge" // seconds since the confirming bar BEGAN; Null unless climaxState == 2
	FieldClimaxConfPx  = "climaxConfPx"  // the confirming close, $; Null unless climaxState == 2

	// BOUNCY BALL SHORT (internal/bbs, a port of web/src/signals/bouncyShort.ts):
	// a support touched >= 3 times with lower highs on each bounce, then a
	// closed 1-minute bar breaking it. An ALERT, uncalibrated. Engine-native over
	// the day's last 90 traded bars. All Null when nothing today.
	FieldBbsAge     = "bbsAge"     // seconds since today's latest break bar BEGAN; Null = none
	FieldBbsSupport = "bbsSupport" // the support: the forming base's (state 1) or the latest break's (state 2), $
	FieldBbsState   = "bbsState"   // 1 forming (support + lower highs, close within 1 %) / 2 broken today; Null = neither
	FieldBbsDvol    = "bbsDvol"    // the break bar's dollar volume (close x volume), $
	FieldBbsDistPct = "bbsDistPct" // while forming (state 1): % the last price sits above the support; Null otherwise

	// IGNITION (internal/ignition, a port of web/src/signals/ignition.ts): the
	// small first candle out of a quiet base right before a dildo. An ALERT,
	// uncalibrated. All Null when nothing today.
	// POWER CANDLE (engine, dildo.go): a dildo-rule candle with any top wick.
	FieldPcAge     = "pcAge"     // seconds since today's latest power candle BEGAN; Null = none
	FieldPcBodyPct = "pcBodyPct" // its green body, %
	FieldPcTopPct  = "pcTopPct"  // its top wick, % of the candle's range
	FieldPcBodyX   = "pcBodyX"   // body / the prior bars' mean |body|; Null = infinite
	FieldPcVolX    = "pcVolX"    // volume / the prior bars' mean; Null = infinite
	FieldPcDvol    = "pcDvol"    // close x volume, $

	// POWER SETUP (engine, dildo.go; core/powersetup.go): the three filters and
	// the plan, stamped on EVERY power candle (pass or fail) so the user sees why.
	FieldPsPass      = "psPass"      // 1 = all three filters pass (a power SETUP), 0 = not; Null = no power candle today
	FieldPsFromOpen  = "psFromOpen"  // the power candle's close vs the day's open (first traded 1m bar from 04:00 ET), %; Null = unknown
	FieldPsEmaSlope  = "psEmaSlope"  // the 1m 9 EMA's rise over the last 3 bars at the close, %; Null = not enough bars
	FieldPsGap       = "psGap"       // the gap at the open vs the previous close, %; Null = unknown
	FieldPsEntry     = "psEntry"     // plan: buy limit = the 1m 9 EMA at the close; Null = EMA not seeded
	FieldPsStop      = "psStop"      // plan: entry x 0.94
	FieldPsTarget    = "psTarget"    // plan: entry + 3R
	FieldPsAge       = "psAge"       // seconds since the power candle BEGAN (= pcAge); Null = none today
	FieldPsAplus     = "psAplus"     // 1 = an A+ grade (the 15 bars before the candle span <= 5%), 0 = not; Null = no power candle today
	FieldPsBaseRange = "psBaseRange" // the 15 1m bars before the power candle: (max high - min low) / min low, %; Null = fewer than 3 bars

	FieldIgnAge     = "ignAge"     // seconds since today's latest ignition bar BEGAN; Null = none
	FieldIgnBodyPct = "ignBodyPct" // its green body, %
	FieldIgnVolX    = "ignVolX"    // its volume / the prior 10 bars' mean
	// FieldIgnFresh: that ignition is FRESH - the day change before it (its
	// open vs the previous close) is at most +3% (engine freshMaxDayChgPct):
	// 1 fresh, 0 not, Null unknown (no previous close).
	FieldIgnFresh = "ignFresh"
	// FieldIgnPass: that ignition passes the four skip rules (engine ignSkip:
	// day change before it <= +3%, candle $ volume <= $250k, close >= $1.60,
	// volume multiple <= 50x): 1 passes, 0 skipped; Null when no ignition.
	FieldIgnPass = "ignPass"

	// VOLUME BREAK (internal/volbreak, engine volbreak.go; docs/volume-break.md):
	// a 1m close over a 30+ minute consolidation high (tested twice, <= 12% wide)
	// on 3x+ the range's volume, on a stock up 10%+ with 1M+ shares; then the
	// first pullback to the 1m 9 EMA. An ALERT. All Null when no break today.
	FieldVbAge      = "vbAge"      // seconds since today's latest break bar BEGAN; Null = none
	FieldVbLevel    = "vbLevel"    // the range high that broke, $
	FieldVbVolX     = "vbVolX"     // the break bar's volume / the range's mean 1m volume
	FieldVbEma      = "vbEma"      // the 1m 9 EMA at the break close, $
	FieldVbState    = "vbState"    // 1 waiting for the pullback / 2 9EMA entry / 3 failed (closed under the level) / 4 expired
	FieldVbEntryAge = "vbEntryAge" // seconds since the entry bar BEGAN; Null unless vbState == 2
	FieldVbEntryPx  = "vbEntryPx"  // the 1m 9 EMA at the entry bar, $; Null unless vbState == 2

	// 9EMA VWAP CROSS (engine emavwap.go): the 9 EMA of 1-minute closes crossing
	// the session VWAP (the scanner's sessVwap: pre-market VWAP before 09:30, the
	// regular-hours VWAP from 09:30), judged on each closed 1-minute bar. An
	// ALERT. All Null when nothing today (Age) or before the EMA is warm.
	FieldEvBullAge = "evBullAge" // seconds since the bar that fired the latest BULL cross CLOSED today; Null = none
	FieldEvBearAge = "evBearAge" // seconds since the bar that fired the latest BEAR cross CLOSED today; Null = none
	FieldEvEma     = "evEma"     // the 9 EMA of 1-minute closes after the latest closed 1-minute bar, $
	FieldEvVwap    = "evVwap"    // the session VWAP read at that bar, $
	FieldEvDist    = "evDist"    // (EMA - VWAP) / VWAP at that bar, %; positive = EMA above VWAP
	// The volume behind the latest fired cross (either direction): the cross
	// 1-minute bar's shares and dollar volume (close x volume) and the session's cumulative volume when it closed. Null = no cross today.
	FieldEvBarVol  = "evBarVol"
	FieldEvBarDvol = "evBarDvol"
	FieldEvSessVol = "evSessVol"

	FieldHalted  = "halted"
	FieldSession = "session"
	// FieldSecType is the Massive/Polygon security-type code: "CS" (common
	// stock), "ADRC" (ADR), or "ETF" -- the only three types the scanner's
	// universe tracks at all today (internal/universe.allowed).
	FieldSecType = "secType"

	// IBKR short-sale availability (refreshed every 15 min from the public file).
	FieldShortable  = "shortable"  // bool: IBKR has borrowable shares right now
	FieldShortAvail = "shortAvail" // shares available to borrow
	FieldShortFee   = "shortFee"   // annual borrow cost, %
)

// Row is the canonical, point-in-time metric view of one ticker. Float fields
// use the Null sentinel (NaN) for "not available"; reference counts that may be
// unknown use nil pointers.
type Row struct {
	Sym  string
	Last float64

	Chg1m  float64 // % change, rolling 60s  (Null until warm)
	Chg5m  float64 // % change, rolling 300s (Null until warm) — signature metric
	Chg15m float64 // % change, rolling 900s (Null until warm)
	ChgDay float64 // % vs prev official close
	Gap    float64 // % day open vs prev close

	Vol    int64   // cumulative session volume (04:00-anchored)
	Rvol   float64 // intraday relative volume (Null until baseline ready)
	AvgVol float64 // trailing avg REGULAR-SESSION volume — see FieldAvgVol

	Vwap   float64
	VsVwap float64 // % of Last vs session VWAP

	Hod   float64
	Lod   float64
	VsHod float64 // % below high of day

	// 5-minute candle structure (see FieldBrk5mAge). All Null when there is no
	// usable previous candle yet (first candle of the day, or a candle that was
	// already in progress when the scanner started).
	Brk5mAge    float64 // whole seconds since the current candle broke the previous low; Null = not broken
	VsPrev5mLow float64 // % of Last vs the previous candle's low
	Prev5mLow   float64 // previous candle's low

	// Close-based variant (see FieldBrk5mCloseAge). Unlike the low, a candle
	// in progress at startup still has an exact close, so it IS a reference.
	Brk5mCloseAge   float64 // whole seconds since the current candle broke the previous close; Null = not broken
	VsPrev5mClose   float64 // % of Last vs the previous candle's close
	Prev5mClose     float64 // previous candle's close
	Brk5mCloseUpAge float64 // long side: whole seconds since the current candle traded ABOVE the previous close; Null = not broken

	// Level breaks (see FieldBrkPmhAge / FieldBrkPrevAhAge).
	BrkPmhAge    float64 // whole seconds since the latest break above today's pre-market high; Null = none
	VsPmh        float64 // % of Last vs today's pre-market high
	PrevAhHigh   float64 // previous trading day's after-hours high
	BrkPrevAhAge float64 // whole seconds since the latest break above PrevAhHigh; Null = none
	VsPrevAh     float64 // % of Last vs PrevAhHigh
	Adv10        float64 // 10-day average daily volume (see FieldAdv10); Null = unknown
	PmVolX       float64 // pre-market volume so far / Adv10; Null = unknown

	BrkPmh5mAge    float64 // 5-min timeframe: whole seconds since a 5-min candle CLOSED above the pre-market high; Null = none
	BrkPrevAh5mAge float64 // 5-min timeframe: whole seconds since a 5-min candle CLOSED above PrevAhHigh; Null = none

	// Daily level breakout (see FieldDbLevel).
	DbLevel float64
	DbType  float64 // dlevel.Kind rank 1..3; Null = none
	DbPct   float64
	DbAge   float64

	// Previous-day-low break (see FieldPdlLevel).
	PdlLevel float64
	PdlPct   float64
	PdlAge   float64

	// Episodic Pivot overlay (see FieldEpPass). EpEarnHL is "" when none.
	EpPass, EpGap, EpVolX, EpEarnAge, Ep3m, EpAdr, EpAdrDays float64
	EpStop, EpOrh, EpBrkAge, EpRiskPct                       float64
	EpEarnHL                                                 string

	// 1-minute 9 EMA (see FieldBrkEma9Age).
	BrkEma9Age float64 // whole seconds since a 1-min candle closed under the 9 EMA (cross-under); Null = none
	VsEma9     float64 // % of Last vs the 9 EMA
	Ema9       float64 // the 1-min 9 EMA of completed candles

	// 1-minute candle spike (see FieldC1mChg / FieldC1mRvol).
	C1mChg  float64 // % change of the displayed 1-min candle vs the previous candle's close; Null = none
	C1mRvol float64 // displayed candle's volume / mean of the 9 minutes before it; Null = unknown

	// Session-scoped (current sub-session only; reset at each session boundary).
	ChgSess  float64 // % vs session-open price (Null until the open is known)
	SessHigh float64 // session high (Null until set)
	SessLow  float64 // session low (Null until set)
	SessVol  int64   // session volume (0 = none yet)
	SessVwap float64 // session VWAP (Null until volume accrues)

	// Frozen prior-session metrics, captured by the engine at the 09:30 (Pre*)
	// and 16:00 (Rth*) ET boundaries — the outgoing sub-session's scoped values
	// preserved so pre-market movers stay reviewable all day. Floats are Null
	// and vols 0 until that session has completed today (and stay so for a
	// ticker that never traded in it). *Chg is % vs the previous close AT
	// SESSION END — a chgDay snapshot at the boundary, not a change vs the
	// session's own first print. Memory-only: a restart after the boundary
	// loses them until the next boundary.
	PreChg       float64
	PreHigh      float64
	PreLow       float64
	PreVwap      float64
	PreGap       float64 // gap = pre-market session HIGH vs prev close, % — the peak gain, not close-vs-prevClose
	PreFade      float64 // fade = % off the pre-market session high at session end, clamped >= 0
	PreVol       int64
	PreVolPctAdv float64 // pre-market volume so far / AvgVol, % — see FieldPreVolPctAdv
	RthChg       float64
	RthHigh      float64
	RthLow       float64
	RthVwap      float64
	RthGap       float64 // gap = regular-session HIGH vs prev close, % — the peak gain
	RthFade      float64 // fade = % off the regular-session high at session end, clamped >= 0
	RthVol       int64

	Float  *int64  // free float (nil = unknown; may be shares-out proxy)
	ShsOut *int64  // shares outstanding (nil = unknown)
	Rot    float64 // float rotation = Vol/Float (Null when Float unknown)

	News    bool
	NewsAge int // seconds since latest headline; -1 = none

	// Hits is a PER-SCAN overlay, not a market metric: how many times this
	// symbol has entered the evaluating scan today (ET). The scanner stamps it
	// onto its own row copies just before filtering, so "hits >= 2" works as a
	// filter ("recurring movers only") and as a sort key. Zero outside a scan
	// evaluation context.
	Hits int

	// Daily-chart context (nightly grouped-bars refresh). DTrend: 1 = daily
	// uptrend, 0 = flat, -1 = downtrend, Null = insufficient history — numeric
	// so "dtrend >= 0" filters out daily downtrends. Chg20d = % change over
	// ~20 trading days (Null when unknown).
	DTrend float64
	Chg20d float64

	// Bull-flag / tight-consolidation context (nightly pattern pass). Like
	// DTrend these are a SCANNER-SIDE OVERLAY: the engine leaves them zero and
	// the scanner stamps them onto its own snapshot rows, so no pattern
	// geometry is ever computed in the per-tick hot path. Null when the symbol
	// has no pattern data, so a scan filtering on any of them simply excludes
	// unclassified tickers.
	//
	// FlagState is 0 = no pattern, 1 = coiling, 2 = breakout — and 2 is
	// assigned LIVE by the overlay when Last clears the stored base high, never
	// by the nightly pass (a breakout happens intraday). FlagBars is a bar
	// COUNT yet still float64: every numeric scan field resolves through
	// fieldNum, which returns float64 (DTrend set that precedent).
	FlagScore   float64
	FlagState   float64
	FlagPole    float64
	FlagRange   float64
	FlagRetrace float64
	FlagVolDry  float64
	FlagBars    float64

	// Intraday bull-flag context: same semantics as the FlagScore..FlagBars
	// block above, but computed over TODAY'S session at 5-minute-bucket
	// resolution by a ~60s worker (not nightly), and stamped by the same
	// scanner-side overlay (never in the engine hot path). Null when the
	// symbol has no intraday pattern data. IFlagState 2 (breakout) is
	// assigned LIVE by the overlay when Last clears the stored INTRADAY base
	// high — its own level, independent of the daily one. IFlagBars counts
	// 5-minute buckets where FlagBars counts daily bars.
	IFlagScore   float64
	IFlagState   float64
	IFlagPole    float64
	IFlagRange   float64
	IFlagRetrace float64
	IFlagVolDry  float64
	IFlagBars    float64

	// 9EMA flag market-scan context (see FieldE9State). Like the flag blocks
	// above this is a SCANNER-SIDE OVERLAY: the engine leaves these zero, the
	// flagscan service evaluates the 1-minute flag state machine on its own
	// loop and publishes EmaFlagRefs, and the scanner stamps them onto its own
	// snapshot rows -- no flag geometry ever runs in the engine's per-tick hot
	// path. Every field is Null for a symbol the service is not currently
	// evaluating, so `e9State == 0` can never match an unevaluated ticker. The
	// discrete ones (E9State, E9FlagBars) are float64 for the same reason
	// FlagState/FlagBars are: every numeric scan field resolves through
	// fieldNum. E9Age is stamped from the snapshot's own clock (AsOf), so it
	// counts up smoothly between the service's once-a-second publishes.
	E9State    float64
	E9Dist     float64
	E9Trigger  float64
	E9Stop     float64
	E9FlagBars float64
	E9Age      float64
	// The latest 9 EMA SLAM of the current ET day (see FieldE9SlamAge): Null
	// when the candidate has none today, or is not a candidate. E9SlamAge is
	// stamped from the snapshot clock like E9Age.
	E9SlamAge float64
	E9SlamPct float64
	// HOD FLAG (see FieldE9HodFlagAge): Null without one today.
	E9HodFlagAge float64

	// GREEN DILDO (see FieldDildoAge). The first four are ENGINE fields (the
	// engine's closed-bar judge, Null when the ticker has no dildo today); the
	// scanner overlay may replace them with a tracked symbol's chart-side dildo
	// when that one is newer. The last four are a SCANNER-SIDE OVERLAY of the
	// flagscan service's pullback verdict (EmaFlagRef), Null for a symbol it is
	// not evaluating. DildoPbState is float64 for fieldNum's sake, like E9State.
	DildoAge      float64
	DildoBody     float64
	DildoVolX     float64
	DildoDvol     float64
	DildoEarly    float64 // FieldDildoEarly: 1 / 0 / Null, about the dildo dildoAge times
	DildoPbAge    float64
	DildoPbPx     float64
	DildoTouchAge float64
	DildoPbState  float64

	// VOLUME CLIMAX (see FieldClimaxAge): engine fields, all Null when the
	// ticker has no climax today; ClimaxConfAge / ClimaxConfPx only while
	// ClimaxState is 2 (bottom confirmed).
	ClimaxAge     float64
	ClimaxDropPct float64
	ClimaxVolX    float64
	ClimaxWickPct float64
	ClimaxDvol    float64
	ClimaxLow     float64
	ClimaxState   float64
	ClimaxConfAge float64
	ClimaxConfPx  float64

	// BOUNCY BALL SHORT (see FieldBbsAge): engine fields, Null when nothing today.
	BbsAge     float64
	BbsSupport float64
	BbsState   float64
	BbsDvol    float64
	BbsDistPct float64

	// POWER CANDLE (see FieldPcAge): engine fields, Null when nothing today.
	PcAge     float64
	PcBodyPct float64
	PcTopPct  float64
	PcBodyX   float64
	PcVolX    float64
	PcDvol    float64
	// POWER SETUP (see FieldPsPass): engine fields, Null when no power candle today.
	PsPass      float64
	PsFromOpen  float64
	PsEmaSlope  float64
	PsGap       float64
	PsEntry     float64
	PsStop      float64
	PsTarget    float64
	PsAge       float64
	PsAplus     float64
	PsBaseRange float64
	// IGNITION (see FieldIgnAge): engine fields, Null when nothing today.
	IgnAge     float64
	IgnBodyPct float64
	IgnVolX    float64
	IgnFresh   float64 // FieldIgnFresh: 1 / 0 / Null
	IgnPass    float64 // FieldIgnPass: 1 / 0 / Null

	// VOLUME BREAK (see FieldVbAge): engine fields, Null when no break today.
	VbAge      float64
	VbLevel    float64
	VbVolX     float64
	VbEma      float64
	VbState    float64
	VbEntryAge float64
	VbEntryPx  float64

	// 9EMA VWAP CROSS (see FieldEvBullAge): engine fields, Null when none.
	EvBullAge float64
	EvBearAge float64
	EvEma     float64
	EvVwap    float64
	EvDist    float64
	EvBarVol  float64
	EvBarDvol float64
	EvSessVol float64

	// IBKR short availability. ShortAvail nil = the ticker isn't in IBKR's file
	// (treat as not shortable / unknown, NOT as zero).
	ShortAvail  *int64
	ShortCapped bool    // file said ">10000000"; ShortAvail is a floor
	ShortFee    float64 // annual borrow cost % (Null when unknown)

	Halted bool
	Sess   Session

	// SecType is the security-type code from the ticker reference (CS/ADRC/
	// ETF; "" if not yet known, e.g. before the first universe refresh).
	SecType string

	// Bars is the recent intraday OHLC series (oldest→newest) for the inline
	// candlestick chart. Empty until enough history accrues.
	Bars []Bar
}

// Bar is one OHLC candle for the inline chart. T is the bar-start epoch ms.
type Bar struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V int64   `json:"v"`
	// VW is the bar's own volume-weighted price when the source supplies it
	// (Massive/Alpaca REST minute bars); 0 = unknown (live chart bars). Used
	// by Engine.SeedScopeVwap to rebuild a session VWAP after a restart.
	VW float64 `json:"vw,omitempty"`
}

// DailyBar is one daily OHLCV candle with the ET trade date it belongs to. It
// is the unit of the widened daily-history pipeline and the input to the
// pattern math, so it deliberately depends on nothing: Date is the
// "2006-01-02" string daily_close_history keys its rows by (lexically
// sortable, which is how bar series stay in oldest→newest order). The
// intraday session-bars path (Engine.SessionBars) reuses the type at 5-minute
// resolution with Date = the bucket's ET start, "2006-01-02 15:04" — still
// lexically sortable within a day.
type DailyBar struct {
	Date string // "2006-01-02" ET trade date (or "2006-01-02 15:04" intraday)
	O    float64
	H    float64
	L    float64
	C    float64
	V    int64
}

// MetricSnapshot is an immutable, point-in-time view of all tracked tickers,
// produced by the engine's merger ~4 Hz and consumed by the scanner. A fresh
// instance is allocated per generation (no buffer reuse), so a slow scanner can
// never observe a torn snapshot.
type MetricSnapshot struct {
	Gen   uint64
	AsOf  int64 // engine tick time, epoch ms
	Sess  Session
	Rows  []Row          // dense, ~7–11k
	Index map[string]int // sym -> Rows position (diff / news-rvol overlay); read-only, may be shared with other snapshots
	// IndexGen identifies Index: snapshots with the same non-zero IndexGen share
	// one Index map, so their rows are in the same order.
	IndexGen uint64
}
