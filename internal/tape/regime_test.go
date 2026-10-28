package tape

import (
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// warmUp feeds enough trades and a live two-sided quote to clear the
// warm-up guard (>=20 total trades, live quote) without tripping any
// detector itself or leaving a fresh cooldown behind -- callers can then
// exercise a single detector in isolation. Sizes are tiny and prices flat so
// none of the six detectors fire on their own.
func warmUp(e *Engine, sym string, ms int64) {
	quote(e, sym, 10.00, 10.02, ms)
	for i := 0; i < 20; i++ {
		trade(e, sym, 10.01, 1, ms)
	}
}

func drainRegimes(e *Engine) []core.RegimeEvent {
	var out []core.RegimeEvent
	for {
		select {
		case ev := <-e.regimeOut:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestDetectLevelAbsorption(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	quoteSz(e, "ABCD", 99.99, 1000, 100.01, 1000, ms) // establish inside quote at 99.99/100.01
	drainRegimes(e)                                   // establishing a quote never fires -- detect() only runs on trades

	floor := levelAbsorbFloorMult * e.medianSize(st) // 5 * median(size=1) = 5
	per := int64(floor)/3 + 1                        // 3 refills together must clear the floor
	for i := 0; i < levelAbsorbRefills; i++ {
		trade(e, "ABCD", 99.99, per, ms) // sellers hit the bid
		quoteSz(e, "ABCD", 99.99, 300, 100.01, 1000, ms)
		quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, ms) // refreshes at the same price
	}
	// detect() only evaluates on a trade event, not a quote -- one more trade
	// is needed to check the level-absorption condition against the refill
	// count/volume the preceding quotes just built up. A tiny buy at the ask
	// leaves the bid-side state (what we're testing) untouched.
	trade(e, "ABCD", 100.01, 1, ms)

	evs := drainRegimes(e)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 regime event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Trigger != "level_absorption" {
		t.Fatalf("trigger: got %q want level_absorption", evs[0].Trigger)
	}
	if st.absorbFiredPx != 99.99 {
		t.Fatalf("absorbFiredPx should be stamped at the bid, got %v", st.absorbFiredPx)
	}

	// Refiring at the SAME level, even well after the cooldown has elapsed,
	// must not fire again -- the price level itself is deduped, not merely
	// gated by the cooldown window.
	later := ms + claudeCooldownMs + 1
	trade(e, "ABCD", 99.99, per, later)
	quoteSz(e, "ABCD", 99.99, 300, 100.01, 1000, later)
	quoteSz(e, "ABCD", 99.99, 1500, 100.01, 1000, later)
	trade(e, "ABCD", 100.01, 1, later)
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("same level should be deduped even after the cooldown elapses, got %+v", evs)
	}
}

func TestDetectBlockPrint(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	th := e.blockThreshold(st)
	trade(e, "ABCD", 10.05, int64(th)+1, ms) // single oversized print, buy (above ask)

	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "block_print" {
		t.Fatalf("expected a single block_print event, got %+v", evs)
	}
}

func TestDetectSweep(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)

	// 5 consecutive buys (above the ask), price net-progressing, all within 3s.
	quote(e, "ABCD", 10.00, 10.02, ms)
	for i := 0; i < sweepLen; i++ {
		trade(e, "ABCD", 10.03+float64(i)*0.01, 10, ms+int64(i)*100)
	}
	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "buy_sweep" {
		t.Fatalf("expected a single buy_sweep event, got %+v", evs)
	}

	// Same direction again -- must be deduped until the direction flips.
	for i := 0; i < sweepLen; i++ {
		trade(e, "ABCD", 10.10+float64(i)*0.01, 10, ms+1000+int64(i)*100)
	}
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("repeated buy_sweep in the same direction must be deduped, got %+v", evs)
	}

	// Flip to a sell sweep -- must fire again, once the shared cooldown from
	// the first buy_sweep has also elapsed. Move the quote up first so these
	// descending prints classify as sells (at/below the new bid).
	quote(e, "ABCD", 10.30, 10.32, ms+1900)
	for i := 0; i < sweepLen; i++ {
		trade(e, "ABCD", 10.29-float64(i)*0.01, 10, ms+claudeCooldownMs+1000+int64(i)*100)
	}
	evs = drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "sell_sweep" {
		t.Fatalf("expected a sell_sweep once direction flips, got %+v", evs)
	}
}

func TestDetectCVDFlip(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	noise := cvdFlipNoiseMult * e.medianSize(st)
	big := int64(noise) + 5

	// 8s ago: a burst of selling (prior 5s window: [now-10s, now-5s)).
	quote(e, "ABCD", 10.00, 10.02, ms-8_000)
	trade(e, "ABCD", 10.00, big, ms-8_000)
	// now: a burst of buying (current 5s window: [now-5s, now]).
	quote(e, "ABCD", 10.00, 10.02, ms)
	trade(e, "ABCD", 10.02, big, ms)

	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "cvd_flip" {
		t.Fatalf("expected a single cvd_flip event, got %+v", evs)
	}
}

