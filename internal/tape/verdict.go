// The always-on, free "who is in control" read: a fixed five-tier cascade
// evaluated fresh on every snapshot() call (the 1Hz publish() tick, and also
// whenever a regime event fires -- see fireRegime in regime.go), independent
// of whether any regime-shift detector fired this tick. This is the Go
// mirror of engine.py's local_verdict(): a level absorbing repeated hits and
// refreshing outranks raw delta (the aggressor is losing); liquidity pulled
// without trading is that side backing away; a one-sided sweep with price
// progressing is momentum; one side aggressing without moving price is
// divergence (absorption without the SMB-style refill signature); otherwise
// the plain 60s buy/sell imbalance is the fallback. First tier whose
// condition holds wins.
package tape

import (
	"fmt"
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	// verdictPullMin is tier 2's threshold -- engine.py's
	// `b["bid_pull"] >= 4 and b["bid_pull"] > b["bid_absorb"]`.
	verdictPullMin = 4

	// verdictSweepScoreThresh is tier 3's momentum-confirmation threshold --
	// engine.py's `score > 0.2` / `score < -0.2`.
	verdictSweepScoreThresh = 0.2

	// verdictDivergenceScoreThresh is tier 4's one-sidedness threshold --
	// engine.py's `score <= -0.3` / `score >= 0.3`.
	verdictDivergenceScoreThresh = 0.3

	// verdictFallbackScoreThresh is tier 5's lean threshold -- engine.py's
	// `score > 0.12` / `score < -0.12`.
	verdictFallbackScoreThresh = 0.12

	// verdictConfidenceCap mirrors engine.py's `round(min(conf, 0.95), 2)` --
	// every tier's confidence is capped here, then rounded to 2 decimals
	// (see localVerdict's out() closure) exactly as Python does.
	verdictConfidenceCap = 0.95

	// Tier 1 (level absorption) reuses regime.go's level-absorption
	// constants (levelAbsorbFloorMult=5.0, levelAbsorbRefills=3) rather than
	// redefining them here: engine.py's local_verdict() and
	// _detect_level_absorption() hardcode the exact same "5x median, >=3
	// refills" literals for the same underlying judgement -- a level
	// repeatedly hit and refreshed -- just read off book_stats() here
	// instead of the raw _bid_level_vol/_bid_refills fields directly.
	//
	// Tier 4 (divergence) reuses regime.go's absorption-detector constants
	// (absorptionVolMedianMult=10.0, absorptionBandPct=0.0005) for the same
	// reason: engine.py hardcodes the identical "10x median volume" and
	// "~5bps of price" literals in both _detect_absorption() and
	// local_verdict()'s tier 4. Tier 4 also reuses regime.go's
	// microWindowMs=60_000 as its lookback into the trade-level micro ring
	// (see symState.micro's doc comment for why the bucket ring can't supply
	// a raw price path).
)

// fmtPx renders a price the way engine.py's local_verdict() inline `px()`
// helper does: general formatting, or an em dash when the price is
// unknown/zero. Only used in verdict evidence/text, purely cosmetic.
func fmtPx(p float64) string {
	if p == 0 {
		return "—"
	}
	return fmt.Sprintf("%.4g", p)
}

