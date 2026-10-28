package orderticket

// Automatic margin preview: whenever the ticket shows a symbol while connected
// (and again when the pane reports a new Position $ / Shares), two what-if
// orders (BUY and SELL short, LMT at the 9 EMA, or the last price when the EMA
// is stale) ask TWS what each side would need. Same what-if safety as
// Prepare's check: fresh ids, orderRef oticket-whatif:, delivered only to the
// waiting round, never adopted, followed or recorded as an order (audited only
// as id-less "whatif" notes).
//
// Rules: one round in flight at a time; at most one round per symbol per 20 s
// unless the quantity changes; a result for a symbol no longer shown is
// dropped; BUY/SELL never wait for it.

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	previewEvery      = 20 * time.Second
	previewDefaultQty = 100
)

// PreviewSide is one side of the preview.
type PreviewSide struct {
	OK        bool    `json:"ok"`
	MaxShares int64   `json:"maxShares"`
	PerShare  float64 `json:"perShare"`
	Note      string  `json:"note"`
	Rejected  bool    `json:"rejected"` // IB refused the what-if (e.g. 201 margin)
}

// Preview is the margin preview shown under the price line.
type Preview struct {
	Sym      string      `json:"sym"`
	Qty      int64       `json:"qty"`
	Long     float64     `json:"longPx"`
	Short    float64     `json:"shortPx"`
	PriceSrc string      `json:"priceSrc"` // 9EMA | last
	Buy      PreviewSide `json:"buy"`
	Sell     PreviewSide `json:"sell"`
	Text     string      `json:"text"`
	At       int64       `json:"at"`
	Currency string      `json:"currency"` // margin $/sh is in the account base currency
}

type previewMark struct {
	at  time.Time
	qty int64
}

// SetPreviewSize is the pane's current Position $ / Shares (debounced there).
func (t *Ticket) SetPreviewSize(positionUSD float64, shares int64, riskUSD float64) {
	t.mu.Lock()
	t.pvPos, t.pvShares, t.pvRisk = positionUSD, shares, riskUSD
	t.mu.Unlock()
}

// previewQtyLocked: Shares, else the risk-based size (long side, planned
// stop from the EMA / the price), else legacy position $, else 100.
func (t *Ticket) previewQtyLocked(price, ema float64) int64 {
	q := int64(0)
	if t.pvShares > 0 {
		q = t.pvShares
	} else if t.pvRisk > 0 && price > 0 {
		q, _ = riskSize("BUY", t.pvRisk, price, stopLevelFor("BUY", ema, price, t.effStopPctLocked()), t.settings.MaxPositionUSD)
	} else if t.pvPos > 0 && price > 0 {
		q = int64(math.Floor(t.pvPos/price + 1e-9))
	}
	if q <= 0 || q > maxOrderShares {
		q = previewDefaultQty
	}
	return q
}

// maybePreview starts a round when due (called from Tick).
func (t *Ticket) maybePreview(now time.Time) {
	t.mu.Lock()
	if t.conn != "connected" || t.client == nil || !t.haveNext || t.pvBusy {
		t.mu.Unlock()
		return
	}
	sym, _ := t.currentSymLocked()
	if sym == "" {
		t.mu.Unlock()
		return
	}
	q := t.quoteLocked(sym, now)
	src, long, short := "9EMA", 0.0, 0.0
	if t.emaUsableLocked(q) {
		long, short = entryPrice("BUY", q.EMA), entryPrice("SELL", q.EMA)
	} else if q.Price > 0 {
		src = "last"
		long, short = snapPrice(q.Price, false), snapPrice(q.Price, true)
	}
	if !(long > 0) || !(short > 0) {
		t.mu.Unlock()
		return
	}
	qty := t.previewQtyLocked(long, map[bool]float64{true: q.EMA, false: 0}[src == "9EMA"])
	if m, ok := t.pvMark[sym]; ok && m.qty == qty && now.Sub(m.at) < previewEvery {
		t.mu.Unlock()
		return
	}
	t.pvMark[sym] = previewMark{at: now, qty: qty}
	t.pvBusy = true
	gen, c, acct := t.gen, t.client, t.account
	bid, sid := t.nextID, t.nextID+1
	t.nextID += 2
	bch, sch := make(chan whatIfRes, 1), make(chan whatIfRes, 1)
	t.whatIfs[bid], t.whatIfs[sid] = bch, sch
	t.wiIDs[bid], t.wiIDs[sid] = true, true
	t.mu.Unlock()

	go func() {
		bp := Pending{Side: "BUY", Sym: sym, Qty: qty, Limit: long}
		sp := Pending{Side: "SELL", Sym: sym, Qty: qty, Limit: short}
		var br, sr whatIfRes
		done := make(chan struct{})
		go func() { sr = t.whatIf(c, sch, sid, acct, sp); close(done) }()
		br = t.whatIf(c, bch, bid, acct, bp)
		<-done

		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.whatIfs, bid)
		delete(t.whatIfs, sid)
		t.pvBusy = false
		if t.gen != gen {
			return
		}
		if cur, _ := t.currentSymLocked(); !strings.EqualFold(cur, sym) {
			return // no longer shown: drop
		}
		avail, have := t.availLocked()
		mb := computeMargin(br, qty, long, avail, have, t.settings.MaxPositionUSD, t.baseCurLocked())
		ms := computeMargin(sr, qty, short, avail, have, t.settings.MaxPositionUSD, t.baseCurLocked())
		p := &Preview{Sym: sym, Qty: qty, Long: long, Short: short, PriceSrc: src, Buy: side(mb), Sell: side(ms), At: t.now().UnixMilli(), Currency: t.baseCurLocked()}
		p.Text = previewText(p)
		t.previews[sym] = p
	}()
}

func side(m MarginCheck) PreviewSide {
	if m.Status == "unavailable" {
		return PreviewSide{Note: m.Note, Rejected: strings.Contains(m.Note, "IB 201") || strings.Contains(strings.ToUpper(m.Note), "MARGIN")}
	}
	return PreviewSide{OK: true, MaxShares: m.MaxShares, PerShare: m.PerShare}
}

// previewText: "Max long N sh · Max short M sh (margin $/sh L / S)".
func previewText(p *Preview) string {
	if !p.Buy.OK && !p.Sell.OK && !p.Buy.Rejected && !p.Sell.Rejected {
		return "margin n/a"
	}
	mx := func(s PreviewSide) string {
		if s.Rejected {
			return "rejected by IB (margin)"
		}
		if !s.OK {
			return "n/a"
		}
		return fmtQty(s.MaxShares) + " sh"
	}
	ps := func(s PreviewSide) string {
		if !s.OK {
			return "n/a"
		}
		return fmt.Sprintf("%.2f", s.PerShare)
	}
	txt := fmt.Sprintf("Max long %s · Max short %s (margin %s/sh %s / %s)", mx(p.Buy), mx(p.Sell), strings.TrimSpace(curSym(p.Currency)), ps(p.Buy), ps(p.Sell))
	if p.PriceSrc == "last" {
		txt += " at last price"
	}
	return txt
}
