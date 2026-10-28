// Pure short-fade signal engine: turns a symbol's own candles into a
// deterministic "wait / short here" call for the trader's exact failure
// mode — shorting a spiking penny stock too early and averaging up into
// strength. No DOM, no store, no `Date.now()` (the caller passes `nowMs`);
// this module is imported TYPE-ONLY against the vendored chart engine (see
// chartAgg.ts / chartTheme.ts for why) so its vitest suite runs in plain
// node with zero canvas/DOM weight, and can be unit-tested with hand-built
// candle fixtures with no chart, store or WS in the loop.
//
// THE STATE MACHINE, and why each gate exists — idle -> spike -> crack ->
// bounce -> armed -> short:
//
//   idle    Nothing notable has happened, OR the day's high-of-day (HOD)
//           hasn't run/gapped far enough to be a shortable spike yet.
//   spike   A HOD has printed a real spike (arming gate below). This state
//           exists ONLY to say "do not short into strength" — the exact
//           mistake in the brief (shorted at 0.55/0.61/0.66 while price was
//           STILL RISING). No entry rule fires in this state, on purpose.
//   crack   The first sign the spike has topped: a close under the prior
//           bar's low, or a real pullback off the high. This is NOT an
//           entry — a crack alone is one red candle, indistinguishable from
//           a normal dip within an ongoing spike. It only downgrades "do not
//           short" to "start watching for structure."
//   bounce  A confirmed swing low has formed after the crack — the market
//           has shown it can hold a level, so the NEXT high is measurable
//           against something. Still not an entry: a bounce could just be
//           the spike resuming.
//   armed   A confirmed swing high has formed BELOW the HOD (a lower high)
//           — the first objective, retrospective proof that sellers turned
//           up before buyers finished, which is the whole diagnosis the
//           user's manual entries were missing. This is the "wait a bit
//           longer" state: trigger/stop are now defined and displayed, but
//           no position yet.
//   short   Price actually traded through the pullback low (or, as an
//           alternate path, closed back under VWAP after the top formed —
//           `vwapLossEntry`) — i.e. the lower high has been CONFIRMED by
//           price making a new low, not just asserted by shape. This is the
//           entry the user asked for: wait for the top, then short the
//           break, not the spike.
//
// A fresh, HIGHER high-of-day invalidates every state past `spike` (even a
// live short — "cover, the top wasn't in") because every downstream level
// (lower high, trigger, stop) is measured FROM the HOD; a new HOD makes them
// all stale at once, not just the one that most obviously broke. Re-arming
// requires the structure to re-form against the new high, not a discount off
// the old one.
//
// Everything here is measured off CLOSED bars only (structure confirmation,
// the crack test, the VWAP close rules, the stop) so nothing "un-happens" as
// a bar's wick moves around mid-print — except the three rules the
// forming-bar pass (2.4, at the bottom of `processDay`) also runs on the
// bar still printing: new-HOD-by-high, entry-by-low, base-target-by-low.
// Each uses a monotonic intrabar extreme (a forming bar's `high` only ever
// grows, its `low` only ever falls), which is precisely why they're safe to
// evaluate early — they can only become MORE true, never flip back, before
// the bar closes. `stopByClose:false` adds a fourth monotonic-extreme rule
// (the stop, by `high`) to that forming-bar set — see the note below.
//
// ─── Iteration 2 — what REAL 1-minute bars changed, and why ──────────────
//
// The state machine above was checked against real fixture data for five
// names on 2026-09-24 (ATYR, GCTK, WETO, SPHL, DCX — src/signals/fixtures/)
// and the STRUCTURE held — the engine finds the same top a trader would
// read off the chart by eye. What was wrong was the NOISE:
//
//   1. `vwapLossEntry` (closing back under VWAP is itself a SHORT) fired
//      minutes into the day, before any top had formed, with the stop
//      parked above the raw HOD — VWAP built on a handful of bars is not a
//      level yet, and a HOD-stop is exactly the wide, too-early risk this
//      tool exists to prevent (a real 35%-risk short on one fixture). The
//      default is now FALSE, and even switched on it is honoured only once
//      a lower high already exists — the stop then sits above THAT lower
//      high, never above HOD.
//   2. A close within a few tenths of a percent of VWAP flipped `target`
//      (T1) and `vwapReclaim` on ALTERNATE bars (four of each in eight
//      minutes on one real name) — VWAP is a cumulative average, not a
//      trip-wire, and price rides along it for minutes at a time during a
//      fade. `vwapBandPct` (default 0.5%) turns the bare cross into a band:
//      LOST needs a close 0.5% under VWAP, RECLAIMED needs a close 0.5%
//      over it. The old `close < vwap` / prior-close cross test is gone.
//   3. Every small counter-bounce after the fade prints a fresh CONFIRMED
//      swing high, so the old engine announced a `lowerHigh` — and would
//      now fire an `entryLH`/add — on each one (twelve on one real name),
//      right up until an ordinary bounce closed back above the trailing
//      stop and covered a trade that was otherwise working. Two rules fix
//      this without losing real information: (a) once VWAP is LOST, a
//      confirmed lower high is tracked SILENTLY — the level still moves,
//      still protects the stop, but nothing prints; the visible signal
//      below VWAP is `vwapReject`, not another `lowerHigh`; (b) a stop
//      derived from a lower high only ever moves UP, never down, so a
//      smaller bounce after a bigger one can't loosen it.
//   4. A live-scanner prevClose of exactly 0 (a separate, already-tracked
//      bug) used to leave the gap gate permanently unarmable, because `??`
//      treats 0 as "present" and never falls back. The fallback is now
//      keyed off `prevClose > 0`, so a bad 0 falls through to the previous
//      trading day's own last (after-hours) close exactly as a genuinely
//      ABSENT prevClose always did; `evidence.refClose`/`refSource` name
//      which one was actually used, because a fallback overnight reference
//      is weaker evidence than an official close and the panel should say
//      so rather than presenting both identically.
//
// The primary entry is therefore now the LOWER HIGH's own CONFIRMING close
// (`entryLH`, param `lhEntry`, default true) — a trader shorts a confirmed
// top, not a level price might never revisit — with the old break of the
// pullback low demoted to `breakdown` (confirmation / add) once already
// short via a lower high, and promoted back to being the SOLE entry
// (`entry`, unchanged) when `lhEntry` is off, same as iteration 1. The stop
// rule can be read two ways: `stopByClose` (default true) requires a CLOSE
// back above the stop, matching every other close-based rule in this
// engine; `stopByClose:false` is a hard intrabar stop order (any HIGH at or
// above it), checked on the forming bar too since a wick's high, like its
// low, only ever grows before the bar closes.
//
// ─── Iteration 4 — the LONG side, and the ABCD shape both sides share ────
//
// The user's second ask: "I want to build the same for the long side" — a
// PULLBACK LONG in the same spiking stock, read off the SAME structure the
// short side already finds, just diverging at the second swing:
//
//   A = HOD           the spike top (unchanged — `hod`).
//   B = pullback low  the running low since HOD (unchanged — `pullbackLow`).
//   C = bounce high   the first confirmed swing high after B. This is
//                     EXACTLY the swing high the short side already detects
//                     and calls a `lowerHigh` whenever it prints below HOD —
//                     tracked here in its own `cHigh`/`cHighIdx` pair so the
//                     long side keeps its own memory of C independent of
//                     the short's `lowerHigh`, which the short resets on
//                     its own stop (a stopped-out short must not erase the
//                     long's own reference level).
//   D = higher low    a second confirmed swing low, strictly ABOVE B and
//                     (by default) at/above VWAP at D's own bar — the
//                     mirror-image of the short's lower high: objective,
//                     retrospective proof that BUYERS turned up before
//                     sellers finished the pullback. Confirming D is the
//                     long's entry (`entryHL`), same shape as the short's
//                     `entryLH` at C.
//
// The two sides are a single read of the same pullback, diverging only in
// which swing confirms first and in which direction price then breaks:
// SHORT enters on C's own confirming close (or later breaks B); LONG enters
// on D's own confirming close (or later breaks C — the BREAKOUT). Both can
// be open at once — a lower high (short) and a deeper higher low (long) are
// not contradictory, they are the market not having decided yet; whichever
// break comes first (below B or above C) closes the LOSING side by that
// side's own stop rule, honestly, rather than the engine picking a winner
// in advance. `SignalState` reports this plainly: `'short'` (short open,
// no long), `'long'` (long open, no short), `'both'` (one of each open) —
// on top of the unchanged shared prefix idle/spike/crack/bounce/armed.
//
// A long's stop, once set at D, NEVER MOVES — unlike the short's
// stop-only-ever-up rule (iteration 2, which reacts to a MORE bearish
// lower high). There is no long-side equivalent event: a higher low that
// prints while already long is an `entryHL` 'ADD (HL)' (more conviction,
// same stop), never a stop adjustment — iteration 2's own reasoning against
// trailing a stop on 1-minute noise applies just as much here, and the long
// side never even acquires the "was this new extreme more favourable"
// question the short side's trailing-up rule exists to answer.
//
// VWAP plays an asymmetric role on the two sides: for the short, losing
// VWAP is a TARGET (T1) and — only once a lower high exists — an optional
// entry; for the long, losing VWAP after entry is only ever a WARNING
// (`longWarn`, list-only) — the position isn't closed by VWAP, only by its
// own stop — while HOLDING VWAP (a dip that touches it and closes back
// above) is either the long's entry while armed (`vwapHold` 'LONG (VWAP
// HOLD)') or an add while already long (`vwapHold` 'VWAP HOLD').
//
// ─── Iteration 4, §8 — the short side's own silent-lower-high rule was too broad ──
//
// Iteration 2 (B2) silenced a confirmed lower high whenever the TRANSIENT
// `vwapLost` flag happened to be true on its confirming bar — meant to kill
// the "every 3-bar bounce after the fade prints a lowerHigh" spam, it also
// swallowed the real ATYR top's FIRST and best lower high: VWAP dipped
// 0.5% under on the 06:22 close (flipping `vwapLost` true), the 0.6670
// lower high confirmed on the very next (06:23) close — silently, by the
// old rule — even though 0.6670 sits ~12% ABOVE VWAP, and VWAP reclaimed on
// that SAME 06:23 bar. A one-bar dip that reclaims immediately is not "this
// bounce is noise below VWAP" — the rule's own intent — it just happened to
// land on the wrong side of a coin-flip band crossing.
//
// The fix judges the lower high by its OWN price, not by a same-bar flag
// that can flip on an unrelated close: a lower high is silent iff `high[j]`
// (the swing's own price) is below VWAP AT ITS CONFIRMING BAR (`vwap[i]`),
// full stop — `vwapLost`'s live value no longer enters into it at all, for
// either the visible-vs-silent branch OR the "ADD (LH)" condition inside
// the visible branch. A lower high genuinely printed below VWAP (the real
// "every small bounce in the chop" case iteration 2 was built for) stays
// silent; one printed well above VWAP that merely closed through the band
// on its way down does not.
//
// ─── Iteration 5 — the adds are capped, per episode, per side ────────────
//
// Replaying the five real fixtures after §8 showed the engine scaling in on
// every 3-bar wiggle for an hour: seven ADD (LH) on one name, six on
// another, three VWAP REJECT prints for a single retest of one VWAP, and on
// ATYR an 'ADD (LH)' at 07:05 that printed 9% under VWAP with the stop still
// at 0.6868 — a 37%-risk add after the first target. A trader scales in a
// couple of times near the top and re-enters on a clean retest, so a short
// episode now allows at most `maxAdds` adds ('ADD (LH)' plus the add form of
// 'VWAP REJECT'; the long mirrors it with 'ADD (HL)' plus 'VWAP HOLD'), an
// 'ADD (LH)'/'ADD (HL)' is refused once T1 has printed or when its own risk
// to the stop exceeds `maxAddRiskPct`, and a VWAP REJECT/HOLD only re-arms
// after a close 2 x `vwapBandPct` on the far side of VWAP — price has to
// leave the line before its next test counts as a new event. The entries
// and the one-per-episode BREAKDOWN/BREAKOUT are never counted, and a
// refused add loses only its marker: `lowerHigh`/`higherLow` still print
// list-only and the stop bookkeeping (the short's stop only moving up) runs
// exactly as before.
//
// ─── Iteration 6 — what the review found in the forming-bar pass ─────────
//
// The forming pass may show a trader an event early, but the CLOSE of that
// same bar must never take it back. (Labelled I6-F1..F4 here so they stay
// apart from iteration 1's own F1-F4 further down.) Four ways the closed loop's ORDER of
// rules (structure first) made it do exactly that, all fixed without
// touching what the five real fixtures produce at the default params:
//
//   I6-F1 (long BREAKOUT / T1 BOUNCE HIGH)  The long's intrabar rules judged
//      the bar against C as the closed loop had ALREADY raised it. A swing
//      high confirmed at j = i-k is above high[i] by definition, so the
//      raised trigger could never be cleared and the closed pass withdrew a
//      BREAKOUT (a buy marker, and a sound) the forming pass had shown
//      against the old C. Both rules now read `cHighIn` — C exactly as it
//      stood going INTO the bar — in both passes.
//   I6-F2 (spike arming on a forming bar)   Of the arming gates only the HOD,
//      the run low and the gap gain are monotonic while a bar prints; the
//      VWAP extension is not (the live bar's own volume drags VWAP up under
//      it), so a SPIKE armed through it early in a bar could vanish before
//      the bar closed. A forming bar now arms from `idle` on the monotonic
//      gates only (the gap gate; the run gate when the VWAP gate is off) and
//      never demotes an episode already in progress; the close re-decides
//      with the full gate. (Otherwise every forming new-HOD bar of a run
//      would read "No spike yet" until it closed.)
//   I6-F3 (higher low against a stale C)    C is "the swing high AFTER B", but
//      the gate only checked C came before D. After a breakdown to a NEW
//      low, a swing low above it qualified against a C that pre-dated the
//      low. The running pullback low now remembers WHERE it was set
//      (`pullbackLowIdx`) and D needs a C that came after it; the display
//      stops showing a stale C too, unless a long is already open on it.
//   I6-F4 (hard stop, `stopByClose:false`)  stepStructure ran before stepStop
//      and could confirm a swing high that lifted the stop above high[i],
//      cancelling a resting stop the bar had already traded — or emitted its
//      `lowerHigh` ahead of the stop the forming pass ended on. In hard-stop
//      mode the closed loop now replays the forming pass's rules FIRST, then
//      runs structure and the close-only rules.
//
// The contract this leaves (and the tests assert): closed history is a
// strict prefix of every later evaluation, and every event a forming bar
// showed survives its close as a set — only the ORDER within the newly
// closed bar may change, because B2 puts a structure event the close
// confirms ahead of that bar's own intrabar breakdown.

