package orderticket

// "Power Setup" preset (Settings.Preset == "power"): the researched system,
// applied to the NEXT order(s) when it is selected:
//
//	entry   BUY/SHORT limit at the live 1m 9EMA (as always), cancelled if not
//	        filled within 15 min (per-order entry timeout)
//	stop    6% under (over) the 9EMA at the fill, fixed (no 9EMA follow)
//	TP      3R: fill + 3 x (fill - stop) (short mirrored), re-priced on the
//	        first fill like the % TP
//	BE      at +7.5% (on), exact entry
//	drop-cancel off for these orders (the research did not use it)
//	5m trail: after the first completed clock-aligned 5-minute bar that lies
//	        wholly after the fill, a live price below the previous completed
//	        5m bar's low (above its high for a short; 2 consecutive prices)
//	        exits through the soft-stop machinery (cancel TP, confirmed-fill
//	        sizing, outsideRth LMT exit with re-price steps, never oversells)
//
// The 5m trail and the entry timeout are per-order flags available in Custom
// too (Settings.Trail5m, Settings.EntryTimeoutMin). Risk sizing uses the
// preset's 6% planned stop. All per-order fields are audited with the place
// record, so a restart restores them.

import (
	"context"
	"fmt"
	"math"
	"time"
)

const (
	presetCustom = "custom"
	presetPower  = "power"

	psStopPct     = 6.0
	psTPR         = 3.0
	psBEPct       = 7.5
	psTimeoutMin  = 15
	fiveMin       = int64(5 * 60 * 1000)
	maxTimeoutMin = 240
)

func (t *Ticket) powerLocked() bool { return t.settings.Preset == presetPower }

// effStopPctLocked: the stop % new orders (and risk sizing) use.
func (t *Ticket) effStopPctLocked() float64 {
	if t.powerLocked() {
		return psStopPct
	}
	return t.settings.StopPct
}

// tpForR: fill + r x (fill - stop) long (rounded up), mirrored short (down).
func tpForR(side string, base, stop, r float64) float64 {
	if side == "BUY" {
		return snapPrice(base+r*(base-stop), true)
	}
	return snapPrice(base-r*(stop-base), false)
}

// tpAt: the order's TP for an entry/fill price `base` and stop `stop`.
func (o *ord) tpAt(base, stop float64) float64 {
	if o.TPR > 0 {
		if !(stop > 0) {
			pct := o.StopPct
			if pct <= 0 {
				pct = psStopPct
			}
			stop = stopLevelFor(o.Side, 0, base, pct)
		}
		return tpForR(o.Side, base, stop, o.TPR)
	}
	return tpPrice(o.Side, base, pctOrLegacy(o.TPPct))
}

// applyPresetLocked fills a Pending's preset fields (Prepare).
func (t *Ticket) applyPresetLocked(p *Pending, ema float64) {
	if t.powerLocked() {
		p.Preset, p.TPR, p.Trail5m, p.DropOff, p.EntryTimeoutMin = presetPower, psTPR, true, true, psTimeoutMin
		p.TP = tpForR(p.Side, p.Limit, stopLevelFor(p.Side, ema, p.Limit, psStopPct), psTPR)
		return
	}
	p.Preset, p.Trail5m, p.EntryTimeoutMin = presetCustom, t.settings.Trail5m, t.settings.EntryTimeoutMin
}

// presetToOrderLocked copies the Pending's preset onto the new order (Confirm).
func (t *Ticket) presetToOrderLocked(o *ord, p *Pending, now time.Time) {
	o.Preset, o.TPR, o.Trail5m, o.DropOff = p.Preset, p.TPR, p.Trail5m, p.DropOff
	if p.EntryTimeoutMin > 0 {
		o.EntryDeadline = now.Add(time.Duration(p.EntryTimeoutMin) * time.Minute).UnixMilli()
	}
	if p.Preset == presetPower {
		o.StopFollow = false
		o.BEOff, o.BEPct, o.BEOffsetPct = false, psBEPct, 0
	}
}

// presetLabel: "PS · SL x · TP 3R y · 5m trail" for the order row.
func presetLabel(o *ord) string {
	s := ""
	if o.Preset == presetPower {
		s = "PS"
		if o.StopLevel > 0 {
			s += " · SL " + fmtPx(o.StopLevel)
		}
		s += fmt.Sprintf(" · TP %sR %s", trimFloat(o.TPR), fmtPx(o.TP))
	}
	if o.Trail5m {
		if s != "" {
			s += " · "
		}
		s += "5m trail"
	}
	return s
}