func TestDetectAbsorption(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	// Warm up well outside the 10s absorption lookback -- otherwise those
	// flat, neutral-classified prints would themselves become the window's
	// "oldest price" and corrupt the flat-price check below.
	warmUp(e, "ABCD", ms-20_000)
	st := e.syms["ABCD"]

	quote(e, "ABCD", 10.00, 10.02, ms)
	med := e.medianSize(st)
	// >=15 prints, volume >= 10x median, all at the ask (buys) so price
	// never actually moves -- heavy buying the offer keeps absorbing.
	perPrint := int64(absorptionVolMedianMult*med)/15 + 1
	for i := 0; i < 15; i++ {
		trade(e, "ABCD", 10.02, perPrint, ms+int64(i)) // within the same second, pinned at the ask
	}

	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "absorption" {
		t.Fatalf("expected a single absorption event, got %+v", evs)
	}
}

func TestDetectSpeedSpike(t *testing.T) {
	e := newTest()
	tick := t0
	e.now = func() time.Time { return tick }
	ms := tick.UnixMilli()
	quote(e, "ABCD", 10.00, 10.02, ms)
	// Trickle 20 trades one per second over the last 20s to clear warm-up
	// and establish a low (~1/s) baseline, without spiking prints_per_sec.
	for i := 0; i < 20; i++ {
		trade(e, "ABCD", 10.01, 1, ms-int64(19-i)*1000)
	}
	drainRegimes(e) // discard anything the warm-up trickle itself fired

	// Advance the clock past the 30s baseline-warmup requirement.
	tick = tick.Add(35 * time.Second)
	ms = tick.UnixMilli()

	// A burst of >= speedSpikeMinPrints trades in the same second -- far
	// above the ~1/s baseline built above.
	for i := 0; i < speedSpikeMinPrints; i++ {
		trade(e, "ABCD", 10.01, 1, ms)
	}

	evs := drainRegimes(e)
	if len(evs) != 1 || evs[0].Trigger != "speed_spike" {
		t.Fatalf("expected a single speed_spike event, got %+v", evs)
	}
}

func TestCooldownGatesAllDetectorsTogether(t *testing.T) {
	e := newTest()
	ms := t0.UnixMilli()
	warmUp(e, "ABCD", ms)
	st := e.syms["ABCD"]

	th := e.blockThreshold(st)
	trade(e, "ABCD", 10.05, int64(th)+1, ms) // fires block_print
	if evs := drainRegimes(e); len(evs) != 1 {
		t.Fatalf("expected the first block print to fire, got %+v", evs)
	}

	// Immediately after: a second, independently-qualifying block print
	// inside the cooldown window must NOT fire, even though its own
	// condition is satisfied on its own.
	trade(e, "ABCD", 10.05, int64(th)+1, ms+1000)
	if evs := drainRegimes(e); len(evs) != 0 {
		t.Fatalf("a second detector firing inside the cooldown must be gated, got %+v", evs)
	}

	// After the cooldown elapses, a qualifying print fires again.
	trade(e, "ABCD", 10.05, int64(th)+1, ms+claudeCooldownMs+1)
	if evs := drainRegimes(e); len(evs) != 1 {
		t.Fatalf("expected a fire once the cooldown has elapsed, got %+v", evs)
	}
}

func TestConcurrentSymbolsDetectIndependently(t *testing.T) {
	e := newTest("AAAA", "BBBB")
	ms := t0.UnixMilli()
	warmUp(e, "AAAA", ms)
	warmUp(e, "BBBB", ms)

	thA := e.blockThreshold(e.syms["AAAA"])
	trade(e, "AAAA", 10.05, int64(thA)+1, ms) // AAAA fires and starts its own cooldown
	evsA := drainRegimes(e)
	if len(evsA) != 1 || evsA[0].Sym != "AAAA" {
		t.Fatalf("expected AAAA to fire once, got %+v", evsA)
	}

	// BBBB must still be able to fire immediately -- cooldown/dedup state is
	// per-symbol, not shared.
	thB := e.blockThreshold(e.syms["BBBB"])
	trade(e, "BBBB", 10.05, int64(thB)+1, ms+10) // well inside AAAA's cooldown window
	evsB := drainRegimes(e)
	if len(evsB) != 1 || evsB[0].Sym != "BBBB" {
		t.Fatalf("BBBB's fire must not be gated by AAAA's cooldown, got %+v", evsB)
	}
}