import type { Candle } from '../vendor/blackthorn-charts';
import { etDayKey } from '../marketClock';

// ─── 2.1 Types ───────────────────────────────────────────────────────────

export interface SignalParams {
  minSpikePct: number; // 15   run gain (runLow -> HOD) needed to arm, %
  minVwapExtPct: number; // 8    HOD must be this % above VWAP at the HOD bar (0 = disabled)
  minGapPct: number; // 15   ALTERNATIVE arming: HOD vs prevClose (or its fallback) gain, % (gappers with no intraday run) — was 20; see iteration-2 note
  runLookbackBars: number; // 60   bars before HOD searched for the run's low (the "base")
  swingStrength: number; // 2    bars on each side that confirm a swing high / swing low
  minPullbackPct: number; // 5    a drop of this % from HOD is a "crack" even without a close below the prior low
  breakBufferPct: number; // 0.3  price must trade this % BELOW the trigger to count as a break
  stopBufferPct: number; // 1.0  stop sits this % ABOVE the lower high (or HOD when no lower high / no LH yet)
  climaxVolMult: number; // 3    evidence only: volume in [HOD-3, HOD] >= this x avg volume of earlier bars that day
  vwapLossEntry: boolean; // false (was true) a CLOSE below VWAP after the top forms is itself an entry — only honoured once a lower high exists (stop above IT, never above HOD)
  lhEntry: boolean; // true  the close of the bar that CONFIRMS a lower high is the primary SHORT entry (entryLH), not just an "armed" flag
  stopByClose: boolean; // true  the stop rule uses the bar CLOSE above the stop; false = any HIGH at/above it (a hard stop order, checked on forming bars too)
  vwapBandPct: number; // 0.5  hysteresis: VWAP counts as LOST when a close is this % below it and RECLAIMED only when a close is this % above it (replaces the old bare cross test)
  hlEntry: boolean; // true  the close that CONFIRMS a higher low is the primary LONG entry (entryHL) — the long side's mirror of lhEntry (iteration 4)
  hlAboveVwap: boolean; // true  a higher low only counts when its OWN low is at/above VWAP at that bar — keeps the long out of sub-VWAP chop, same intent as the short's silent-lower-high rule (iteration 4)
  maxAdds: number; // 2    the most ADD signals per episode, per side: 'ADD (LH)' + the add form of 'VWAP REJECT' (short), 'ADD (HL)' + the add form of 'VWAP HOLD' (long). Entries and BREAKDOWN/BREAKOUT never count; 0 = no adds at all (iteration 5)
  maxAddRiskPct: number; // 12   an 'ADD (LH)' / 'ADD (HL)' needs its own risk — distance from the confirming close to the episode's current stop, as % of that close — at or under this (iteration 5)
}

export const DEFAULT_PARAMS: SignalParams = {
  minSpikePct: 15,
  minVwapExtPct: 8,
  minGapPct: 15,
  runLookbackBars: 60,
  swingStrength: 2,
  minPullbackPct: 5,
  breakBufferPct: 0.3,
  stopBufferPct: 1.0,
  climaxVolMult: 3,
  vwapLossEntry: false,
  lhEntry: true,
  stopByClose: true,
  vwapBandPct: 0.5,
  hlEntry: true,
  hlAboveVwap: true,
  maxAdds: 2,
  maxAddRiskPct: 12,
};

export type SignalState = 'idle' | 'spike' | 'crack' | 'bounce' | 'armed' | 'short' | 'long' | 'both';

export type SignalEventKind =
  | 'spike' // engine armed (idle -> spike): "do not short into strength"
  | 'crack' // first crack after HOD
  | 'lowerHigh' // a confirmed swing high below HOD, list-only (silent instead — no event at all — while VWAP is lost; iteration 2, see B2)
  | 'entryLH' // SHORT/ADD: the bar that CONFIRMS a lower high above VWAP (marker 'sell') — iteration 2, the primary entry; an 'ADD (LH)' is capped (iteration 5: `maxAdds`, not after T1, bounded risk)
  | 'breakdown' // price traded below the trigger while ALREADY short via a lower high (marker 'sell') — confirmation/add, iteration 2
  | 'entry' // SHORT: price traded below the trigger while NOT already short (marker 'sell') — the sole entry when lhEntry:false
  | 'entryVwap' // SHORT: closed below VWAP after the top formed, only once a lower high exists (marker 'sell')
  | 'vwapReject' // touched VWAP from below and closed under it again (marker 'sell'); an entry (label 'SHORT (VWAP REJECT)') if not already short, else a capped add (iteration 5); re-arms only after a close 2 x vwapBandPct under VWAP
  | 'target' // T1 VWAP reached (a close below VWAP while short, once per episode) or T2 base reached (marker 'buy')
  | 'vwapReclaim' // closed back above VWAP after losing it (warning, no marker)
  | 'stopped' // stop hit while short — by close or by high per `stopByClose` (marker 'buy')
  | 'invalidated' // new HOD while crack/bounce/armed/short (marker 'buy' only if it was short)
  // ─── iteration 4 — the LONG side (see the module comment's ABCD section) ──
  | 'higherLow' // a confirmed swing low above the pullback low (D), list-only
  | 'entryHL' // LONG/ADD: the bar that CONFIRMS a higher low, while VWAP is not lost (marker 'buy') — the primary long entry; an 'ADD (HL)' is capped (iteration 5: `maxAdds`, not after T1, bounded risk)
  | 'breakout' // price traded above the C bounce high — 'LONG' entry when armed-long with hlEntry:false, 'BREAKOUT' confirmation/add once already long (marker 'buy')
  | 'vwapHold' // a dip to VWAP that closed back above it — 'LONG (VWAP HOLD)' entry while armed, 'VWAP HOLD' add while already long (marker 'buy', capped — iteration 5); re-arms only after a close 2 x vwapBandPct over VWAP
  | 'longWarn' // closed below VWAP while long — a WARNING only (the long is closed by its own stop, never by VWAP), list-only
  | 'longStopped' // long's stop hit — by close or by low per `stopByClose` (marker 'sell')
  | 'longTarget'; // T1 bounce high (C), T2 HOD (A), or a NEW HOD reached while long (marker 'sell')

export interface SignalEvent {
  ts: number; // the candle's ts the event belongs to
  kind: SignalEventKind;
  price: number; // the level the event is about (break level, VWAP, stop, HOD ...)
  label: string; // short, for the chart marker pill: 'SHORT (LH)', 'ADD (LH)', 'BREAKDOWN', 'SHORT', 'SHORT (VWAP)', 'VWAP REJECT', 'SHORT (VWAP REJECT)', 'T1 VWAP', 'BASE', 'STOP', 'NEW HOD', 'LOWER HIGH', 'CRACK', 'SPIKE', 'HIGHER LOW', 'LONG (HL)', 'ADD (HL)', 'LONG', 'BREAKOUT', 'LONG (VWAP HOLD)', 'VWAP HOLD', 'VWAP LOST', 'STOP (LONG)', 'T1 BOUNCE HIGH', 'T2 HOD'
  note: string; // one sentence for the events list, with ET times and prices
  marker?: 'sell' | 'buy'; // draw as an engine Execution marker; absent = list-only
  side: 'short' | 'long'; // iteration 4: which side of the engine this event belongs to — spike/crack/invalidated are 'short' (the shared spike-top story), except the long's own 'NEW HOD' target (module comment, §4/g2)
}

export interface SignalLevels {
  hod: number | null;
  hodTs: number | null;
  runLow: number | null;
  runLowTs: number | null; // the pre-spike base (T2)
  pullbackLow: number | null; // lowest low since HOD (running)
  swingLow: number | null;
  swingLowTs: number | null; // most recent CONFIRMED swing low after HOD
  lowerHigh: number | null;
  lowerHighTs: number | null; // most recent confirmed swing high after HOD, < HOD
  trigger: number | null; // (swingLow ?? pullbackLow) * (1 - breakBufferPct/100), only when armed/short
  stop: number | null; // (lowerHigh ?? hod) * (1 + stopBufferPct/100), only when armed/short
  vwap: number | null; // VWAP at the last bar of the day
  entryPrice: number | null; // the FIRST entry of the current short episode (an 'ADD (LH)' moves the stop, never entryPrice)
  entryTs: number | null; // set while short
  entryKind: 'lh' | 'break' | 'vwap' | null; // which rule opened the current episode — iteration 2
  // ─── iteration 4 — the LONG side ──────────────────────────────────────
  higherLow: number | null; // D — the confirmed swing low that arms/enters the long
  higherLowTs: number | null;
  bounceHigh: number | null; // C — the confirmed swing high the short side calls `lowerHigh`; the long's trigger base
  longTrigger: number | null; // C * (1 + breakBufferPct/100); null until C exists
  longStop: number | null; // the long episode's actual stop while armed/long; null otherwise
  longEntryPrice: number | null; // the FIRST entry of the current long episode (an 'ADD (HL)'/'BREAKOUT' never moves it)
  longEntryTs: number | null;
  longEntryKind: 'hl' | 'break' | 'vwap' | null; // which rule opened the current long episode
}

export interface SignalEvidence {
  runGainPct: number | null; // (hod - runLow) / runLow * 100
  gapGainPct: number | null; // (hod - refClose) / refClose * 100, null without a ref close (see refClose/refSource)
  vwapExtPct: number | null; // (hod / vwap[hodIdx] - 1) * 100
  climaxVol: boolean;
  pullbackPct: number | null; // (hod - pullbackLow) / hod * 100
  retracePct: number | null; // (bounceHigh - pullbackLow) / (hod - pullbackLow) * 100 — how much of the drop the bounce recovered
  barsSinceHod: number | null;
  riskPct: number | null; // (stop - trigger) / trigger * 100 when armed; (stop - entryPrice)/entryPrice when short
  refClose: number | null; // the overnight reference actually used for gapGainPct — iteration 2 (see refSource)
  refSource: 'prevClose' | 'prevDayClose' | null; // 'prevClose' = the caller's official prior-session close (only when > 0); 'prevDayClose' = fallback to the previous ET day's own last candle in this array (a live prevClose of exactly 0 — a separate, tracked bug — now falls through to this the same as a genuinely absent one)
}

export interface SignalResult {
  state: SignalState;
  dayKey: string; // ET 'YYYY-MM-DD' of the last day evaluated ('' for no bars)
  levels: SignalLevels;
  evidence: SignalEvidence;
  // ALL days, oldest first, in EMISSION order — NOT sorted by ts. A
  // `lowerHigh` (and the entryLH/breakdown/etc. that can follow it) carries
  // the swing BAR's ts but is only emitted once its CONFIRMING bar closes
  // k bars later, so ts order and emission order diverge on purpose (the
  // Signal window sorts by ts descending, ties in reverse emission order,
  // for display — this array itself stays in emission order).
  events: SignalEvent[];
  headline: string; // e.g. 'ARMED — short below 0.598'. The COMBINED read of both sides (iteration 4, see 5) — one side's own headline when only it is open, a 'TWO-SIDED' summary when both are, otherwise the short's shared spike-top narrative with the long's first detail line appended once C exists.
  detail: string[]; // 2–4 short lines for the panel (see 2.5); combined the same way as `headline`
  // ─── iteration 4 — per-side reads, so the UI can show one side alone ──
  short: { state: 'none' | 'armed' | 'short'; headline: string; detail: string[] }; // == the pre-iteration-4 top-level headline/detail, unchanged wording, just relocated
  long: { state: 'none' | 'armed' | 'long'; headline: string; detail: string[] };
}

