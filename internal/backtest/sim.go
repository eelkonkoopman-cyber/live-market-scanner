package backtest

import (
	"math"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Exit kinds (Trade.ExitKind).
const (
	ExitStop    = "stop"    // the touch of the trailing stop order / the hard stop (stop first inside a bar)
	ExitClose   = "close"   // close mode: a 1-minute CLOSE under the engine's trail
	ExitFlatten = "flatten" // the 15:50 ET flatten, at that bar's open
	ExitEOD     = "eod"     // the data ended with the position open: the last close
)

// Skip reasons: why a signal the engine called did not become a trade. The
// order is the order the live trader checks them (autotrade.tryEntry).
const (
	SkipHalted    = "halted (daily loss)"
	SkipWindow    = "outside the entry window"
	SkipInSymbol  = "already in the symbol"
	SkipMaxOpen   = "max open positions"
	SkipMaxTrades = "max trades per day"
	SkipMaxSymbol = "max entries per symbol"
	SkipPrice     = "price outside the range"
	SkipNoStop    = "no usable stop"
	SkipSizeRisk  = "sizing: under 1 share by risk"
	SkipSizePos   = "sizing: under 1 share by max position"
	SkipSizeLiq   = "sizing: under 1 share by liquidity"
	SkipNoFill    = "no fill (bar opened above the limit)"
)

// SkipReasons lists every reason in display order.
var SkipReasons = []string{
	SkipHalted, SkipWindow, SkipInSymbol, SkipMaxOpen, SkipMaxTrades, SkipMaxSymbol,
	SkipPrice, SkipNoStop, SkipSizeRisk, SkipSizePos, SkipSizeLiq, SkipNoFill,
}

// Trade is one completed round trip.
type Trade struct {
	Variant string
	Date    string
	Sym     string

	EntryTs  int64 // the entry bar's start (ms)
	ExitTs   int64 // the exit bar's start (ms); a close-mode exit happens at that bar's CLOSE
	EntryMin int   // ET minute of day of the entry bar
	ExitKind string

	Shares  int64
	Trigger float64 // the engine's trigger (what the order was sized on)
	Stop    float64 // the engine's initial stop

	EntryRaw  float64 // model fill: max(trigger, the entry bar's open)
	EntryFill float64 // EntryRaw worse by the entry slippage (never above the limit)
	ExitRaw   float64 // model exit price
	ExitFill  float64 // ExitRaw worse by the exit's slippage

	Gross       float64 // (ExitRaw - EntryRaw) * shares: no commission, no fees, no slippage
	Commission  float64 // both sides
	Fees        float64 // both sides (regulatory / exchange)
	Slippage    float64 // what the slippage cost (Gross - the price P&L at the fill prices)
	Net         float64 // after all of it
	InitialRisk float64 // shares * (trigger - stop): the 1R of this trade
	RGross      float64
	RNet        float64

	PmVolX float64
	Adv10  float64

	// SplitFactor is 1, or the factor of a split executed after the day (a name that
	// reverse-split later: the prices, shares and costs above are on the basis of the
	// day, not the feed's adjusted one). SplitCostGap is what the adjusted figures
	// would have understated: this trade's commission + fees minus what the same
	// position costs at the adjusted price and share count (see splits.go).
	SplitFactor  float64
	SplitCostGap float64

	pxPnL   float64 // (ExitFill - EntryFill) * shares: realized price P&L, the basis of the daily-loss halt (the broker's realized P&L carries no commissions)
	effExit int64   // when a later entry sees the position as closed (see SimulateDay)
	entryIx int
	cand    *Candidate
}

// DayResult is one day under one variant.
type DayResult struct {
	Date    string
	Seen    int // signals the engine called on admitted candidates
	Trades  []Trade
	Skips   map[string]int
	Halted  bool
	HaltTs  int64 // the minute the daily-loss halt tripped (0 = never)
	Net     float64
	Gross   float64
	Entries int // entries counted against the daily limit
	MaxOpen int // most positions open at once
}

// VariantResult is one variant over every test day.
type VariantResult struct {
	Rules  Rules
	Days   []DayResult
	Trades []Trade
	Skips  map[string]int
	Seen   int
}

// RunVariant replays every day under r.
func RunVariant(days []PreparedDay, r Rules) VariantResult {
	vr := VariantResult{Rules: r, Skips: map[string]int{}}
	for _, pd := range days {
		dr := SimulateDay(pd, r)
		vr.Days = append(vr.Days, dr)
		vr.Trades = append(vr.Trades, dr.Trades...)
		vr.Seen += dr.Seen
		for k, v := range dr.Skips {
			vr.Skips[k] += v
		}
	}
	return vr
}

// SimulateDay replays one day's signals, in time order across ALL candidates,
// under the trader's portfolio rules (autotrade.tryEntry / checkHalt).
//
// TIMING. Everything is decided on 1-minute bars, so time is the bar start. A
// signal at bar T meets the portfolio as it stood at T:
//   - a position is still OPEN for the signal when its exit happened inside bar
//     T or later (a stop, flatten or end-of-data exit counts from its bar's
//     start; a close-mode exit from its bar's close): inside the same bar the
//     order of events is unknowable, and keeping the position open is the
//     conservative reading (the limit binds);
//   - the daily-loss halt is checked at EVERY minute boundary from the first
//     entry on (the live trader checks on every update), on realized P&L plus
//     the open positions marked at the last close BEFORE that minute; once
//     tripped it holds for the rest of the day (no new entries, exits go on);
//   - a signal that does not fill (the bar opened above the entry limit) is
//     refunded: it uses no slot of the trades-per-day or per-symbol limits
//     (autotrade refunds an unfilled entry's slot).
func SimulateDay(pd PreparedDay, r Rules) DayResult {
	res := DayResult{Date: pd.Date, Skips: map[string]int{}, Seen: len(pd.Signals)}
	var pos []*Trade // every accepted position of the day, closed or not (the paths are known up front; the loop only reveals them in time order)
	entriesToday := 0
	symEntries := map[string]int{}
	halted := false
	var lastCheck int64

	openAt := func(t int64) []*Trade { // positions not yet realized for a signal at bar t
		var o []*Trade
		for _, p := range pos {
			if p.EntryTs <= t && !(p.effExit < t) {
				o = append(o, p)
			}
		}
		return o
	}
	pnlAt := func(m int64) float64 { // realized + open marked at the last close before minute m
		var total float64
		for _, p := range pos {
			if p.EntryTs >= m {
				continue
			}
			if p.effExit < m {
				total += p.pxPnL
				continue
			}
			bars := p.cand.Bars
			k := sort.Search(len(bars), func(i int) bool { return bars[i].T >= m }) - 1
			if k < p.entryIx {
				k = p.entryIx
			}
			total += (bars[k].C - p.EntryFill) * float64(p.Shares)
		}
		return total
	}

	sigs := pd.Signals
	for i := 0; i < len(sigs); {
		t := sigs[i].Ts
		j := i
		for j < len(sigs) && sigs[j].Ts == t {
			j++
		}
		if !halted && len(pos) > 0 {
			for m := lastCheck + minuteMs; m <= t; m += minuteMs {
				if pnlAt(m) <= -r.MaxDailyLossUSD {
					halted, res.Halted, res.HaltTs = true, true, m
					break
				}
			}
		}
		lastCheck = t
		for k := i; k < j; k++ {
			s := sigs[k]
			skip := func(why string) { res.Skips[why]++ }
			c := s.Cand
			bar := c.Bars[s.Idx]
			open := openAt(t)
			if n := len(open); n > res.MaxOpen {
				res.MaxOpen = n
			}
			if halted {
				skip(SkipHalted)
				continue
			}
			if m := ETMinute(t); m < r.WindowStartMin || m >= r.WindowEndMin {
				skip(SkipWindow)
				continue
			}
			inSym := false
			for _, p := range open {
				if p.Sym == c.Sym {
					inSym = true
				}
			}
			switch {
			case inSym:
				skip(SkipInSymbol)
				continue
			case len(open) >= r.MaxOpenPositions:
				skip(SkipMaxOpen)
				continue
			case entriesToday >= r.MaxTradesPerDay:
				skip(SkipMaxTrades)
				continue
			case symEntries[c.Sym] >= r.MaxEntriesPerSym:
				skip(SkipMaxSymbol)
				continue
			case !(s.Trigger >= r.MinPrice && s.Trigger <= r.MaxPrice):
				skip(SkipPrice)
				continue
			case math.IsNaN(s.Stop) || !(s.Stop > 0) || !(s.Trigger > s.Stop):
				skip(SkipNoStop)
				continue
			}

			// Sizing (autotrade.tryEntry): on the TRIGGER, the lowest of the three caps.
			byRisk := floorShares(r.RiskPerTradeUSD / (s.Trigger - s.Stop))
			byPos := floorShares(r.MaxPositionUSD / s.Trigger)
			byLiq := floorShares(r.MaxLiquidityPct / 100 * float64(vol5(c.Bars, s.Idx, c.AdmitTs)))
			shares := min(byRisk, byPos, byLiq)
			if shares < 1 {
				switch {
				case byLiq < 1:
					skip(SkipSizeLiq)
				case byRisk < 1:
					skip(SkipSizeRisk)
				default:
					skip(SkipSizePos)
				}
				continue
			}

			// The fill: a BUY LMT at trigger * (1 + 1%), rounded to the tick. The bar
			// that broke the trigger must not have OPENED above it (the live order
			// would not fill within its 15 s); else it fills at the trigger, or at the
			// open when the bar opened above the trigger.
			limit := math.Max(tickDown(s.Trigger*(1+r.EntryLimitPct/100)), tickUp(s.Trigger))
			if bar.O > limit {
				skip(SkipNoFill)
				continue
			}
			base := math.Max(s.Trigger, bar.O)

			tr := buildTrade(s, r, shares, base, limit)
			entriesToday++
			symEntries[c.Sym]++
			pos = append(pos, tr)
			if n := len(open) + 1; n > res.MaxOpen {
				res.MaxOpen = n
			}
		}
		i = j
	}

	// The halt can still trip after the day's last signal (the live trader checks on
	// every update and tick, not only when a signal arrives): keep scanning the
	// minutes until the last position has left, one minute past its exit - pnlAt
	// counts a position as realized only from the minute after effExit. This
	// changes no trade (no entry follows), only whether the day is counted as halted.
	if !halted && len(pos) > 0 {
		var end int64
		for _, p := range pos {
			end = max(end, p.effExit)
		}
		for m := lastCheck + minuteMs; m <= end+minuteMs; m += minuteMs {
			if pnlAt(m) <= -r.MaxDailyLossUSD {
				halted, res.Halted, res.HaltTs = true, true, m
				break
			}
		}
	}

	res.Entries = entriesToday
	for _, p := range pos {
		cp := *p
		cp.cand = nil // do not pin the candidate's bars in the result
		res.Trades = append(res.Trades, cp)
		res.Net += p.Net
		res.Gross += p.Gross
	}
	return res
}

// buildTrade runs one accepted entry to its exit and prices it.
func buildTrade(s Signal, r Rules, shares int64, base, limit float64) *Trade {
	c := s.Cand
	closes := make([]float64, len(c.Bars))
	for i := range c.Bars {
		closes[i] = c.Bars[i].C
	}
	ema := emaSeries(closes, r.Flag.EmaPeriod)
	ex := exitPath(c.Bars, ema, s.Idx, s.Stop, r)

	entryFill := r.Costs.EntryFill(base, limit)
	exitFill := r.Costs.ExitFill(ex.raw, ex.kind)
	n := float64(shares)
	gross := (ex.raw - base) * n
	pxPnL := (exitFill - entryFill) * n
	comm := r.Costs.Commission(shares, entryFill) + r.Costs.Commission(shares, exitFill)
	fees := 2 * r.Costs.Fees(shares)
	risk := n * (s.Trigger - s.Stop)
	exBar := c.Bars[ex.idx]
	eff := exBar.T
	if ex.kind == ExitClose {
		eff = exBar.T + minuteMs - 1 // the close: before the next bar begins
	}
	t := &Trade{
		Variant: r.Name, Date: c.Date, Sym: c.Sym,
		EntryTs: s.Ts, ExitTs: exBar.T, EntryMin: ETMinute(s.Ts), ExitKind: ex.kind,
		Shares: shares, Trigger: s.Trigger, Stop: s.Stop,
		EntryRaw: base, EntryFill: entryFill, ExitRaw: ex.raw, ExitFill: exitFill,
		Gross: gross, Commission: comm, Fees: fees, Slippage: gross - pxPnL, Net: pxPnL - comm - fees,
		InitialRisk: risk, PmVolX: c.PmVolX, Adv10: c.Adv10,
		pxPnL: pxPnL, effExit: eff, entryIx: s.Idx, cand: c,
	}
	if risk > 0 {
		t.RGross, t.RNet = t.Gross/risk, t.Net/risk
	}
	t.SplitFactor = c.splitFactor()
	if f := t.SplitFactor; f != 1 {
		// the same position on the feed's adjusted basis: shares / f at prices * f
		adjShares := int64(math.Round(n / f))
		adjCost := r.Costs.Commission(adjShares, entryFill*f) + r.Costs.Commission(adjShares, exitFill*f) + 2*r.Costs.Fees(adjShares)
		t.SplitCostGap = comm + fees - adjCost
	}
	return t
}

// exit is where and how a position ends.
type exit struct {
	idx  int     // the exit bar
	raw  float64 // the model price
	kind string
}

// exitPath walks the bars after the entry and finds the exit.
//
// Stop mode (autotrade.trailStep, TrailExit "stop"): the protective stop rests
// at the engine's stop and, after every CLOSED bar, is raised (never lowered) to
// the 9 EMA of that bar * (1 - TrailStopPct/100). It fires on the first bar
// whose LOW is at or under it, at min(open, level): a bar that gaps down through
// it fills at the open. The entry bar's own close counts as a closed bar for the
// raise (the trader trails from "the bars that close after this decision", the
// entry bar among them), but the level only takes effect from the NEXT bar.
//
// Close mode (TrailExit "close"): the resting stop stays at the entry's stop
// (a touch of it fires as above); the engine's EXIT - a 1-minute CLOSE under
// max(stop, EMA of an earlier closed bar * (1 - Flag.StopBufferPct/100)) - ends
// the trade at that close.
//
// INSIDE A BAR THE STOP COMES FIRST: if the entry bar's low is at or under the
// stop, the trade is stopped out at the stop on its own entry bar (a minute bar
// cannot say whether the dip came before or after the break).
//
// Both modes flatten at the first bar at or after FlattenMin, at that bar's
// open (the stop is not consulted: the position is flattened at the open). The
// engine's own EXIT is never later than the stop-mode touch (a close under the
// engine's trail implies a low at or under it), so the stop mode ignores it.
func exitPath(bars []core.Bar, ema []float64, i0 int, stop float64, r Rules) exit {
	n := len(bars)
	factor := 1 - r.TrailStopPct/100
	if r.TrailExit == TrailClose {
		factor = 1 - r.Flag.StopBufferPct/100
	}
	lvl := stop // stop mode: the level in force; close mode: the engine's trail (the hard stop stays `stop`)
	raise := func(i int) {
		if e := ema[i]; !math.IsNaN(e) && e > 0 {
			lvl = math.Max(lvl, e*factor)
		}
	}
	if bars[i0].L <= stop {
		return exit{i0, stop, ExitStop}
	}
	raise(i0)
	for i := i0 + 1; i < n; i++ {
		b := bars[i]
		if ETMinute(b.T) >= r.FlattenMin {
			return exit{i, b.O, ExitFlatten}
		}
		if r.TrailExit == TrailStop {
			if b.L <= lvl {
				return exit{i, math.Min(b.O, lvl), ExitStop}
			}
		} else {
			if b.L <= stop {
				return exit{i, math.Min(b.O, stop), ExitStop}
			}
			if b.C < lvl {
				return exit{i, b.C, ExitClose}
			}
		}
		raise(i)
	}
	return exit{n - 1, bars[n-1].C, ExitEOD}
}

// vol5 is the volume of the five 1-minute SLOTS before the entry bar (a minute
// with no print has no bar and counts 0), the backtest's reading of
// autotrade's vol5. The live trader reads the five slots ENDING at the newest
// bar, the still-forming one among them, whose partial volume a minute bar
// cannot reproduce; the bar itself is left out (conservative, no look-ahead).
//
// Only minutes at or after admission count (admitTs, the start of the bar the
// candidate was admitted on; 0 = every minute): the live symState.vols is filled
// by observe() from the updates the trader has received since the symbol was
// admitted, and a minute before that counts 0 ("a symbol only just admitted
// sizes conservatively", trader.go). So an entry within four minutes of admission
// sizes on less liquidity than five full bars - the replay no longer overstates it.
func vol5(bars []core.Bar, idx int, admitTs int64) int64 {
	lo := bars[idx].T - 5*minuteMs
	if admitTs > lo {
		lo = admitTs
	}
	var sum int64
	for k := idx - 1; k >= 0 && bars[k].T >= lo; k-- {
		sum += bars[k].V
	}
	return sum
}

// floorShares is autotrade.floorShares: 50 / (1 - 0.95) is 999.9999999999993 in
// floating point and a share count is whole.
func floorShares(v float64) int64 {
	v += 1e-6
	if math.IsNaN(v) || v < 1 {
		return 0
	}
	return int64(math.Min(math.Floor(v), 1e9))
}

// The exchange price increment: $0.0001 under $1, $0.01 from $1 (autotrade.tick*).
func tickScale(p float64) float64 {
	if p < 1 {
		return 1e4
	}
	return 100
}
func tickDown(p float64) float64 { s := tickScale(p); return math.Max(math.Floor(p*s+1e-6)/s, 0.0001) }
func tickUp(p float64) float64   { s := tickScale(p); return math.Max(math.Ceil(p*s-1e-6)/s, 0.0001) }
