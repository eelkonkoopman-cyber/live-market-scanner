package orderticket

// Auto-cancel on a fast drop into the 9EMA ("cancel on fast drop"): a working
// entry that has not (fully) filled is cancelled when ALL hold
//
//	BUY:   (1) the 9EMA is falling: EMA now (order's mode) < EMA as of the
//	           previous closed bar;
//	       (2) the price drops fast: last <= (highest trade in the last
//	           DropSec) x (1 - DropPct%);
//	       (3) the price is near the 9EMA: last <= EMA x (1 + NearPct%).
//	SHORT: mirrored (EMA rising, last >= low x (1 + DropPct%), last >= EMA x
//	       (1 - NearPct%)).
//
// The tape is the ticket's own live price feed (FeedAgg closes, per symbol,
// kept 5 min). Checked on every live price, debounced like the soft stop (2
// consecutive prices or 1 s), BEFORE the follow loop can move the limit into
// the drop: on trigger following stops and the entry is cancelled - with its
// TP if nothing filled; a partly filled entry loses only its remainder (TP and
// soft stop keep protecting the filled shares). Only ever sends cancels, only
// for ticket orders.

import (
	"fmt"
	"time"
)

type tapePt struct {
	at time.Time
	px float64
}

const tapeKeep = 5 * time.Minute

// tapeAddLocked records one live price and trims the tape.
func (t *Ticket) tapeAddLocked(sym string, px float64, now time.Time) {
	tp := append(t.tape[sym], tapePt{now, px})
	i := 0
	for i < len(tp) && now.Sub(tp[i].at) > tapeKeep {
		i++
	}
	t.tape[sym] = tp[i:]
}

// tapeExtremesLocked: highest and lowest price within the last `win`.
func (t *Ticket) tapeExtremesLocked(sym string, now time.Time, win time.Duration) (hi, lo float64) {
	for _, p := range t.tape[sym] {
		if now.Sub(p.at) > win {
			continue
		}
		if p.px > hi {
			hi = p.px
		}
		if lo == 0 || p.px < lo {
			lo = p.px
		}
	}
	return hi, lo
}

type dropCheck struct {
	hit           bool
	ema, prevEMA  float64
	movePct, last float64
}

// dropCondLocked evaluates the three conditions for o at price last.
func (t *Ticket) dropCondLocked(o *ord, last float64, now time.Time) dropCheck {
	s := t.settings
	q := t.book.quoteMode(o.Sym, now, o.EMALive)
	prev := t.book.quoteMode(o.Sym, now.Add(-time.Minute), false) // EMA as of the previous closed bar
	d := dropCheck{ema: q.EMA, prevEMA: prev.EMA, last: last}
	if q.EMA <= 0 || prev.EMA <= 0 {
		return d
	}
	hi, lo := t.tapeExtremesLocked(o.Sym, now, time.Duration(s.DropSec)*time.Second)
	if o.Side == "BUY" {
		if hi <= 0 {
			return d
		}
		d.movePct = (last/hi - 1) * 100 // negative
		d.hit = q.EMA < prev.EMA && last <= hi*(1-s.DropPct/100)+1e-12 && last <= q.EMA*(1+s.NearPct/100)+1e-12
		return d
	}
	if lo <= 0 {
		return d
	}
	d.movePct = (last/lo - 1) * 100 // positive
	d.hit = q.EMA > prev.EMA && last >= lo*(1+s.DropPct/100)-1e-12 && last >= q.EMA*(1-s.NearPct/100)-1e-12
	return d
}

// dropCheckLocked runs the guard for one order (fresh = a new live price).
func (t *Ticket) dropCheckLocked(o *ord, last float64, now time.Time, fresh bool) []func() {
	if t.settings.DropCancelOff || t.conn != "connected" || t.client == nil {
		return nil
	}
	if o.DropOff || !o.parentActive() || o.cancelled || o.Filled >= o.Qty || o.StopState == stTriggered || o.StopState == stExiting {
		return nil
	}
	d := t.dropCondLocked(o, last, now)
	if !d.hit {
		if fresh {
			o.dropHits = 0
		}
		return nil
	}
	if o.dropHits == 0 {
		o.dropFirst = now
	}
	if fresh || o.dropHits == 0 {
		o.dropHits++
	}
	if o.dropHits < 2 && now.Sub(o.dropFirst) < stopConfirmAfter {
		return nil
	}
	o.dropHits = 0
	o.Following = false
	o.cancelled = true
	verb := "falling"
	if o.Side == "SELL" {
		verb = "rising"
	}
	msg := fmt.Sprintf("Cancelled %s entry: 9EMA %s + price %+.1f%% in %d s (near 9EMA)", o.Sym, verb, d.movePct, t.settings.DropSec)
	o.Note = msg
	t.note = msg
	c := t.client
	rec := auditRec{Ev: "autocancel", ID: o.ID, Sym: o.Sym, Limit: d.last,
		Note: fmt.Sprintf("ema %.4f prev %.4f move %.2f%% last %.4f filled %d/%d", d.ema, d.prevEMA, d.movePct, d.last, o.Filled, o.Qty)}
	id := o.ID
	acts := []func(){func() { t.record(rec); c.CancelOrder(id) }}
	if o.Filled == 0 && o.childActive() {
		cid := o.ChildID
		r2 := auditRec{Ev: "cancel", ID: cid, Sym: o.Sym, Note: "autocancel (TP of unfilled entry)"}
		acts = append(acts, func() { t.record(r2); c.CancelOrder(cid) })
	}
	return acts
}

// dropTickLocked: the 1 s rule (a hit not contradicted by a newer price).
func (t *Ticket) dropTickLocked(now time.Time) []func() {
	var acts []func()
	for _, o := range t.orders {
		if o.dropHits > 0 {
			if last := t.book.quote(o.Sym, now).Price; last > 0 {
				acts = append(acts, t.dropCheckLocked(o, last, now, false)...)
			}
		}
	}
	return acts
}