export interface EvalInput {
  candles: Candle[]; // ts ascending; may span the previous trading day + today
  tfMs: number; // candle width (60_000 for 1m, 300_000 for 5m)
  nowMs: number; // the last candle is the FORMING bar iff nowMs < last.ts + tfMs
  prevClose?: number | null; // prior session's official close, for the gap gate
  params?: Partial<SignalParams>;
}

// ─── fmtEt / fmtPx ───────────────────────────────────────────────────────

const ET_HM_FORMAT = new Intl.DateTimeFormat('en-US', {
  timeZone: 'America/New_York',
  hour12: false,
  hour: '2-digit',
  minute: '2-digit',
});

/** 'HH:MM' in America/New_York (normalises hour12:false's '24' at midnight to '00'). */
export function fmtEt(ts: number): string {
  let hh = '00';
  let mm = '00';
  for (const p of ET_HM_FORMAT.formatToParts(new Date(ts))) {
    if (p.type === 'hour') hh = p.value === '24' ? '00' : p.value;
    else if (p.type === 'minute') mm = p.value;
  }
  return `${hh}:${mm}`;
}

/** Same rule as MonitorChart's `dp`: sub-$1 tickers need 4dp or every level looks identical. */
export function fmtPx(v: number): string {
  return Math.abs(v) < 1 ? v.toFixed(4) : v.toFixed(2);
}

// One-decimal percent, for evidence/detail text only (not exported — the
// public surface is exactly 2.1's names).
function pct1(v: number): string {
  return `${v.toFixed(1)}%`;
}

// ─── Per-day precompute (2.2) ────────────────────────────────────────────

// Cumulative session VWAP, mirroring the vendored engine's own construction
// (indicators/registry.ts `vwap`) bar for bar: null while cumulative volume
// is zero, otherwise cumPV/cumV. Called once per DAY's candle slice (the
// caller already grouped by etDayKey), so there is no day-reset branch here
// — day 2's array starts this function's cumPV/cumV fresh, same as the
// engine resets on `etDate(c.ts)` changing.
function dayVwap(candles: Candle[]): (number | null)[] {
  const out: (number | null)[] = new Array(candles.length).fill(null);
  let cumPV = 0;
  let cumV = 0;
  for (let i = 0; i < candles.length; i++) {
    const c = candles[i]!;
    cumPV += ((c.high + c.low + c.close) / 3) * c.volume;
    cumV += c.volume;
    if (cumV > 0) out[i] = cumPV / cumV;
  }
  return out;
}

// mean volume of bars [0, i) of the day; 0 for i<=0 (no earlier bars).
function avgVolBefore(volumes: number[], i: number): number {
  if (i <= 0) return 0;
  let sum = 0;
  for (let x = 0; x < i; x++) sum += volumes[x]!;
  return sum / i;
}

// Evidence-only "did the top print on a burst of volume" check. Compares the
// average volume of the (at most 4-bar) window [hodIdx-3, hodIdx] against the
// average volume of every bar STRICTLY BEFORE that window. Requires at least
// one bar before the window to compare against (a HOD on bar 0-3 of the day
// has nothing earlier to call a "burst" relative to, so reads false rather
// than true-by-default-of-no-data).
function climaxVolume(volumes: number[], hodIdx: number, mult: number): boolean {
  if (mult <= 0) return false;
  const start = Math.max(0, hodIdx - 3);
  if (start === 0) return false;
  let winSum = 0;
  for (let x = start; x <= hodIdx; x++) winSum += volumes[x]!;
  const winAvg = winSum / (hodIdx - start + 1);
  const earlierAvg = avgVolBefore(volumes, start);
  return earlierAvg > 0 && winAvg >= mult * earlierAvg;
}

// Swing detection (only ever called with fully-closed bars on both sides —
// see the callers): strength k, candidate j, requires low/high[j-k..j+k] all
// defined (caller enforces j-k>=0 and j+k<=lastClosedIdx).
function isSwingLow(low: number[], j: number, k: number): boolean {
  for (let m = 1; m <= k; m++) {
    if (!(low[j]! <= low[j - m]!)) return false;
    if (!(low[j]! < low[j + m]!)) return false;
  }
  return true;
}
function isSwingHigh(high: number[], j: number, k: number): boolean {
  for (let m = 1; m <= k; m++) {
    if (!(high[j]! >= high[j - m]!)) return false;
    if (!(high[j]! > high[j + m]!)) return false;
  }
  return true;
}

function maxHigh(high: number[], from: number, to: number): number {
  let m = high[from]!;
  for (let x = from + 1; x <= to; x++) if (high[x]! > m) m = high[x]!;
  return m;
}

// The trigger's base level. A confirmed swing low is, by definition, >=
// pullbackLow (the running low since HOD) — UNLESS it was confirmed by
// structure that ran while price was still above it (a swing low printed
// during the spike, before any crack, or a post-crack swing low that a
// later bar has since undercut). In that case the swing low is stale: the
// break that actually matters is of the real pullback low, not a level
// price has already traded through. min() picks whichever is lower, so the
// trigger is never set ABOVE price that has already printed (F3).
function triggerBase(swingLow: number | null, pullbackLow: number | null): number | null {
  return swingLow != null && pullbackLow != null ? Math.min(swingLow, pullbackLow) : (swingLow ?? pullbackLow);
}

function computeTrigger(swingLow: number | null, pullbackLow: number | null, params: SignalParams): number | null {
  const base = triggerBase(swingLow, pullbackLow);
  return base != null ? base * (1 - params.breakBufferPct / 100) : null;
}

function computeStop(lowerHigh: number | null, hod: number, params: SignalParams): number {
  return (lowerHigh ?? hod) * (1 + params.stopBufferPct / 100);
}

function computeRetrace(bounceHigh: number | null, pullbackLow: number | null, hod: number | null): number | null {
  if (bounceHigh == null || pullbackLow == null || hod == null) return null;
  const drop = hod - pullbackLow;
  if (drop <= 0) return null;
  return ((bounceHigh - pullbackLow) / drop) * 100;
}

// iteration 4: the per-day SHORT-side state machine's own `state` variable
// (below) never actually holds 'long' or 'both' — those are combined/public
// values computed AFTER the fact from this variable plus the long side's
// own `long.state` (see the end of processDay) — so it is typed with this
// narrower alias rather than the full, widened `SignalState`. "the short
// rules are untouched" (module comment) is true of this type too.
type ShortState = 'idle' | 'spike' | 'crack' | 'bounce' | 'armed' | 'short';

// `state` is a `let` mutated only from inside nested closures (stepHod,
// stepStructure, ...) called from a loop — TypeScript's control-flow
// narrowing does not follow assignments made through a closure call, so a
// direct `state === 'armed'` read in the OUTER scope after the loop still
// carries whatever literal narrowed type the declaration had ('idle'),
// which makes every other branch a false "no overlap" compile error.
// Routing the comparison through a plain function call sidesteps it: inside
// `stateIn`, `s` has its full declared `ShortState` type with none of the
// caller's stale narrowing.
function stateIn(s: ShortState, ...opts: ShortState[]): boolean {
  return opts.includes(s);
}

// Same closure-narrowing issue as `stateIn`, for the `entry` variable: it is
// only ever set from inside `stepEntry`/`stepVwap`/`stepStop` (closures), so
// a direct `entry ? entry.price : ...` read in the outer scope keeps
// TypeScript's stale narrowed-to-`null` type from the declaration, which
// then makes the truthy branch `never`. Routing through this identity
// function re-types it as the full declared union.
// Shape shared by the short's `entry` and (iteration 4) the long's
// `longEntry` — same closure-narrowing issue as `stateIn` above, for both.
type Episode = { idx: number; price: number; stop: number };
function widenEntry(e: Episode | null): Episode | null {
  return e;
}

function mkEvent(
  ts: number,
  kind: SignalEventKind,
  price: number,
  label: string,
  note: string,
  marker?: 'sell' | 'buy',
  side: 'short' | 'long' = 'short', // iteration 4: every existing short-side call site is unaffected by this default
): SignalEvent {
  return marker ? { ts, kind, price, label, note, marker, side } : { ts, kind, price, label, note, side };
}

interface DayOutcome {
  events: SignalEvent[];
  state: SignalState;
  levels: SignalLevels;
  evidence: SignalEvidence;
  headline: string;
  detail: string[];
  short: { state: 'none' | 'armed' | 'short'; headline: string; detail: string[] };
  long: { state: 'none' | 'armed' | 'long'; headline: string; detail: string[] };
}

function emptyLevels(): SignalLevels {
  return {
    hod: null,
    hodTs: null,
    runLow: null,
    runLowTs: null,
    pullbackLow: null,
    swingLow: null,
    swingLowTs: null,
    lowerHigh: null,
    lowerHighTs: null,
    trigger: null,
    stop: null,
    vwap: null,
    entryPrice: null,
    entryTs: null,
    entryKind: null,
    higherLow: null,
    higherLowTs: null,
    bounceHigh: null,
    longTrigger: null,
    longStop: null,
    longEntryPrice: null,
    longEntryTs: null,
    longEntryKind: null,
  };
}

function emptyEvidence(): SignalEvidence {
  return {
    runGainPct: null,
    gapGainPct: null,
    vwapExtPct: null,
    climaxVol: false,
    pullbackPct: null,
    retracePct: null,
    barsSinceHod: null,
    riskPct: null,
    refClose: null,
    refSource: null,
  };
}