// ─── entry timeout ───────────────────────────────────────────────────────

func (t *Ticket) entryTimeoutLocked(now time.Time) []func() {
	if t.conn != "connected" || t.client == nil {
		return nil
	}
	c := t.client
	var acts []func()
	for _, o := range t.orders {
		if o.EntryDeadline <= 0 || now.UnixMilli() < o.EntryDeadline {
			continue
		}
		o.EntryDeadline = -1 // once
		if !o.parentActive() || o.cancelled || o.StopState == stTriggered || o.StopState == stExiting {
			continue
		}
		o.cancelled, o.Following = true, false
		id := o.ID
		r := auditRec{Ev: "cancel", ID: id, Sym: o.Sym, Note: "entry timeout"}
		acts = append(acts, func() { t.record(r); c.CancelOrder(id) })
		what := "entry"
		if o.Filled == 0 && o.childActive() {
			cid := o.ChildID
			r2 := auditRec{Ev: "cancel", ID: cid, Sym: o.Sym, Note: "entry timeout (TP of unfilled entry)"}
			acts = append(acts, func() { t.record(r2); c.CancelOrder(cid) })
		} else if o.Filled > 0 {
			what = "rest of entry"
		}
		o.Note = "entry timeout"
		t.note = fmt.Sprintf("%s %s cancelled: not filled in time", o.Sym, what)
	}
	return acts
}

// ─── 5m trail ────────────────────────────────────────────────────────────

// trailCheckLocked: exit when the live price breaks the previous completed
// 5m bar (wholly after the fill) - low for a long, high for a short.
func (t *Ticket) trailCheckLocked(o *ord, last float64, now time.Time) []func() {
	if !o.Trail5m || o.held() <= 0 || t.conn != "connected" || t.client == nil {
		return nil
	}
	switch o.StopState {
	case stTriggered, stExiting, stDone, stDisarmed:
		return nil
	}
	nowMs := now.UnixMilli()
	cur := nowMs - ((nowMs%fiveMin)+fiveMin)%fiveMin
	prev := cur - fiveMin
	if o.FillAt > 0 && prev < o.FillAt { // that bar is not wholly after the fill yet
		o.trailHits = 0
		return nil
	}
	lo, hi, n := t.book.extremes(o.Sym, prev, cur)
	if n == 0 {
		return nil
	}
	hit := last < lo
	ref := lo
	if o.Side == "SELL" {
		hit, ref = last > hi, hi
	}
	if !hit {
		o.trailHits = 0
		return nil
	}
	o.trailHits++
	if o.trailHits < 2 {
		return nil
	}
	o.trailHits = 0
	o.StopOff = false // the exit runs through the soft-stop path
	o.StopState = stTriggered
	o.Following = false
	cmp := "<"
	if o.Side == "SELL" {
		cmp = ">"
	}
	why := fmt.Sprintf("5m trail exit: last %s %s 5m %s %s", fmtPx(last), cmp, map[bool]string{true: "low", false: "high"}[o.Side == "BUY"], fmtPx(ref))
	o.Note = why
	t.note = o.Sym + " " + why
	acts := []func(){t.stopRec(o, why)}
	acts = append(acts, t.stopCancelsLocked(o, now)...)
	acts = append(acts, t.stopProgressLocked(o, now)...)
	return acts
}

// ─── settings ────────────────────────────────────────────────────────────

// SetPreset selects the preset for the next orders and the Custom 5m-trail /
// entry-timeout defaults (timeout minutes 0 = off).
func (t *Ticket) SetPreset(ctx context.Context, name string, trail5m bool, timeoutMin int) error {
	if name != presetCustom && name != presetPower {
		return fmt.Errorf("unknown preset %q", name)
	}
	if timeoutMin < 0 || timeoutMin > maxTimeoutMin {
		return fmt.Errorf("entry timeout must be 0-%d min", maxTimeoutMin)
	}
	t.mu.Lock()
	s := t.settings
	s.Preset, s.Trail5m, s.EntryTimeoutMin = name, trail5m, timeoutMin
	if name == presetPower { // the Power Setup is traded at 3 % of the account
		s.RiskMode = riskModePct
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

var _ = math.Abs

const (
	riskModeUSD = "$"
	riskModePct = "%"
	// fxUSDKey: where the USD ledger ExchangeRate is kept in Ticket.funds.
	fxUSDKey = "$fx:USD"
)