// localVerdict is the always-on, free "who is in control" read -- the Go
// mirror of engine.py's local_verdict(), called once per publish() tick from
// snapshot() (independent of the regime-shift cascade in regime.go) and once
// per fired regime event from fireRegime, so every RegimeEvent ships with an
// instant starter verdict the same way engine.py's server.py attaches
// local_verdict() to the alert the moment it fires.
func (e *Engine) localVerdict(st *symState) core.TapeVerdict {
	b := e.bookStats(st, bookLookback)
	b60, s60, _, _ := e.window(st, 60)
	score := controlScore60(b60, s60)
	med := e.medianSize(st)
	floor := levelAbsorbFloorMult * med
	var ev []string

	out := func(control string, conf float64, text string) core.TapeVerdict {
		if conf > verdictConfidenceCap {
			conf = verdictConfidenceCap
		}
		// Python: `round(min(conf, 0.95), 2)`.
		conf = math.Round(conf*100) / 100
		return core.TapeVerdict{
			Control:    control,
			Confidence: conf,
			Text:       text,
			Evidence:   ev,
			Source:     "local",
		}
	}

	// 1) Absorption at a level -- the strongest read on the tape.
	if b.BidRefills >= levelAbsorbRefills && b.BidAbsorbedVol >= floor {
		ev = append(ev, fmt.Sprintf("bid %s refreshed %dx, absorbed %g", fmtPx(st.bid), b.BidRefills, b.BidAbsorbedVol))
		if score < -0.1 {
			ev = append(ev, fmt.Sprintf("heavy selling (%+.2f) not moving price", score))
		}
		return out("buyers", 0.55+0.05*float64(b.BidRefills),
			fmt.Sprintf("Bid at %s absorbing repeated selling and refreshing.", fmtPx(st.bid)))
	}
	if b.AskRefills >= levelAbsorbRefills && b.AskAbsorbedVol >= floor {
		ev = append(ev, fmt.Sprintf("offer %s refreshed %dx, absorbed %g", fmtPx(st.ask), b.AskRefills, b.AskAbsorbedVol))
		if score > 0.1 {
			ev = append(ev, fmt.Sprintf("heavy buying (%+.2f) not lifting price", score))
		}
		return out("sellers", 0.55+0.05*float64(b.AskRefills),
			fmt.Sprintf("Offer at %s capping — buying absorbed, price not lifting.", fmtPx(st.ask)))
	}

	// 2) Liquidity pulled without trading -- that side stepping away.
	if b.BidPull >= verdictPullMin && b.BidPull > b.BidAbsorb {
		ev = append(ev, fmt.Sprintf("bid size pulled %dx with no trades", b.BidPull))
		return out("sellers", 0.5, "Bid size pulling away — support thinning under price.")
	}
	if b.AskPull >= verdictPullMin && b.AskPull > b.AskAbsorb {
		ev = append(ev, fmt.Sprintf("offer size pulled %dx with no trades", b.AskPull))
		return out("buyers", 0.5, "Offers pulling away — resistance thinning above price.")
	}

	// 3) A one-sided run with price progressing.
	if st.lastSweptDir == "buy" && score > verdictSweepScoreThresh {
		ev = append(ev, fmt.Sprintf("buyers sweeping offers, 60s imbalance %+.2f", score))
		return out("buyers", 0.5+math.Abs(score)/2, "Buyers lifting offers and price stepping up.")
	}
	if st.lastSweptDir == "sell" && score < -verdictSweepScoreThresh {
		ev = append(ev, fmt.Sprintf("sellers sweeping bids, 60s imbalance %+.2f", score))
		return out("sellers", 0.5+math.Abs(score)/2, "Sellers hitting bids and price stepping down.")
	}

	// 4) Divergence -- one side aggressing but price refusing to follow.
	vol60 := b60 + s60
	first, last, ok := microPriceRange(st, microWindowMs, e.now().UnixMilli())
	if ok && float64(vol60) > absorptionVolMedianMult*med {
		price := st.lastPrice
		if price == 0 {
			price = 1
		}
		band := absorptionBandPct * price
		flat := math.Abs(last-first) <= band
		if flat && score <= -verdictDivergenceScoreThresh {
			ev = append(ev, fmt.Sprintf("sell imbalance %+.2f but price flat over 60s", score))
			return out("buyers", 0.5, "Sellers pressing but price is holding — supply being absorbed.")
		}
		if flat && score >= verdictDivergenceScoreThresh {
			ev = append(ev, fmt.Sprintf("buy imbalance %+.2f but price flat over 60s", score))
			return out("sellers", 0.5, "Buyers pressing but price is stalling — supply on offer.")
		}
	}

	// 5) Fall back to the plain 60s imbalance.
	ev = append(ev, fmt.Sprintf("60s imbalance %+.2f (%d bought / %d sold)", score, b60, s60))
	if b.InsideImbalance != 0 {
		ev = append(ev, fmt.Sprintf("inside size %+.2f", b.InsideImbalance))
	}
	if score > verdictFallbackScoreThresh {
		return out("buyers", 0.35+math.Abs(score)/2, "Buyers leaning on the offer.")
	}
	if score < -verdictFallbackScoreThresh {
		return out("sellers", 0.35+math.Abs(score)/2, "Sellers leaning on the bid.")
	}
	return out("balanced", 0.4, "Two-sided flow, no clear control.")
}