// Runs the state machine (2.2-2.4) over exactly ONE day's candles: the
// closed-bar loop below calls stepHod/stepStructure/stepCrack/stepEntry/
// stepBaseTarget/stepVwap/stepStop/stepBaseTarget (again) for every closed
// bar — (g) runs both right after (d) and again after (f), since a BASE
// touch is, like the entry, an intrabar-low event that must not un-happen
// when the same bar's CLOSE later triggers the stop (F4); the second call is
// a no-op unless (e)'s entryVwap just opened a short whose low had already
// reached the base. Then — only when `forming` is true — a restricted
// forming-bar pass runs stepHod/stepEntry/stepBaseTarget ((a)/(d)/(g), 2.4)
// once more on the last, still-printing candle. Those three are shared
// functions between the two passes (not copy-pasted) specifically so "a
// forming bar's events survive the same bar closing" (2.6 test 5) holds by
// construction rather than by two implementations happening to agree. What
// that promises is set-shaped, not a strict prefix: closed history is a strict
// prefix, and the forming bar's own events all reappear at its close, though a
// structure event the close confirms is emitted ahead of them (B2). Iteration
// 6 tightens "by construction" where the closed loop's rule order broke it:
// with `stopByClose:false` the closed loop replays the forming pass's own
// rules BEFORE stepStructure (I6-F4), and both passes hand the long rules C as
// it stood going into the bar (I6-F1).
function processDay(
  candles: Candle[],
  params: SignalParams,
  refClose: number | null,
  refSource: 'prevClose' | 'prevDayClose' | null,
  forming: boolean,
): DayOutcome {
  const n = candles.length;
  const ts = candles.map((c) => c.ts);
  const highs = candles.map((c) => c.high);
  const lows = candles.map((c) => c.low);
  const closes = candles.map((c) => c.close);
  const volumes = candles.map((c) => c.volume);
  const vwapArr = dayVwap(candles);

  let state: ShortState = 'idle';
  let hod: number | null = null;
  let hodIdx: number | null = null;
  let runLow: number | null = null;
  let runLowIdx: number | null = null;
  let runGainPct: number | null = null;
  let vwapExtPct: number | null = null;
  let gapGainPct: number | null = null;
  let climaxVol = false;
  let swingLow: number | null = null;
  let swingLowIdx: number | null = null;
  let lowerHigh: number | null = null;
  let lowerHighIdx: number | null = null;
  let bounceHigh: number | null = null;
  let pullbackLow: number | null = null;
  // Where the running pullback low was SET (the earliest bar on a tie). The
  // long side's C must come AFTER it (I6-F3): a C that pre-dates the current
  // pullback low is a bounce the price has since fallen through, not the
  // bounce the pullback is now turning up from. Reset with pullbackLow.
  let pullbackLowIdx: number | null = null;
  let vwapLost = false;
  let baseHit = false;
  let entry: Episode | null = null;
  let crackTs: number | null = null;
  // Per-short-episode bookkeeping (iteration 2, B2-B4) — all five reset
  // together on a stop, an invalidation, or any new HOD (stepHod/stepStop):
  //   entryKind    which rule opened the episode ('lh' | 'break' | 'vwap');
  //                exposed on SignalLevels as-is.
  //   entryIsReject a finer split of 'vwap' for the panel's detail text only
  //                (NOT exposed — SignalLevels.entryKind stays the 3-value
  //                type the brief defines): true iff the episode opened via
  //                a VWAP REJECTION (stepVwap's REJECT branch, which also
  //                sets t1Hit true immediately) rather than a plain VWAP
  //                LOSS (stepVwap's LOST branch, entryVwap).
  //   brokeDown    true once the pullback low has been broken THIS episode
  //                (an 'entry'/'breakdown' has fired) — stepEntry's `d`
  //                gate for "don't re-announce the same breakdown".
  //   stopLH       the lower high the CURRENT stop is measured from — the
  //                stop-only-moves-up rule (B2) compares each new confirmed
  //                lower high against this, not against the live `stop`.
  //   t1Hit        true once T1 (VWAP) has fired this episode — VWAP can
  //                cross the hysteresis band many times in one fade; T1 is
  //                "first target", not "every time", so it fires once.
  let entryKind: 'lh' | 'break' | 'vwap' | null = null;
  let entryIsReject = false;
  let brokeDown = false;
  let stopLH: number | null = null;
  let t1Hit = false;
  // ─── iteration 5 — the add caps ───────────────────────────────────────
  //   addsUsed     ADD signals fired THIS short episode — 'ADD (LH)' and the
  //                add form of 'VWAP REJECT'; capped at `maxAdds`. Zeroed
  //                where the episode ends (stepStop, stepHod), which is the
  //                only way one ends, so every entry starts it at 0.
  //   rejectArmed  per DAY, not per episode: may a VWAP REJECT fire? Starts
  //                true, goes false when one fires, and comes back only on a
  //                CLOSE 2 x vwapBandPct under VWAP — price has to leave the
  //                line before its next retest is a new event, not the same
  //                retest printed again a bar or two later.
  let addsUsed = 0;
  let rejectArmed = true;

  // ─── iteration 4 — the LONG side's own state, tracked in parallel ──────
  //   cHigh/cHighIdx  C — the confirmed swing high the short calls
  //                   `lowerHigh`, kept in its OWN variable so a short stop
  //                   (which nulls `lowerHigh`) can't erase the long's
  //                   memory of it. Reset only on a new HOD (stepHod).
  //   higherLow/Idx   D — the confirmed swing low that arms/enters the long.
  //   long.state      'none' | 'armed' | 'long' — the long's own episode
  //                   state, independent of the short's `state`.
  //   longEntry       {idx, price, stop} of the OPEN long episode, or null.
  //   longKind        which rule opened it: 'hl' | 'break' | 'vwap'.
  //   brokeOut        true once THIS episode has seen a breakout above C
  //                   (stepLongTrigger's "don't re-announce" gate, the long
  //                   mirror of the short's `brokeDown`).
  //   t1Long/t2Long   true once T1 (bounce high) / T2 (HOD) has fired this
  //                   episode — a breakout latches t1Long itself (breaking
  //                   C IS reaching T1), so stepLongTarget's own T1 check
  //                   never double-fires on the same bar (see stepLongTrigger).
  //   vwapJustLost    true for the ONE bar the short's stepVwap flips
  //                   `vwapLost` false->true — how stepLongVwap detects
  //                   "VWAP was just lost" for the `longWarn` warning
  //                   without re-deriving the short's own band math.
  //   cHighIn         C exactly as it stood going INTO the current bar — set
  //                   right after stepHod in both passes, BEFORE stepStructure
  //                   can confirm a higher swing high on the closing bar. The
  //                   long's intrabar rules (breakout, T1) read THIS, not
  //                   `cHigh`, so a bar's own close can never raise the level
  //                   out from under a print the forming pass already showed
  //                   (I6-F1).
  let cHigh: number | null = null;
  let cHighIdx: number | null = null;
  let cHighIn: number | null = null;
  let higherLow: number | null = null;
  let higherLowIdx: number | null = null;
  const long: { state: 'none' | 'armed' | 'long' } = { state: 'none' };
  let longEntry: Episode | null = null;
  let longKind: 'hl' | 'break' | 'vwap' | null = null;
  let brokeOut = false;
  let t1Long = false;
  let t2Long = false;
  //   longAddsUsed / holdArmed (iteration 5) are addsUsed / rejectArmed's
  //                   long mirrors: 'ADD (HL)' + the add form of 'VWAP HOLD'
  //                   against `maxAdds` per long episode (zeroed in
  //                   stepLongStop / stepHod), and a per-day re-arm that
  //                   needs a CLOSE 2 x vwapBandPct OVER VWAP after a hold.
  let longAddsUsed = 0;
  let holdArmed = true;
  let vwapJustLost = false;

  const events: SignalEvent[] = [];

  function armInfoAt(idx: number) {
    const start = Math.max(0, idx - params.runLookbackBars);
    let rl = lows[idx]!;
    let rlIdx = idx;
    for (let x = start; x <= idx; x++) {
      if (lows[x]! < rl) {
        rl = lows[x]!;
        rlIdx = x;
      }
    }
    const hodVal = highs[idx]!;
    const rGain = rl > 0 ? ((hodVal - rl) / rl) * 100 : null;
    const vAtHod = vwapArr[idx];
    const vExt = vAtHod != null && vAtHod > 0 ? (hodVal / vAtHod - 1) * 100 : null;
    const gGain = refClose != null && refClose > 0 ? ((hodVal - refClose) / refClose) * 100 : null;
    const runOk = rGain != null && rGain >= params.minSpikePct && (params.minVwapExtPct === 0 || (vExt != null && vExt >= params.minVwapExtPct));
    const gapOk = gGain != null && gGain >= params.minGapPct;
    return {
      runLow: rl,
      runLowIdx: rlIdx,
      runGainPct: rGain,
      vwapExtPct: vExt,
      gapGainPct: gGain,
      armed: runOk || gapOk,
      runArmed: runOk,
      climaxVol: climaxVolume(volumes, idx, params.climaxVolMult),
    };
  }

  // (a) HOD — shared by the closed-bar loop and the forming-bar pass; safe
  // on a forming bar because `high` only ever grows before the bar closes.
  //
  // The ARMING decision is the one part that is NOT safe as-is on a forming
  // bar (I6-F2): HOD, the run low (its window includes the live bar's own low,
  // which only falls) and the gap gain are all monotonic while the bar
  // prints, but `vwapExtPct` is not — the live bar's own typical price and
  // volume move VWAP, so a HOD that cleared the extension gate on a thin
  // early print can fall back under it as the same bar keeps trading, and a
  // SPIKE (with "SPIKING — do not short") shown early would un-happen.
  // `formingBar` therefore restricts arming FROM idle to the monotonic gates
  // — the gap gate, and the run gate when the VWAP gate is disabled
  // (minVwapExtPct 0) — and never demotes an episode already in progress: a
  // forming new-HOD bar inside a run keeps 'spike' instead of flashing "No
  // spike yet" until it closes. The CLOSE re-decides with the full gate, so a
  // bar that only clears it at its close appends its SPIKE then.
  function stepHod(i: number, formingBar = false) {
    if (hod != null && highs[i]! <= hod) return;
    const prevLowerHigh = lowerHigh;
    if (state === 'short') {
      events.push(
        mkEvent(
          ts[i]!,
          'invalidated',
          highs[i]!,
          'NEW HOD',
          `cover — new high of day ${fmtEt(ts[i]!)}` + (prevLowerHigh != null ? ` above the lower high ${fmtPx(prevLowerHigh)}` : ''),
          'buy',
        ),
      );
    } else if (state === 'crack' || state === 'bounce' || state === 'armed') {
      events.push(mkEvent(ts[i]!, 'invalidated', highs[i]!, 'NEW HOD', 'setup reset — new high of day'));
    }
    // iteration 4: a genuinely new HOD while long is open is the long's OWN
    // target — "the top just got taken out again, trail or take profit" —
    // emitted BEFORE the reset below closes the episode (long.state='none'),
    // same "emit first, then reset" order as the short's own `invalidated`
    // just above. Side 'long' (module comment's ABCD section, §4/g2).
    if (long.state === 'long') {
      events.push(mkEvent(ts[i]!, 'longTarget', highs[i]!, 'NEW HOD', `new high of day ${fmtEt(ts[i]!)} — trail or take profit`, 'sell', 'long'));
    }
    hod = highs[i]!;
    hodIdx = i;
    swingLow = null;
    swingLowIdx = null;
    lowerHigh = null;
    lowerHighIdx = null;
    bounceHigh = null;
    pullbackLow = null;
    pullbackLowIdx = null;
    entry = null;
    vwapLost = false;
    baseHit = false;
    crackTs = null;
    entryKind = null;
    entryIsReject = false;
    brokeDown = false;
    stopLH = null;
    t1Hit = false;
    addsUsed = 0; // iteration 5 — a new HOD closes the episode, adds and all (rejectArmed/holdArmed are per DAY and deliberately NOT reset: "price left the line" is a fact about price and VWAP, not about this HOD)
    // iteration 4 — the long side resets too: C and D are both measured
    // FROM the old HOD's post-top structure, exactly as stale as the
    // short's own lowerHigh/trigger/stop the moment a new high prints.
    cHigh = null;
    cHighIdx = null;
    higherLow = null;
    higherLowIdx = null;
    longEntry = null;
    longKind = null;
    brokeOut = false;
    t1Long = false;
    t2Long = false;
    longAddsUsed = 0;
    long.state = 'none';

    const arm = armInfoAt(i);
    runLow = arm.runLow;
    runLowIdx = arm.runLowIdx;
    runGainPct = arm.runGainPct;
    vwapExtPct = arm.vwapExtPct;
    gapGainPct = arm.gapGainPct;
    climaxVol = arm.climaxVol;
    const wasIdle = state === 'idle';
    const gapOk = arm.gapGainPct != null && arm.gapGainPct >= params.minGapPct;
    const monotonicArmed = gapOk || (arm.runArmed && params.minVwapExtPct === 0);
    const armedNow = formingBar ? monotonicArmed || state !== 'idle' : arm.armed;
    state = armedNow ? 'spike' : 'idle';
    if (wasIdle && state === 'spike') {
      const note = arm.runArmed
        ? `+${pct1(runGainPct!)} run from base ${fmtPx(runLow!)} (${pct1(vwapExtPct ?? 0)} above VWAP) — do not short into strength`
        : `+${pct1(gapGainPct!)} gap vs prior close — do not short into strength`;
      events.push(mkEvent(ts[i]!, 'spike', hod, 'SPIKE', note));
    }
  }

  // (d) Entry — shared; safe on a forming bar because `low` only ever falls.
  // Two outcomes, both gated on the SAME trigger break (B2):
  //   armed -> short  the classic entry: `entry` ('SHORT'). Only path left
  //                   when lhEntry:false, since a confirmed lower high with
  //                   lhEntry:true enters straight from stepStructure and
  //                   this bar is already `short` by the time (d) runs.
  //   short, !brokeDown -> stays short  the pullback low breaking WHILE
  //                   already short via a lower high is `breakdown` — the
  //                   confirmation/add the base design's only entry has
  //                   become now that the lower-high close is the primary
  //                   signal. Fires once per episode (brokeDown latches).
  function stepEntry(i: number) {
    if (state !== 'armed' && !(state === 'short' && !brokeDown)) return;
    const trig = computeTrigger(swingLow, pullbackLow, params);
    if (trig == null || !(lows[i]! < trig)) return;
    if (state === 'armed') {
      const stp = computeStop(lowerHigh, hod!, params);
      state = 'short';
      entry = { idx: i, price: trig, stop: stp };
      entryKind = 'break';
      stopLH = lowerHigh;
      brokeDown = true;
      const vAtI = vwapArr[i];
      const brokenLevel = triggerBase(swingLow, pullbackLow)!;
      const note =
        `broke the ${fmtPx(brokenLevel)} pullback low after a lower high at ${fmtPx(lowerHigh!)} — stop ${fmtPx(stp)}` +
        (vAtI != null ? ` · T1 VWAP ${fmtPx(vAtI)}` : '') +
        (runLow != null ? ` · T2 base ${fmtPx(runLow)}` : '');
      events.push(mkEvent(ts[i]!, 'entry', trig, 'SHORT', note, 'sell'));
    } else {
      brokeDown = true;
      const brokenLevel = triggerBase(swingLow, pullbackLow)!;
      events.push(mkEvent(ts[i]!, 'breakdown', trig, 'BREAKDOWN', `broke the ${fmtPx(brokenLevel)} pullback low — confirmation / add`, 'sell'));
    }
  }

  // (d2) iteration 4 — the LONG trigger: the breakout above C, same shape as
  // the short's stepEntry (d) mirrored upward. Safe on a forming bar because
  // `high` only ever grows — provided the level it is judged against is the
  // one the bar STARTED with: `cHighIn`, not `cHigh`, which the closing bar's
  // own stepStructure may already have raised above high[i] (I6-F1).
  //   armed -> long        the classic breakout entry, only reachable when
  //                        hlEntry:false (a higher low only armed, never
  //                        auto-entered).
  //   long, !brokeOut -> stays long   the breakout WHILE already long via a
  //                        higher low is confirmation/add (`breakout`
  //                        'BREAKOUT') — and since clearing C is
  //                        DEFINITIONALLY reaching T1, this latches t1Long
  //                        too (both branches do), so stepLongTarget's own
  //                        T1 check never double-fires on the same bar.
  function stepLongTrigger(i: number) {
    if (cHighIn == null) return;
    const longTrigger = cHighIn * (1 + params.breakBufferPct / 100);
    if (!(highs[i]! > longTrigger)) return;
    if (long.state === 'armed') {
      const stp = higherLow! * (1 - params.stopBufferPct / 100);
      long.state = 'long';
      longEntry = { idx: i, price: longTrigger, stop: stp };
      longKind = 'break';
      brokeOut = true;
      t1Long = true;
      events.push(mkEvent(ts[i]!, 'breakout', longTrigger, 'LONG', `broke above the ${fmtPx(cHighIn)} bounce high — long, stop ${fmtPx(stp)}`, 'buy', 'long'));
    } else if (long.state === 'long' && !brokeOut) {
      brokeOut = true;
      t1Long = true;
      events.push(mkEvent(ts[i]!, 'breakout', longTrigger, 'BREAKOUT', `broke the ${fmtPx(cHighIn)} bounce high — confirmation / add`, 'buy', 'long'));
    }
  }

  // (g) Base target — shared; safe on a forming bar because `low` only ever falls.
  function stepBaseTarget(i: number) {
    if (state !== 'short' || baseHit || runLow == null) return;
    if (!(lows[i]! <= runLow)) return;
    baseHit = true;
    events.push(mkEvent(ts[i]!, 'target', runLow, 'BASE', `reached the pre-spike base ${fmtPx(runLow)} — full-cover zone`, 'buy'));
  }

  // (g2) iteration 4 — LONG targets: T1 the bounce high (C), then T2 the
  // actual HOD (A). A breakout bar already latches t1Long itself (d2), so
  // this never re-announces T1 on a breakout bar. A NEW HOD is handled
  // entirely inside stepHod (2.3a/module comment) — by the time a genuinely
  // new high prints, `hod` itself is about to change and the whole
  // structure resets, so this function only ever sees T2 as "price
  // re-touched today's CURRENT high without exceeding it". Safe on a
  // forming bar because `high` only ever grows; T1 reads `cHighIn` for the
  // same reason (d2) does.
  function stepLongTarget(i: number) {
    if (long.state !== 'long') return;
    if (cHighIn != null && !t1Long && highs[i]! >= cHighIn) {
      t1Long = true;
      events.push(mkEvent(ts[i]!, 'longTarget', cHighIn, 'T1 BOUNCE HIGH', `reached the ${fmtPx(cHighIn)} bounce high — first target`, 'sell', 'long'));
    }
    if (hod != null && !t2Long && highs[i]! >= hod) {
      t2Long = true;
      events.push(mkEvent(ts[i]!, 'longTarget', hod, 'T2 HOD', `reached the high of day ${fmtPx(hod)} — full target`, 'sell', 'long'));
    }
  }

  // (b) Structure — closed bars only (swing confirmation needs k CLOSED bars on both sides).
  // Gated to POST-CRACK states only (crack/bounce/armed/short), not `spike`:
  // a swing low or swing high that prints while still spiking is part of the
  // tight consolidation under HOD, not structure "after the top" (2.2's
  // whole premise) — recording it lets a pre-crack swing low become the
  // trigger (via triggerBase) at a price above where the post-top action is
  // actually happening, and lets a pre-crack lower high get announced with a
  // "short below T" note while the headline still says SPIKING — do not
  // short (F3).
  //
  // Iteration 2 (B2): a confirmed lower high below VWAP behaves completely
  // differently from one above it —
  //   VWAP lost   tracked SILENTLY: the level and (while short) the stop
  //               still move, but nothing is announced. This is the fix for
  //               the real-data "twelve lowerHigh events, every 3-bar bounce
  //               after the fade prints one" spam — below VWAP the visible
  //               re-entry signal is `vwapReject` (stepVwap), not another
  //               lower high.
  //   VWAP intact `lowerHigh` prints (list-only, as iteration 1) and then:
  //     - not yet short, lhEntry on  -> `entryLH` 'SHORT (LH)': the
  //       confirming close IS the entry (this is now the primary signal;
  //       `armed` never has a chance to be a resting state in this case).
  //     - not yet short, lhEntry off -> falls through to the old `armed`
  //       resting state; the confirmed lower high still becomes the trigger
  //       base and stop shown to the trader, but nothing has fired yet.
  //     - already short              -> `entryLH` 'ADD (LH)' — but only up
  //       to `maxAdds` per episode, never once T1 (the VWAP loss) has
  //       printed (below VWAP the re-entry is the VWAP rejection), and never
  //       when the add's own risk to the stop exceeds `maxAddRiskPct`
  //       (iteration 5; a refused add leaves only the list-only `lowerHigh`).
  //       Refused or not, the stop only actually moves when this lower high
  //       is HIGHER than the one it's currently based on (`stopLH`) — never
  //       down, so a smaller bounce after a bigger one can't loosen a
  //       working stop. The risk is measured AFTER that move: the add's
  //       exposure is to the stop it would actually carry.
  function stepStructure(i: number) {
    if ((state !== 'crack' && state !== 'bounce' && state !== 'armed' && state !== 'short') || hodIdx == null || !(i > hodIdx)) return;
    const k = params.swingStrength;
    const j = i - k;
    if (j > hodIdx && j - k >= 0) {
      if (isSwingLow(lows, j, k)) {
        swingLow = lows[j]!;
        swingLowIdx = j;
        bounceHigh = maxHigh(highs, j + 1, i);
        if (state === 'crack') state = 'bounce';

        // ─── iteration 4 — the LONG side's higher low (D) ────────────────
        // Same confirmed-swing-low fact the short side just used for B/the
        // bounce, checked against a SECOND, long-only bar: strictly above
        // the running pullback low (B — "at/below is NOT a higher low, it
        // is the short side's business", §4), confirmed AFTER C exists
        // (`cHighIdx`), and — by default — at/above VWAP at D's OWN bar j
        // (`hlAboveVwap`). `pullbackLow` here is the value as it stands
        // going into bar i (the running min through bar i-1 — see the F1
        // comment on the end-of-loop update below), which already excludes
        // bar j and everything after it up to i-1: "before bar j" for this
        // gate's purpose, since j <= i-k < i.
        //
        // C must also FOLLOW the current pullback low (I6-F3): "C is the
        // confirmed swing high after B" (addendum 3 §3). cHigh is the latest
        // swing high since the HOD and was never retired when price later
        // undercut B, so after a breakdown to a NEW low any swing low above
        // it qualified against a C that pre-dated the low — a long armed on a
        // level the pullback had already fallen through. `pullbackLowIdx` is
        // where the running low was set (the earliest bar on a tie), and D
        // needs `cHighIdx > pullbackLowIdx`. It is safe for D itself: a swing
        // low at j has higher lows at j+1..i, so the running min was set
        // before j whenever lows[j] > pullbackLow.
        if (pullbackLow != null && lows[j]! > pullbackLow && cHighIdx != null && pullbackLowIdx != null && cHighIdx > pullbackLowIdx && j > cHighIdx) {
          const vAtJ = vwapArr[j];
          const qualifies = params.hlAboveVwap ? vAtJ != null && lows[j]! >= vAtJ : true;
          if (qualifies) {
            const d = lows[j]!;
            higherLow = d;
            higherLowIdx = j;
            events.push(
              mkEvent(
                ts[j]!,
                'higherLow',
                d,
                'HIGHER LOW',
                `higher low ${fmtPx(d)} at ${fmtEt(ts[j]!)} above the ${fmtPx(pullbackLow)} pullback low` +
                  (vAtJ != null ? ` (VWAP ${fmtPx(vAtJ)})` : '') +
                  ` — long above ${fmtPx(cHigh!)}`,
                undefined,
                'long',
              ),
            );
            if ((long.state === 'none' || long.state === 'armed') && params.hlEntry) {
              const stp = d * (1 - params.stopBufferPct / 100);
              long.state = 'long';
              longEntry = { idx: i, price: closes[i]!, stop: stp };
              longKind = 'hl';
              brokeOut = false;
              t1Long = false;
              t2Long = false;
              events.push(
                mkEvent(
                  ts[i]!,
                  'entryHL',
                  closes[i]!,
                  'LONG (HL)',
                  `higher low ${fmtPx(d)} confirmed — long at ${fmtPx(closes[i]!)}, stop ${fmtPx(stp)} · T1 bounce high ${fmtPx(cHigh!)} · T2 HOD ${fmtPx(hod!)}`,
                  'buy',
                  'long',
                ),
              );
            } else if (long.state === 'none' && !params.hlEntry) {
              long.state = 'armed';
            } else if (long.state === 'long') {
              // The stop does NOT move (unlike the short's stop-only-up
              // rule) — module comment: trailing on 1-minute noise is wrong
              // on this side too, and a higher low is thinner evidence than
              // a lower high this early in a fade.
              // Iteration 5: the add is capped like the short's — at most
              // `maxAdds` per episode, none once T1 (the bounce high) has
              // been reached (the trade is past its first target), and none
              // whose own risk, close down to the unmoved stop, is over
              // `maxAddRiskPct`.
              const addRiskPct = longEntry != null ? ((closes[i]! - longEntry.stop) / closes[i]!) * 100 : Infinity;
              if (!vwapLost && params.hlEntry && !t1Long && longAddsUsed < params.maxAdds && addRiskPct <= params.maxAddRiskPct) {
                longAddsUsed++;
                events.push(mkEvent(ts[i]!, 'entryHL', closes[i]!, 'ADD (HL)', `new higher low ${fmtPx(d)} — add`, 'buy', 'long'));
              }
            }
          }
        }
      }
      if (isSwingHigh(highs, j, k) && highs[j]! < hod!) {
        const x = highs[j]!;
        lowerHigh = x;
        lowerHighIdx = j;
        // iteration 4 — the long side reuses this SAME confirmed swing high
        // as C (the bounce high), tracked unconditionally in its own
        // variable — C is a structural fact for the long side, not
        // something the short's VWAP-relative noise filter below should
        // hide (see the module comment's ABCD section).
        cHigh = x;
        cHighIdx = j;
        // iteration 4, §8: silence is keyed off the LOWER HIGH'S OWN PRICE
        // vs VWAP at its confirming bar, not the transient `vwapLost` flag
        // a same-bar dip used to flip (module comment has the real-ATYR
        // case this fixes).
        const vAtConfirm = vwapArr[i];
        const silentByVwap = vAtConfirm != null && x < vAtConfirm;
        if (silentByVwap) {
          // Silent: no lowerHigh event, no add — but the stop still trails
          // up while short, same "only ever moves up" rule as the visible
          // path, just without printing anything (the comment block above).
          if (state === 'short' && (stopLH == null || x > stopLH)) {
            stopLH = x;
            const stp = x * (1 + params.stopBufferPct / 100);
            if (entry) entry = { ...entry, stop: stp };
          }
        } else {
          const retrace = computeRetrace(bounceHigh, pullbackLow, hod);
          const base = `lower high ${fmtPx(x)} at ${fmtEt(ts[j]!)} vs HOD ${fmtPx(hod!)} (retraced ${pct1(retrace ?? 0)} of the drop)`;
          if (state === 'crack' || state === 'bounce') state = 'armed';
          if (state === 'armed' && params.lhEntry) {
            const stp = x * (1 + params.stopBufferPct / 100);
            state = 'short';
            entry = { idx: i, price: closes[i]!, stop: stp };
            entryKind = 'lh';
            entryIsReject = false;
            stopLH = x;
            brokeDown = false;
            t1Hit = false;
            events.push(mkEvent(ts[j]!, 'lowerHigh', x, 'LOWER HIGH', base));
            events.push(
              mkEvent(ts[i]!, 'entryLH', closes[i]!, 'SHORT (LH)', `lower high ${fmtPx(x)} confirmed — short at ${fmtPx(closes[i]!)}, stop ${fmtPx(stp)} · T1 VWAP ${vwapArr[i] != null ? fmtPx(vwapArr[i]!) : '?'} · T2 base ${runLow != null ? fmtPx(runLow) : '?'}`, 'sell'),
            );
          } else if (state === 'short') {
            events.push(mkEvent(ts[j]!, 'lowerHigh', x, 'LOWER HIGH', `${base} — stop trails up, never down`));
            if (stopLH == null || x > stopLH) {
              stopLH = x;
              const stp = x * (1 + params.stopBufferPct / 100);
              if (entry) entry = { ...entry, stop: stp };
            }
            // Iteration 5's three add gates, all evaluated against the stop
            // as it stands AFTER the move-up just above (see the block
            // comment on this function).
            const addRiskPct = entry != null ? ((entry.stop - closes[i]!) / closes[i]!) * 100 : Infinity;
            if (params.lhEntry && !t1Hit && addsUsed < params.maxAdds && addRiskPct <= params.maxAddRiskPct) {
              addsUsed++;
              events.push(mkEvent(ts[i]!, 'entryLH', closes[i]!, 'ADD (LH)', `new lower high ${fmtPx(x)} — add; stop ${fmtPx(entry!.stop)}`, 'sell'));
            }
          } else {
            // armed, lhEntry:false — the old resting state: announce the
            // structure, trigger/stop stay reactive (computed from
            // swingLow/lowerHigh/hod at the end of processDay).
            const trig = computeTrigger(swingLow, pullbackLow, params);
            events.push(mkEvent(ts[j]!, 'lowerHigh', x, 'LOWER HIGH', `${base} — short below ${trig != null ? fmtPx(trig) : '?'}`));
          }
        }
      }
    }
    if (swingLowIdx != null) {
      bounceHigh = bounceHigh == null ? highs[i]! : Math.max(bounceHigh, highs[i]!);
    }
  }

  // (c) Crack — closed bars only (needs the prior CLOSED bar's low).
  function stepCrack(i: number) {
    if (state !== 'spike' || hodIdx == null || !(i > hodIdx)) return;
    const pullbackPct = ((hod! - lows[i]!) / hod!) * 100;
    const closeBelowPriorLow = closes[i]! < lows[i - 1]!;
    if (!closeBelowPriorLow && pullbackPct < params.minPullbackPct) return;
    state = 'crack';
    crackTs = ts[i]!;
    const note = closeBelowPriorLow
      ? `first close below the prior bar's low, ${pct1(pullbackPct)} off the high — wait for a lower high`
      : `first pullback of ${pct1(pullbackPct)} off the high — wait for a lower high`;
    events.push(mkEvent(ts[i]!, 'crack', closes[i]!, 'CRACK', note));
  }

  // (e) VWAP rules — closed bars only (close-based). Iteration 2 (B4)
  // replaces the old bare `close < vwap` CROSS test with a `vwapBandPct`
  // HYSTERESIS band: real closes ride within a few tenths of a percent of a
  // cumulative VWAP for minutes at a time, and a bare cross flips LOST and
  // RECLAIMED on alternate bars (four of each in eight real minutes on one
  // fixture) — the band needs a real move to flip state, so it needs no
  // prior-bar comparison at all (LOST/RECLAIMED no longer look at vPrev).
  function stepVwap(i: number) {
    if (state !== 'crack' && state !== 'bounce' && state !== 'armed' && state !== 'short') return;
    const vCur = vwapArr[i];
    if (vCur == null) return;
    const lossLevel = vCur * (1 - params.vwapBandPct / 100);
    const reclaimLevel = vCur * (1 + params.vwapBandPct / 100);
    // Iteration 5 — the REJECT re-arm. A reject that just fired stays spent
    // until price has LEFT the line: a CLOSE 2 x the band under VWAP (twice
    // the distance the LOST rule needs, so a rejection that only nudges the
    // line can't re-arm itself). `rejectWasArmed` is the flag as it stood
    // going INTO this bar, so a bar's own deep close can neither re-arm nor
    // pre-arm its own rejection — the fired branch below re-spends it.
    const rejectWasArmed = rejectArmed;
    if (closes[i]! < vCur * (1 - (2 * params.vwapBandPct) / 100)) rejectArmed = true;

    if (!vwapLost && closes[i]! < lossLevel) {
      vwapLost = true;
      vwapJustLost = true; // iteration 4: stepLongVwap's cue for `longWarn`
      if (state === 'short') {
        // T1 is "first target reached", not "every re-loss" — a fade can
        // cross the band many times in one episode; only the first counts.
        if (!t1Hit) {
          t1Hit = true;
          events.push(mkEvent(ts[i]!, 'target', vCur, 'T1 VWAP', `closed below VWAP ${fmtPx(vCur)} (${pct1(params.vwapBandPct)} band) — first target`, 'buy'));
        }
      } else if (params.vwapLossEntry && lowerHigh != null) {
        // Only once a lower high exists — never against the raw HOD (the
        // 35%-risk, mid-spike entry the real fixtures showed, B1/B4).
        const stp = lowerHigh * (1 + params.stopBufferPct / 100);
        state = 'short';
        entry = { idx: i, price: closes[i]!, stop: stp };
        entryKind = 'vwap';
        entryIsReject = false;
        stopLH = lowerHigh;
        events.push(
          mkEvent(
            ts[i]!,
            'entryVwap',
            vCur,
            'SHORT (VWAP)',
            `closed below VWAP ${fmtPx(vCur)} after the top, with a lower high at ${fmtPx(lowerHigh)} — stop ${fmtPx(stp)}`,
            'sell',
          ),
        );
      }
      return;
    }
    if (vwapLost && closes[i]! > reclaimLevel) {
      vwapLost = false;
      events.push(mkEvent(ts[i]!, 'vwapReclaim', vCur, 'VWAP RECLAIMED', `closed back above VWAP ${fmtPx(vCur)} (${pct1(params.vwapBandPct)} band) — fade weakening`));
      return;
    }
    if (vwapLost && i > 0) {
      const vPrev = vwapArr[i - 1];
      if (rejectWasArmed && vPrev != null && highs[i]! >= vCur && closes[i]! < vCur && highs[i - 1]! < vPrev) {
        if (state === 'short') {
          // The add form counts against `maxAdds`. Past the cap nothing is
          // emitted at all — there is no separate structure event to leave
          // list-only here, the rejection IS the add — and the re-arm is
          // NOT spent: only a signal that actually fired consumes it.
          if (addsUsed < params.maxAdds) {
            addsUsed++;
            rejectArmed = false;
            events.push(mkEvent(ts[i]!, 'vwapReject', vCur, 'VWAP REJECT', `tested VWAP ${fmtPx(vCur)} from below and closed under it — add`, 'sell'));
          }
        } else {
          // The entry form is never counted as an add, but it does spend the
          // re-arm like any other rejection.
          rejectArmed = false;
          // Below VWAP, the re-entry is the rejection itself (a lower high
          // is tracked silently down here, per stepStructure) — stop sits
          // above THIS bar's high, since the pullback low is typically far
          // above price by now and no breakdown add applies (brokeDown:true
          // pre-empts stepEntry re-announcing one).
          const stp = highs[i]! * (1 + params.stopBufferPct / 100);
          state = 'short';
          entry = { idx: i, price: closes[i]!, stop: stp };
          entryKind = 'vwap';
          entryIsReject = true;
          stopLH = highs[i]!;
          brokeDown = true;
          t1Hit = true; // already below VWAP at entry — there is no separate T1 to reach
          events.push(
            mkEvent(
              ts[i]!,
              'vwapReject',
              vCur,
              'SHORT (VWAP REJECT)',
              `tested VWAP ${fmtPx(vCur)} from below and closed under it — short, stop above the rejection bar ${fmtPx(stp)}`,
              'sell',
            ),
          );
        }
      }
    }
  }

  // (e) iteration 4 — the LONG VWAP rules, run AFTER the short's own (same
  // bar), reading `vwapLost`/`vwapJustLost` exactly as the short's stepVwap
  // just left them. Two INDEPENDENT checks (mutually exclusive by close
  // value in practice — HOLD needs close > vwap, LOST needs close well
  // under it):
  //   HOLD   a dip that touches VWAP and closes back above it (first touch
  //          since the last loss) — the long's version of a successful
  //          retest. 'armed' -> entry ('LONG (VWAP HOLD)'); 'long' -> add
  //          ('VWAP HOLD'). Uses the raw `vwap[i]` cross, not the
  //          hysteresis band — a HOLD is defined by actually closing back
  //          ABOVE the line, not by clearing some looser threshold under it.
  //   LOST   a WARNING only, list-only, no marker — losing VWAP does not
  //          close a long (only the long's own stop, f2, does); it just
  //          flags rising fade risk while the position stays open.
  function stepLongVwap(i: number) {
    const vCur = vwapArr[i];
    if (vCur == null || i === 0) return;
    const vPrev = vwapArr[i - 1];
    // Iteration 5 — the HOLD re-arm, the mirror of stepVwap's REJECT one: a
    // hold that fired stays spent until a CLOSE 2 x the band OVER VWAP, and
    // the flag is read as it stood going into this bar (`holdWasArmed`).
    const holdWasArmed = holdArmed;
    if (closes[i]! > vCur * (1 + (2 * params.vwapBandPct) / 100)) holdArmed = true;
    if (holdWasArmed && !vwapLost && vPrev != null && lows[i]! <= vCur && closes[i]! > vCur && lows[i - 1]! > vPrev) {
      if (long.state === 'long') {
        // The add form counts against `maxAdds`; past the cap nothing is
        // emitted and the re-arm is not spent (see stepVwap's REJECT).
        if (longAddsUsed < params.maxAdds) {
          longAddsUsed++;
          holdArmed = false;
          events.push(mkEvent(ts[i]!, 'vwapHold', vCur, 'VWAP HOLD', `dipped to VWAP ${fmtPx(vCur)} and held — add`, 'buy', 'long'));
        }
      } else if (long.state === 'armed') {
        holdArmed = false;
        const stp = lows[i]! * (1 - params.stopBufferPct / 100);
        long.state = 'long';
        longEntry = { idx: i, price: closes[i]!, stop: stp };
        longKind = 'vwap';
        brokeOut = false;
        t1Long = false;
        t2Long = false;
        events.push(
          mkEvent(ts[i]!, 'vwapHold', closes[i]!, 'LONG (VWAP HOLD)', `held VWAP ${fmtPx(vCur)} on the dip — long at ${fmtPx(closes[i]!)}, stop ${fmtPx(stp)}`, 'buy', 'long'),
        );
      }
    }
    if (vwapJustLost && long.state === 'long') {
      events.push(mkEvent(ts[i]!, 'longWarn', vCur, 'VWAP LOST', `closed below VWAP ${fmtPx(vCur)} while long — fade risk; stop ${fmtPx(longEntry ? longEntry.stop : 0)}`, undefined, 'long'));
    }
  }

  // (f) Stop — `stopByClose` (default true) needs a CLOSE above the stop,
  // matching every other close-based rule; false is a hard intrabar stop
  // order (any HIGH at/above it) and, because `high` only ever grows before
  // a bar closes, is also safe to check on the forming bar (2.4/B3).
  function stepStop(i: number) {
    if (state !== 'short' || entry == null) return;
    const hit = params.stopByClose ? closes[i]! > entry.stop : highs[i]! >= entry.stop;
    if (!hit) return;
    const verb = params.stopByClose ? 'closed above' : 'traded at/above';
    events.push(mkEvent(ts[i]!, 'stopped', entry.stop, 'STOP', `${verb} the stop ${fmtPx(entry.stop)} — cover`, 'buy'));
    entry = null;
    lowerHigh = null;
    lowerHighIdx = null;
    entryKind = null;
    entryIsReject = false;
    brokeDown = false;
    stopLH = null;
    t1Hit = false;
    addsUsed = 0;
    state = swingLow != null ? 'bounce' : 'crack';
  }

  // (f2) iteration 4 — the LONG stop: mirrors stepStop but inverted (a
  // long's stop sits BELOW entry, so it triggers on weakness). Unlike the
  // short's lower-high stop, this level NEVER moves once set — see the
  // module comment. Safe on a forming bar only when `!stopByClose` (a hard
  // stop by `low`, which only ever falls before the bar closes — the same
  // reasoning as the short's own stopByClose:false path, B3).
  function stepLongStop(i: number) {
    if (long.state !== 'long' || longEntry == null) return;
    const hit = params.stopByClose ? closes[i]! < longEntry.stop : lows[i]! <= longEntry.stop;
    if (!hit) return;
    const verb = params.stopByClose ? 'closed below' : 'traded at/below';
    events.push(mkEvent(ts[i]!, 'longStopped', longEntry.stop, 'STOP (LONG)', `${verb} the stop ${fmtPx(longEntry.stop)} — cover the long`, 'sell', 'long'));
    longEntry = null;
    higherLow = null;
    higherLowIdx = null;
    longKind = null;
    brokeOut = false;
    t1Long = false;
    t2Long = false;
    longAddsUsed = 0;
    long.state = 'none';
  }

  // Is C still the bounce the pullback is turning up from — i.e. did it form
  // AFTER the current pullback low B (I6-F3)? A closure, not an inline test,
  // for the same reason as `stateIn`: cHigh/cHighIdx are only ever assigned
  // inside closures, so read directly in the outer scope TypeScript would see
  // their declaration's `null`.
  function cFollowsPullbackLow(): boolean {
    return cHigh != null && cHighIdx != null && pullbackLowIdx != null && cHighIdx > pullbackLowIdx;
  }

  const lastClosedIdx = forming ? n - 2 : n - 1;
  for (let i = 0; i <= lastClosedIdx; i++) {
    vwapJustLost = false; // iteration 4: reset each bar — see stepVwap/stepLongVwap
    stepHod(i);
    cHighIn = cHigh; // C as it stands going INTO bar i — before stepStructure below can raise it (I6-F1)
    if (!params.stopByClose) {
      // Hard-stop mode (I6-F4): replay, in the forming pass's own order, every
      // rule that pass runs — with the stops and levels as they stood going
      // into this bar — BEFORE stepStructure. Otherwise stepStructure could
      // confirm a swing high at i-k (always above high[i]) and lift the stop
      // over a bar that had already traded through the resting one, cancelling
      // a STOP the forming pass showed; or emit its `lowerHigh` ahead of an
      // event the forming pass ended on. Every rule here is idempotent within a
      // bar (state and latch guards), so the regular calls further down are
      // no-ops for anything this replay already fired and only add what the
      // close itself creates (an episode opened this very bar).
      stepEntry(i);
      stepLongTrigger(i);
      stepBaseTarget(i);
      stepLongTarget(i);
      stepStop(i);
      stepLongStop(i);
      stepBaseTarget(i);
      stepLongTarget(i);
    }
    stepStructure(i);
    stepCrack(i);
    stepEntry(i);
    stepLongTrigger(i); // (d2)
    // (g)/(g2) right after (d)/(d2), same order as the forming pass: a
    // BASE/T1/T2 target touched intrabar must survive this same bar going
    // on to close above/below its stop. Running these again after (f)/(f2)
    // is a deliberate second call — both are idempotent (baseHit/t1Hit/
    // t1Long/t2Long latches), so it's a no-op unless (e) just opened a
    // fresh episode whose own low/high had already reached a target (F4).
    stepBaseTarget(i);
    stepLongTarget(i);
    stepVwap(i);
    stepLongVwap(i);
    stepStop(i);
    stepLongStop(i); // (f2)
    stepBaseTarget(i);
    stepLongTarget(i);
    // pullbackLow (running) = min low over (hodIdx, i]. Updated at the END
    // of the bar's iteration, AFTER every rule above has run for bar i, so
    // rule (b)/(d) on bar i see the running min over (hodIdx, i-1] — the
    // same view the forming pass has of the SAME bar (2.4 folds nothing into
    // pullbackLow before its own stepEntry either). Folding lows[i] in
    // earlier meant that once a bar armed with no confirmed swingLow, the
    // trigger (swingLow ?? pullbackLow) would already include that bar's own
    // low, so `low[i] < trigger` could never be true — the entry would slide
    // down under price forever instead of ever firing (F1). Not named in the
    // HOD clear-list (2.3a) explicitly, but it must reset there too: its own
    // window definition is relative to the CURRENT hodIdx, so a stale value
    // from before a new HOD would be measuring the wrong range. `null` on
    // the HOD bar itself (the window (hodIdx, hodIdx] is empty) is exactly
    // that reset, achieved for free by the `i > hodIdx` guard below.
    if (hodIdx != null && i > hodIdx && (pullbackLow == null || lows[i]! < pullbackLow)) {
      pullbackLow = lows[i]!;
      pullbackLowIdx = i; // strict <: a double bottom keeps the EARLIEST bar
    }
  }

  // 2.4 Forming-bar pass: (a) HOD-by-high, (d) entry-by-low, (g) base-by-low
  // — each uses an intrabar extreme that can only grow more true, never
  // un-happen, while the bar keeps printing. No swing confirmation, no
  // crack-by-close, no VWAP close rule. Iteration 2 (B3): when
  // `stopByClose:false` the stop check joins this set too — `high` is just
  // as monotonic as `low`, so a hard stop order is exactly as safe to
  // check early; run it (and re-run base-by-low after it, same F4 reason
  // as the closed-bar loop: a base touched by this bar's `low` must not
  // un-happen because this bar's `high` also stopped it out).
  // Iteration 4: (d2) the long breakout and (g2) the long's T1/T2 targets
  // join unconditionally — both read `high`, exactly as monotonic as (a)
  // and (g); (f2) the long stop joins only when `!stopByClose`, same
  // reasoning as the short's own (f) above (it reads `low`, not `close`).
  // Iteration 6: (a) arms a fresh SPIKE from idle on the monotonic gates only
  // (stepHod's `formingBar`, I6-F2), and (d2)/(g2) read `cHighIn` (I6-F1).
  if (forming) {
    const i = n - 1;
    stepHod(i, true); // arms from idle on the monotonic gates only (I6-F2)
    cHighIn = cHigh; // same C the closed loop hands the long rules for this bar (I6-F1)
    stepEntry(i);
    stepLongTrigger(i);
    stepBaseTarget(i);
    stepLongTarget(i);
    if (!params.stopByClose) {
      stepStop(i);
      stepLongStop(i);
      stepBaseTarget(i);
      stepLongTarget(i);
    }
    // Display-only: fold the forming bar's own low into pullbackLow so
    // levels.pullbackLow / evidence.pullbackPct read "lowest low since HOD
    // INCLUDING the live bar" while the bar is printing. stepEntry/
    // stepBaseTarget above have already run against the pre-fold value (the
    // same running min the closed pass would have going into this bar), so
    // this has no effect on events or state — display only.
    if (hodIdx != null && i > hodIdx && (pullbackLow == null || lows[i]! < pullbackLow)) {
      pullbackLow = lows[i]!;
      pullbackLowIdx = i; // strict <: a double bottom keeps the EARLIEST bar
    }
  }

  const lastIdx = n - 1;
  const entryBox = widenEntry(entry);
  const trigger = stateIn(state, 'armed', 'short') ? computeTrigger(swingLow, pullbackLow, params) : null;
  const stop = stateIn(state, 'armed') ? computeStop(lowerHigh, hod!, params) : stateIn(state, 'short') && entryBox ? entryBox.stop : null;
  const entryPrice = entryBox ? entryBox.price : null;
  const entryTs = entryBox ? ts[entryBox.idx]! : null;
  const pullbackPct = hod != null && pullbackLow != null ? ((hod - pullbackLow) / hod) * 100 : null;
  const retracePct = computeRetrace(bounceHigh, pullbackLow, hod);
  const barsSinceHod = hodIdx != null ? lastIdx - hodIdx : null;
  const riskPct = stateIn(state, 'armed')
    ? trigger != null && stop != null
      ? ((stop - trigger) / trigger) * 100
      : null
    : stateIn(state, 'short') && entryPrice != null && stop != null
      ? ((stop - entryPrice) / entryPrice) * 100
      : null;

  // iteration 4 — the LONG side's own display levels, same shape as the
  // short's just above: `longStop` is the OPEN episode's actual stop
  // (whichever rule set it — hl/break/vwap each use a different formula,
  // see stepStructure/stepLongTrigger/stepLongVwap) while long, falling
  // back to the prospective D-based stop while only `armed`.
  const longEntryBox = widenEntry(longEntry);
  // What the chart and panel may SHOW as C (I6-F3): a C that pre-dates the
  // current pullback low is stale — the long can no longer arm on it, so the
  // level (and "a swing low above B arms the long") would be false — unless a
  // long is already open on it, whose own BREAKOUT and T1 legitimately keep
  // the entry-time C. The rules themselves (stepLongTrigger/Target) never
  // look at this: they read `cHighIn`.
  const cShown = long.state !== 'none' || cFollowsPullbackLow() ? cHigh : null;
  const cShownTs = cShown != null && cHighIdx != null ? ts[cHighIdx]! : null;
  const longTrigger = cShown != null ? cShown * (1 + params.breakBufferPct / 100) : null;
  const longStop = longEntryBox != null ? longEntryBox.stop : long.state === 'armed' && higherLow != null ? higherLow * (1 - params.stopBufferPct / 100) : null;
  const longEntryPrice = longEntryBox ? longEntryBox.price : null;
  const longEntryTs = longEntryBox ? ts[longEntryBox.idx]! : null;

  const levels: SignalLevels = {
    hod,
    hodTs: hodIdx != null ? ts[hodIdx]! : null,
    runLow,
    runLowTs: runLowIdx != null ? ts[runLowIdx]! : null,
    pullbackLow,
    swingLow,
    swingLowTs: swingLowIdx != null ? ts[swingLowIdx]! : null,
    lowerHigh,
    lowerHighTs: lowerHighIdx != null ? ts[lowerHighIdx]! : null,
    trigger,
    stop,
    vwap: vwapArr[lastIdx] ?? null,
    entryPrice,
    entryTs,
    entryKind: stateIn(state, 'short') ? entryKind : null,
    higherLow,
    higherLowTs: higherLowIdx != null ? ts[higherLowIdx]! : null,
    bounceHigh: cShown,
    longTrigger,
    longStop,
    longEntryPrice,
    longEntryTs,
    longEntryKind: long.state === 'long' ? longKind : null,
  };
  const evidence: SignalEvidence = {
    runGainPct,
    gapGainPct,
    vwapExtPct,
    climaxVol,
    pullbackPct,
    retracePct,
    barsSinceHod,
    riskPct,
    refClose,
    refSource,
  };

  // iteration 4 — combine the short's own (unchanged) state machine with
  // the long's independent one into the public SignalState: 'both' when
  // both episodes are open, 'short'/'long' when exactly one is, 'armed'
  // when neither is open but at least one side is armed (with its
  // confirm-to-enter param off), otherwise the short's shared prefix
  // (idle/spike/crack/bounce), since that prefix IS the shared spike-top
  // context both sides read (module comment).
  const shortOpen = stateIn(state, 'short');
  const shortArmed = stateIn(state, 'armed');
  const longOpen = long.state === 'long';
  const overallState: SignalState = shortOpen && longOpen ? 'both' : shortOpen ? 'short' : longOpen ? 'long' : shortArmed || long.state === 'armed' ? 'armed' : state;

  const shortSubState: 'none' | 'armed' | 'short' = shortOpen ? 'short' : shortArmed ? 'armed' : 'none';
  const shortHD = buildHeadlineDetail(state, levels, evidence, params, crackTs, bounceHigh, vwapLost, baseHit, entryIsReject, addsUsed);
  const longHD = buildLongHeadlineDetail(long.state, levels, params, cShown, cShownTs, t1Long, t2Long, longAddsUsed);
  const { headline, detail } = combineHeadlineDetail(overallState, shortHD, longHD, cShown, { entryPrice, stop }, { entryPrice: longEntryPrice, stop: longStop });

  return {
    events,
    state: overallState,
    levels,
    evidence,
    headline,
    detail,
    short: { state: shortSubState, headline: shortHD.headline, detail: shortHD.detail },
    long: { state: long.state, headline: longHD.headline, detail: longHD.detail },
  };
}

