// Package backtest replays the 9EMA-flag auto-trader over history.
//
// It is a PURE simulation: no network, no clock, no files. cmd/backtest fetches
// and caches the data and hands it in; this package only does the arithmetic -
// candidate admission (prepare.go), the trader's rules replayed over the
// signals the engine (internal/emaflag) calls (sim.go), the cost model
// (costs.go), the statistics (stats.go) and the report writer (report.go).
//
// The trader itself (internal/autotrade) is NOT imported: it is a goroutine
// machine wired to a broker, and what a backtest needs from it is a handful of
// rules. Each rule below cites the line of internal/autotrade it mirrors
// (settings.go: Settings / DefaultSettings; trader.go: tryEntry, trailStep,
// checkHalt, flattenDue, vol5) and a test (rules_test.go) pins the numeric
// defaults to autotrade.FlagSettings() (the 9EMA flag strategy's defaults) so a change there turns this red.
//
// Every approximation is documented where it is made, and again, in plain
// language, in the report's caveats.
package backtest

import (
	"fmt"
	"math"

	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// Trail exits (autotrade.TrailStop / autotrade.TrailClose).
const (
	TrailStop  = "stop"  // a trailing STOP ORDER: exits on a touch of the trail, intrabar
	TrailClose = "close" // the engine's EXIT: a 1-minute CLOSE under its own trail; the hard stop stays at the entry's stop
)

// Costs is the friction model. Commission is the IBKR tiered approximation
// (per share, with a per-order minimum and a cap at a percent of trade value),
// Fees the regulatory/exchange per-share charge, and the three slippages are the
// allowance for fast penny-stock markets, always against the trader.
type Costs struct {
	CommissionPerShare float64 // 0.0035 $/share, each side
	CommissionMin      float64 // 0.35 $ per order
	CommissionMaxPct   float64 // 1 (% of trade value): the cap, which WINS over the minimum on a tiny trade (IBKR's rule)
	FeePerShare        float64 // 0.002 $/share, each side (regulatory + exchange)
	EntrySlipPct       float64 // 0.25: entries fill this much worse than the model price
	StopSlipPct        float64 // 0.5: stop exits (touch trail, hard stop) fill this much worse
	FlattenSlipPct     float64 // 0.1: flatten / close-mode / end-of-data exits fill this much worse
}

// DefaultCosts are the figures of the task spec.
func DefaultCosts() Costs {
	return Costs{
		CommissionPerShare: 0.0035,
		CommissionMin:      0.35,
		CommissionMaxPct:   1,
		FeePerShare:        0.002,
		EntrySlipPct:       0.25,
		StopSlipPct:        0.5,
		FlattenSlipPct:     0.1,
	}
}

// NoCosts is the frictionless model (the gross run).
func NoCosts() Costs { return Costs{} }

// Rules is everything the simulation needs for one variant: the trader's
// settings (autotrade.Settings, same names where one exists) plus the cost
// model. Windows are minutes of the ET day, half-open [start, end) exactly as
// autotrade's inWindow.
type Rules struct {
	Name string // "V1"
	Desc string // one line for the report

	TrailExit    string  // TrailStop | TrailClose           (Settings.TrailExit)
	TrailStopPct float64 // 1.0 / 0.5: the trail sits this % under the 9 EMA of the last CLOSED bar (Settings.TrailStopPct); close mode uses the engine's own Flag.StopBufferPct

	WindowStartMin int // 04:00 = 240   (Settings.WindowStartET)
	WindowEndMin   int // 11:00 = 660   (Settings.WindowEndET)
	FlattenMin     int // 15:50 = 950   (Settings.FlattenAtET)

	MaxTradesPerDay  int     // 10   entries per ET day
	MaxOpenPositions int     // 3
	MaxEntriesPerSym int     // 2    per ET day
	MaxDailyLossUSD  float64 // 200  realized + marked open P&L <= -this halts new entries

	RiskPerTradeUSD float64 // 50
	MaxPositionUSD  float64 // 2000
	MaxLiquidityPct float64 // 2    shares <= this % of the 5 one-minute bars before the entry bar
	MinPrice        float64 // 0.20
	MaxPrice        float64 // 50
	EntryLimitPct   float64 // 1.0  BUY LMT at trigger*(1+this/100)   (Settings.EntrySlipPct - the LIMIT's allowance, not Costs.EntrySlipPct)

	MaxCandidates int // 80: the live scan's cap on candidates at once (flagscan.MaxCandidates)

	Flag  emaflag.Params // the engine's thresholds (DefaultParams)
	Costs Costs
}

// ShippedRules is autotrade.FlagSettings() - the 9EMA flag strategy's defaults - as a Rules (V1 before its name).
func ShippedRules() Rules {
	return Rules{
		TrailExit:        TrailStop,
		TrailStopPct:     1.0,
		WindowStartMin:   mustHHMM("04:00"),
		WindowEndMin:     mustHHMM("11:00"),
		FlattenMin:       mustHHMM("15:50"),
		MaxTradesPerDay:  10,
		MaxOpenPositions: 3,
		MaxEntriesPerSym: 2,
		MaxDailyLossUSD:  200,
		RiskPerTradeUSD:  50,
		MaxPositionUSD:   2000,
		MaxLiquidityPct:  2,
		MinPrice:         0.20,
		MaxPrice:         50,
		EntryLimitPct:    1.0,
		MaxCandidates:    80,
		Flag:             emaflag.DefaultParams(),
		Costs:            DefaultCosts(),
	}
}

// Variants are the five runs of the task, in order.
func Variants() []Rules {
	v1 := ShippedRules()
	v1.Name = "V1"
	v1.Desc = "trail stop 1%, window 04:00-11:00, 10 trades/day (the shipped defaults)"

	v2 := ShippedRules()
	v2.Name = "V2"
	v2.TrailStopPct = 0.5
	v2.Desc = "trail stop 0.5%, window 04:00-11:00, 10 trades/day"

	v3 := ShippedRules()
	v3.Name = "V3"
	v3.TrailExit = TrailClose
	v3.Desc = "close-mode trail (exit on a 1-minute close under EMA-1%), hard stop at the entry stop, window 04:00-11:00, 10 trades/day"

	v4 := ShippedRules()
	v4.Name = "V4"
	v4.WindowEndMin = mustHHMM("15:45")
	v4.Desc = "trail stop 1%, window 04:00-15:45, 10 trades/day"

	v5 := ShippedRules()
	v5.Name = "V5"
	v5.MaxTradesPerDay = 5
	v5.MaxDailyLossUSD = 150
	v5.Desc = "trail stop 1%, window 04:00-11:00, the user's current limits: 5 trades/day, daily loss $150"

	return []Rules{v1, v2, v3, v4, v5}
}

// VariantByName finds a variant ("V1".."V5", case-insensitive).
func VariantByName(name string) (Rules, bool) {
	for _, v := range Variants() {
		if equalFold(v.Name, name) {
			return v, true
		}
	}
	return Rules{}, false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Validate rejects a Rules the simulation cannot run (an unusable value is an
// error here, never silently clamped - the same stance as autotrade.Settings).
func (r Rules) Validate() error {
	fin := func(name string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s: not finite", name)
		}
		return nil
	}
	for _, c := range []struct {
		n string
		v float64
	}{
		{"TrailStopPct", r.TrailStopPct}, {"MaxDailyLossUSD", r.MaxDailyLossUSD}, {"RiskPerTradeUSD", r.RiskPerTradeUSD},
		{"MaxPositionUSD", r.MaxPositionUSD}, {"MaxLiquidityPct", r.MaxLiquidityPct}, {"MinPrice", r.MinPrice},
		{"MaxPrice", r.MaxPrice}, {"EntryLimitPct", r.EntryLimitPct},
	} {
		if err := fin(c.n, c.v); err != nil {
			return err
		}
	}
	switch r.TrailExit {
	case TrailStop, TrailClose:
	default:
		return fmt.Errorf("TrailExit must be %q or %q, got %q", TrailStop, TrailClose, r.TrailExit)
	}
	if r.TrailExit == TrailStop && !(r.TrailStopPct > 0 && r.TrailStopPct < 100) {
		return fmt.Errorf("TrailStopPct must be in (0, 100), got %g", r.TrailStopPct)
	}
	if r.WindowStartMin >= r.WindowEndMin {
		return fmt.Errorf("window [%d, %d) is empty", r.WindowStartMin, r.WindowEndMin)
	}
	if r.FlattenMin < r.WindowEndMin {
		return fmt.Errorf("FlattenMin %d is before the window end %d", r.FlattenMin, r.WindowEndMin)
	}
	if r.MaxTradesPerDay < 1 || r.MaxOpenPositions < 1 || r.MaxEntriesPerSym < 1 {
		return fmt.Errorf("the trade / open / per-symbol limits must be at least 1")
	}
	if !(r.RiskPerTradeUSD > 0) || !(r.MaxPositionUSD > 0) || !(r.MaxLiquidityPct > 0) || !(r.MaxDailyLossUSD > 0) {
		return fmt.Errorf("risk, position, liquidity and daily-loss limits must be positive")
	}
	if !(r.MinPrice > 0) || !(r.MaxPrice > r.MinPrice) {
		return fmt.Errorf("price range [%g, %g] is empty", r.MinPrice, r.MaxPrice)
	}
	return nil
}