// ─── 2.5 Headline + detail ───────────────────────────────────────────────

function buildHeadlineDetail(
  state: ShortState,
  levels: SignalLevels,
  evidence: SignalEvidence,
  params: SignalParams,
  crackTs: number | null,
  bounceHigh: number | null,
  vwapLost: boolean,
  baseHit: boolean,
  entryIsReject: boolean,
  addsUsed: number,
): { headline: string; detail: string[] } {
  const px = (v: number | null) => (v == null ? '?' : fmtPx(v));
  const et = (v: number | null) => (v == null ? '?' : fmtEt(v));
  const pc = (v: number | null) => pct1(v ?? 0);
  // `levels.entryKind` is the 3-value public type ('lh'|'break'|'vwap'); the
  // panel's wording needs a 4th distinction the type doesn't carry — a
  // 'vwap' entry can be a plain VWAP LOSS (entryVwap) or a VWAP REJECTION
  // (the "SHORT (VWAP REJECT)" branch of stepVwap) — so `entryIsReject`
  // (processDay-internal only, not on SignalLevels) breaks that tie for
  // display purposes only; it changes no other behaviour.
  const entryKindWord =
    levels.entryKind === 'lh'
      ? 'lower high'
      : levels.entryKind === 'break'
        ? 'breakdown'
        : levels.entryKind === 'vwap'
          ? entryIsReject
            ? 'VWAP rejection'
            : 'VWAP loss'
          : '?';

  switch (state) {
    case 'idle':
      return {
        headline: 'No spike yet',
        detail: [`Waiting for a run of ≥${params.minSpikePct}% that stretches ≥${params.minVwapExtPct}% above VWAP (or a ≥${params.minGapPct}% gap)`],
      };
    case 'spike':
      return {
        headline: 'SPIKING — do not short',
        detail: [
          `+${pc(evidence.runGainPct)} from the ${px(levels.runLow)} base, ${pc(evidence.vwapExtPct)} above VWAP`,
          `HOD ${px(levels.hod)} at ${et(levels.hodTs)}` + (evidence.climaxVol ? ' · climax volume' : ''),
          `Waiting for the first crack: a close below the prior bar's low or a ${pct1(params.minPullbackPct)} pullback`,
        ],
      };
    case 'crack':
      return {
        headline: 'TOP FORMING — wait for a lower high',
        detail: [
          `First crack ${et(crackTs)}, ${pc(evidence.pullbackPct)} off the high`,
          `Pullback low ${px(levels.pullbackLow)} · HOD ${px(levels.hod)}`,
          `A bounce that fails below ${px(levels.hod)} arms the short`,
        ],
      };
    case 'bounce':
      return {
        headline: 'BOUNCING — a lower high arms the short',
        detail: [
          `Bounce high ${px(bounceHigh)} so far (retraced ${pc(evidence.retracePct)} of the drop)`,
          `Pullback low ${px(levels.pullbackLow)} · HOD ${px(levels.hod)}`,
          `A confirmed swing high below ${px(levels.hod)} is the lower high`,
        ],
      };
    case 'armed':
      // Only reachable at all when lhEntry:false — with lhEntry on, a
      // confirmed lower high enters straight from `armed` in the same step
      // (B6), so this headline is transient-to-nonexistent in that mode.
      return {
        headline: `ARMED — short below ${px(levels.trigger)}`,
        detail: [
          `Lower high ${px(levels.lowerHigh)} at ${et(levels.lowerHighTs)} (retraced ${pc(evidence.retracePct)})`,
          `Trigger ${px(levels.trigger)} (pullback low ${px(triggerBase(levels.swingLow, levels.pullbackLow))} − ${pct1(params.breakBufferPct)})`,
          `Stop ${px(levels.stop)} (lower high +${pct1(params.stopBufferPct)}) · risk ${pc(evidence.riskPct)}`,
          `T1 VWAP ${px(levels.vwap)} (${pct1(params.vwapBandPct)} band) · T2 base ${px(levels.runLow)}`,
        ],
      };
    case 'short': {
      const stopWord = params.stopByClose ? 'A close above' : 'A print at/above';
      return {
        headline: `SHORT signalled ${et(levels.entryTs)} at ${px(levels.entryPrice)}`,
        detail: [
          `Entered ${et(levels.entryTs)} at ${px(levels.entryPrice)} (${entryKindWord}) · stop ${px(levels.stop)} · risk ${pc(evidence.riskPct)}`,
          `T1 VWAP ${px(levels.vwap)} (${pct1(params.vwapBandPct)} band)${vwapLost ? ' ✓' : ''} · T2 base ${px(levels.runLow)}${baseHit ? ' ✓' : ''}`,
          `${stopWord} ${px(levels.stop)} stops; a new HOD invalidates`,
          // Iteration 5: once the cap is reached, say so as the LAST line —
          // otherwise a trader watching an ADD marker stop appearing can't
          // tell "the engine sees nothing" from "the engine is done adding".
          ...(addsUsed >= params.maxAdds ? [`Adds used ${params.maxAdds}/${params.maxAdds} — no more adds this trade (a new trade after a stop can still open on a VWAP rejection)`] : []),
        ],
      };
    }
  }
}

// iteration 4 — the LONG side's own headline/detail (section 5). `cHigh`/
// `cHighTs` are passed in separately from `levels.bounceHigh`/nothing
// because "C exists" (armed on the SHORT SIDE's shared prefix, before any
// long structure) needs to be checkable even when `long.state` is still
// 'none' — `levels.bounceHigh` already IS `cHigh`, this parameter is just
// the readable name for that branch.
function buildLongHeadlineDetail(
  longState: 'none' | 'armed' | 'long',
  levels: SignalLevels,
  params: SignalParams,
  cHigh: number | null,
  cHighTs: number | null,
  t1Long: boolean,
  t2Long: boolean,
  longAddsUsed: number,
): { headline: string; detail: string[] } {
  const px = (v: number | null) => (v == null ? '?' : fmtPx(v));
  const et = (v: number | null) => (v == null ? '?' : fmtEt(v));
  const pc = (v: number | null) => pct1(v ?? 0);
  const riskPct = (entryLike: number | null, stopLike: number | null) => (entryLike != null && stopLike != null ? ((entryLike - stopLike) / entryLike) * 100 : null);

  switch (longState) {
    case 'none':
      if (cHigh == null) {
        return { headline: 'Long: no higher low yet', detail: ['Needs a bounce high, then a pullback that holds above B and VWAP'] };
      }
      return {
        headline: 'Long: waiting for a higher low above B',
        detail: [`Bounce high ${px(cHigh)} at ${et(cHighTs)} · pullback low ${px(levels.pullbackLow)}`, `A swing low above ${px(levels.pullbackLow)} (and VWAP ${px(levels.vwap)}) arms the long`],
      };
    case 'armed':
      return {
        headline: `LONG ARMED — buy above ${px(levels.bounceHigh)}`,
        detail: [
          `Higher low ${px(levels.higherLow)} at ${et(levels.higherLowTs)}`,
          `Trigger ${px(levels.longTrigger)} · stop ${px(levels.longStop)} · risk ${pc(riskPct(levels.longTrigger, levels.longStop))}`,
          `T1 bounce high ${px(levels.bounceHigh)} · T2 HOD ${px(levels.hod)}`,
        ],
      };
    case 'long': {
      const kindWord = levels.longEntryKind === 'hl' ? 'higher low' : levels.longEntryKind === 'break' ? 'breakout' : levels.longEntryKind === 'vwap' ? 'VWAP hold' : '?';
      return {
        headline: `LONG signalled ${et(levels.longEntryTs)} at ${px(levels.longEntryPrice)} (${kindWord})`,
        detail: [
          `Stop ${px(levels.longStop)} · risk ${pc(riskPct(levels.longEntryPrice, levels.longStop))}`,
          `T1 bounce high ${px(levels.bounceHigh)}${t1Long ? ' ✓' : ''} · T2 HOD ${px(levels.hod)}${t2Long ? ' ✓' : ''}`,
          `A close below ${px(levels.longStop)} stops; VWAP loss is a warning`,
          // Iteration 5: the short side's cap line, long wording (the long
          // has no "next re-entry" of its own to name).
          ...(longAddsUsed >= params.maxAdds ? [`Adds used ${params.maxAdds}/${params.maxAdds}`] : []),
        ],
      };
    }
  }
}

// iteration 4 — combine the short's and the long's own headline/detail into
// the public top-level fields (section 5): when only one side is open, its
// own read IS the top-level read; when both are, a two-sided summary;
// otherwise (nothing open on either side yet) the short's rich narrative —
// it IS the shared spike-top story both sides are reading — with the
// long's own first detail line appended once C exists, so the panel hints
// at the long setup without needing its own headline slot yet.
function combineHeadlineDetail(
  overallState: SignalState,
  shortHD: { headline: string; detail: string[] },
  longHD: { headline: string; detail: string[] },
  cHigh: number | null,
  short: { entryPrice: number | null; stop: number | null },
  long: { entryPrice: number | null; stop: number | null },
): { headline: string; detail: string[] } {
  if (overallState === 'short') return shortHD;
  if (overallState === 'long') return longHD;
  if (overallState === 'both') {
    return {
      headline: `TWO-SIDED — short ${fmtPx(short.entryPrice!)} (stop ${fmtPx(short.stop!)}) vs long ${fmtPx(long.entryPrice!)} (stop ${fmtPx(long.stop!)})`,
      detail: [`Breakout above ${fmtPx(cHigh!)} favours the long · breakdown below the pullback low favours the short`, shortHD.detail[0]!, longHD.detail[0]!],
    };
  }
  return { headline: shortHD.headline, detail: cHigh != null ? [...shortHD.detail, longHD.detail[0]!] : shortHD.detail };
}

// ─── 2.6 top-level entry point ───────────────────────────────────────────

export function evaluate(input: EvalInput): SignalResult {
  const params: SignalParams = { ...DEFAULT_PARAMS, ...input.params };
  const candles = input.candles;

  if (candles.length === 0) {
    const shortHD = buildHeadlineDetail('idle', emptyLevels(), emptyEvidence(), params, null, null, false, false, false, 0);
    const longHD = buildLongHeadlineDetail('none', emptyLevels(), params, null, null, false, false, 0);
    const { headline, detail } = combineHeadlineDetail('idle', shortHD, longHD, null, { entryPrice: null, stop: null }, { entryPrice: null, stop: null });
    return {
      state: 'idle',
      dayKey: '',
      levels: emptyLevels(),
      evidence: emptyEvidence(),
      events: [],
      headline,
      detail,
      short: { state: 'none', headline: shortHD.headline, detail: shortHD.detail },
      long: { state: 'none', headline: longHD.headline, detail: longHD.detail },
    };
  }

  const dayKeys: string[] = [];
  const groups: Candle[][] = [];
  const indexOf = new Map<string, number>();
  // etDayKey is an Intl format() per call — 2 ms of a 2.8 ms evaluate on a
  // 1000-bar name, times up to 16 cells per live tick. The ET calendar date can
  // only change on a UTC HOUR boundary (America/New_York's offsets are whole
  // hours, and its DST switches happen at 02:00 local, also on the hour), so
  // one lookup per UTC hour gives the same key for every candle in it: ~30
  // Intl calls per 1000 bars instead of 1000 (T35 pins the assumption).
  let lastHour = Number.NaN;
  let key = '';
  for (const c of candles) {
    const hour = Math.floor(c.ts / 3_600_000);
    if (hour !== lastHour) {
      lastHour = hour;
      key = etDayKey(c.ts);
    }
    let idx = indexOf.get(key);
    if (idx == null) {
      idx = groups.length;
      indexOf.set(key, idx);
      groups.push([]);
      dayKeys.push(key);
    }
    groups[idx]!.push(c);
  }

  const allEvents: SignalEvent[] = [];
  let lastOutcome: DayOutcome | null = null;
  for (let d = 0; d < groups.length; d++) {
    const dayCandles = groups[d]!;
    const isLastDay = d === groups.length - 1;
    // input.prevClose is the caller's official prior-session close for the
    // day it describes — which is whichever day is LAST here (candles "may
    // span the previous trading day + today", 2.1), not day 0. Using it on
    // day 0 instead measured yesterday's gap against today's official close
    // and left today's gap gate comparing against yesterday's own last
    // candle (its final after-hours bar, not an official close) — see F2.
    // Earlier days (and the last day when the caller passed no prevClose)
    // fall back to the prior day's own last close, same as before.
    //
    // Iteration 2 (B5): that fallback is now also what a genuinely-present
    // but BAD prevClose falls through to. `input.prevClose ?? prevGroupClose`
    // treats 0 as "present" (`??` only catches null/undefined), so a live
    // scanner's prevClose of exactly 0 (a separate, already-tracked bug)
    // used to leave gapGainPct permanently null instead of falling back —
    // an honest gapper (e.g. one whose prevClose lookup is broken) could
    // never arm off the gap gate. `> 0` closes that gap: a zero-or-absent
    // prevClose both fall through to the previous ET day's own last candle,
    // same as a fully-absent one always did. refSource names which one was
    // actually used, since a fallback overnight reference is materially
    // weaker evidence than an official close.
    const prevGroupClose = d > 0 ? (groups[d - 1]![groups[d - 1]!.length - 1]!.close ?? null) : null;
    const officialPrevClose = isLastDay && input.prevClose != null && input.prevClose > 0 ? input.prevClose : null;
    const refClose = officialPrevClose ?? prevGroupClose;
    const refSource: 'prevClose' | 'prevDayClose' | null = officialPrevClose != null ? 'prevClose' : prevGroupClose != null ? 'prevDayClose' : null;
    const lastCandle = dayCandles[dayCandles.length - 1]!;
    const forming = isLastDay && input.nowMs < lastCandle.ts + input.tfMs;
    const outcome = processDay(dayCandles, params, refClose, refSource, forming);
    allEvents.push(...outcome.events);
    if (isLastDay) lastOutcome = outcome;
  }

  const last = lastOutcome!;
  return {
    state: last.state,
    dayKey: dayKeys[dayKeys.length - 1]!,
    levels: last.levels,
    evidence: last.evidence,
    events: allEvents,
    headline: last.headline,
    detail: last.detail,
    short: last.short,
    long: last.long,
  };
}
