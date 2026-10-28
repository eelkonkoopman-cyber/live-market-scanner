package orderticket

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// ─── fake EClient (no network) ───────────────────────────────────────────

type placed struct {
	id int64
	c  Contract
	o  Order
}

type fake struct {
	mu        sync.Mutex
	w         Wrapper
	conns     int
	places    []placed
	cancels   []int64
	groupSubs []int
	openReqs  int
	syms      map[int64]string // conId -> symbol for ReqContractDetails
	account   string
	nextID    int64
	onOpen    func(w Wrapper) // answers ReqOpenOrders
	// margin check
	whatIfs     []placed
	whatIfReply func(o *Order) *OrderState // nil result: TWS stays silent
	avail       string                     // AvailableFunds sent on ReqAccountSummary ("" = none)
	acctSubs    int
	acctCancels int
	mdReqs      []mdCall
	mdCancels   []int64
}

func (f *fake) Connect(string, int, int64) error { f.mu.Lock(); f.conns++; f.mu.Unlock(); return nil }
func (f *fake) Disconnect() error                { return nil }
func (f *fake) IsConnected() bool                { return true }
func (f *fake) ReqManagedAccts() {
	go func() {
		f.w.NextValidID(f.nextID)
		f.w.ManagedAccounts([]string{f.account})
	}()
}
func (f *fake) ReqCurrentTime() { f.w.CurrentTime(0) }
func (f *fake) ReqOpenOrders() {
	f.mu.Lock()
	f.openReqs++
	cb := f.onOpen
	f.mu.Unlock()
	if cb != nil {
		cb(f.w)
	}
}
func (f *fake) PlaceOrder(id int64, c *Contract, o *Order) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o.WhatIf {
		f.whatIfs = append(f.whatIfs, placed{id, *c, *o})
		reply := f.whatIfReply
		if reply == nil {
			reply = defaultWhatIf
		}
		if st := reply(o); st != nil {
			ct, oo := *c, *o
			go f.w.OpenOrder(id, &ct, &oo, st)
		}
		return
	}
	f.places = append(f.places, placed{id, *c, *o})
}

// defaultWhatIf: 25% initial / 20% maintenance margin of qty x limit.
func defaultWhatIf(o *Order) *OrderState {
	v := float64(o.TotalQuantity) * o.LmtPrice
	return &OrderState{WhatIf: true, Status: "PreSubmitted", Parsed: true,
		InitMarginChange: strconv.FormatFloat(v*0.25, 'f', 2, 64), MaintMarginChange: strconv.FormatFloat(v*0.20, 'f', 2, 64)}
}

func (f *fake) ReqAccountSummary(reqID int64, group, tags string) {
	f.mu.Lock()
	f.acctSubs++
	avail := f.avail
	f.mu.Unlock()
	if avail != "" {
		go f.w.AccountSummary(reqID, f.account, "AvailableFunds", avail, "USD")
	}
}
func (f *fake) CancelAccountSummary(int64) { f.mu.Lock(); f.acctCancels++; f.mu.Unlock() }
func (f *fake) ReqMktData(reqID int64, c *Contract, generic string) {
	f.mu.Lock()
	f.mdReqs = append(f.mdReqs, mdCall{reqID, c.Symbol, generic})
	f.mu.Unlock()
}
func (f *fake) CancelMktData(reqID int64) {
	f.mu.Lock()
	f.mdCancels = append(f.mdCancels, reqID)
	f.mu.Unlock()
}

type mdCall struct {
	id      int64
	sym     string
	generic string
}

func (f *fake) whatIfsCopy() []placed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]placed(nil), f.whatIfs...)
}
func (f *fake) CancelOrder(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
}
func (f *fake) SubscribeToGroupEvents(_ int64, g int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupSubs = append(f.groupSubs, g)
}
func (f *fake) UnsubscribeFromGroupEvents(int64) {}
func (f *fake) ReqContractDetails(reqID int64, c *Contract) {
	f.mu.Lock()
	sym := f.syms[c.ConID]
	f.mu.Unlock()
	cd := &ContractDetails{}
	cd.Contract.Symbol = sym
	cd.Contract.SecType = "STK"
	f.w.ContractDetails(reqID, cd)
}

func (f *fake) placesCopy() []placed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]placed(nil), f.places...)
}

// ─── rig ─────────────────────────────────────────────────────────────────

type rig struct {
	t    *testing.T
	tk   *Ticket
	f    *fake
	mu   sync.Mutex
	now  time.Time
	aud  *MemAudit
	meta map[string]string
}

// 10:30:30 ET on a weekday.
var t0 = time.Date(2026, 10, 14, 14, 30, 30, 0, time.UTC)

func newRig(t *testing.T, aud *MemAudit) *rig {
	t.Helper()
	if aud == nil {
		aud = &MemAudit{}
	}
	r := &rig{t: t, now: t0, aud: aud, meta: map[string]string{}}
	r.f = &fake{syms: map[int64]string{111: "SGRX", 222: "ABCD"}, account: "U1234567", nextID: 100, avail: "1000000"}
	tk, err := New(Config{
		Audit:         aud,
		WhatIfTimeout: 300 * time.Millisecond,
		Now:           func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		Factory: func(w Wrapper) EClient {
			r.f.w = w
			return r.f
		},
		GetMeta: func(_ context.Context, k string) (string, error) { return r.meta[k], nil },
		SetMeta: func(_ context.Context, k, v string) error { r.meta[k] = v; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.tk = tk
	return r
}

func (r *rig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

func (r *rig) connect() {
	r.t.Helper()
	r.tk.Connect()
	deadline := time.Now().Add(3 * time.Second)
	for r.tk.State().Conn != "connected" {
		if time.Now().After(deadline) {
			r.t.Fatalf("never connected: %+v", r.tk.State())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// show makes TWS's display group show conId.
func (r *rig) show(conID string) { r.f.w.DisplayGroupUpdated(groupReqID, conID+"@SMART") }

// bars: n closed 1m bars ending with the minute before now, all closing at c.
func (r *rig) seedBars(sym string, n int, c float64) {
	cur := minuteOf(r.now.UnixMilli())
	var bars []core.Bar
	for i := n; i >= 1; i-- {
		bars = append(bars, core.Bar{T: cur - int64(i)*60000, O: c, H: c, L: c, C: c, V: 100})
	}
	r.tk.SeedBars(sym, bars)
}

// closeBarAt appends a closed bar for the current minute and moves to the next.
func (r *rig) closeBar(sym string, c float64) {
	r.tk.FeedAgg(core.Agg{Sym: sym, StartMs: minuteOf(r.now.UnixMilli()), Open: c, High: c, Low: c, Close: c, Vol: 10})
	r.advance(60 * time.Second)
}

func (r *rig) ready(sym string, conID string, ema float64) {
	r.connect()
	r.seedBars(sym, 30, ema)
	r.show(conID)
	if s := r.tk.State(); s.Sym != sym || s.SymSource != "TWS" {
		r.t.Fatalf("ticket follows %q via %q, want %s via TWS", s.Sym, s.SymSource, sym)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ─── tests ───────────────────────────────────────────────────────────────

func TestRounding(t *testing.T) {
	cases := []struct {
		side      string
		ema       float64
		wantEntry float64
		wantTP    float64
	}{
		{"BUY", 0.81236, 0.8123, 0.8936},  // 0.8123*1.1 = 0.89353 -> up 0.8936
		{"SELL", 0.81231, 0.8124, 0.7311}, // 0.8124*0.9 = 0.73116 -> down 0.7311
		{"BUY", 12.3456, 12.34, 13.58},    // 13.574 -> 13.58
		{"SELL", 12.3412, 12.35, 11.11},   // 11.115 -> 11.11
		{"BUY", 0.65, 0.65, 0.715},        // on-tick prices never move
		{"BUY", 0.9999, 0.9999, 1.10},     // 1.09989 crosses $1: cent tick, up
	}
	for _, c := range cases {
		e := entryPrice(c.side, c.ema)
		if !near(e, c.wantEntry) {
			t.Errorf("%s entry(%v) = %v, want %v", c.side, c.ema, e, c.wantEntry)
		}
		if tp := tpPrice(c.side, e, 10); !near(tp, c.wantTP) {
			t.Errorf("%s tp(%v) = %v, want %v", c.side, e, tp, c.wantTP)
		}
	}
}

func TestSizing(t *testing.T) {
	if q := sizeQty(1000, 0.8123, 0.8123, 5000); q != 1231 { // floor(1231.07)
		t.Fatalf("qty %d", q)
	}
	if q := sizeQty(5000, 2.5, 2.51, 5000); q != 1992 { // 2000 x 2.51 > cap -> trimmed
		t.Fatalf("short qty trimmed to the cap: %d", q)
	}
	if q := sizeQty(1, 5, 5, 5000); q != 0 {
		t.Fatalf("qty below one share must be 0, got %d", q)
	}
	if q := sizeQty(100, 0, 0, 5000); q != 0 {
		t.Fatal("no EMA, no qty")
	}
}

func TestBuyPlacesParentWithAttachedTP(t *testing.T) {
	r := newRig(t, nil)
	r.f.nextID = 99 // the what-if margin check takes 99; the real orders 100/101
	r.ready("SGRX", "111", 0.81236)
	if len(r.f.groupSubs) != 1 || r.f.groupSubs[0] != 1 {
		t.Fatalf("group subscription %v", r.f.groupSubs)
	}
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "BUY 1,230 SGRX @ 0.8123 (9EMA) ~$999, TP +10% 0.8936, SL 1% under 9EMA (soft)" {
		t.Fatalf("confirm text %q", p.Text)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("Prepare sent %d orders", n)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if len(ps) != 2 {
		t.Fatalf("want parent + child, got %d", len(ps))
	}
	par, ch := ps[0], ps[1]
	if par.id != 100 || par.o.Action != "BUY" || par.o.OrderType != "LMT" || !near(par.o.LmtPrice, 0.8123) ||
		par.o.TIF != "DAY" || !par.o.OutsideRTH || par.o.Transmit || par.o.ParentID != 0 || par.o.TotalQuantity.Int() != 1230 ||
		par.o.Account != "U1234567" || par.c.Symbol != "SGRX" || par.c.SecType != "STK" {
		t.Fatalf("parent %+v", par.o)
	}
	if ch.id != 101 || ch.o.ParentID != 100 || !ch.o.Transmit || ch.o.Action != "SELL" || ch.o.TIF != "GTC" ||
		!ch.o.OutsideRTH || !near(ch.o.LmtPrice, 0.8936) || ch.o.TotalQuantity.Int() != 1230 {
		t.Fatalf("child %+v", ch.o)
	}
	if !strings.HasPrefix(par.o.OrderRef, refPrefix) || !strings.HasPrefix(ch.o.OrderRef, refPrefix) {
		t.Fatal("orders must carry the ticket's orderRef")
	}
	// the ids are in the audit
	got := map[int64]bool{}
	for _, a := range r.aud.Recs {
		if a.Ev == "place" {
			got[a.ID] = true
		}
	}
	if !got[100] || !got[101] {
		t.Fatalf("audit %+v", r.aud.Recs)
	}
	if s := r.tk.State(); s.Live != true {
		t.Fatal("U account must show LIVE")
	}
}

func TestShortSide(t *testing.T) {
	r := newRig(t, nil)
	r.f.account = "DU5491699"
	r.ready("SGRX", "111", 0.81231)
	p, err := r.tk.Prepare("SELL", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ps := r.f.placesCopy()
	if ps[0].o.Action != "SELL" || !near(ps[0].o.LmtPrice, 0.8124) || ps[0].o.TotalQuantity.Int() != 1231 {
		t.Fatalf("short entry %+v", ps[0].o)
	}
	if ps[1].o.Action != "BUY" || !near(ps[1].o.LmtPrice, 0.7311) || ps[1].o.ParentID != ps[0].id {
		t.Fatalf("short TP %+v", ps[1].o)
	}
	if !strings.HasPrefix(p.Text, "SHORT 1,231 SGRX @ 0.8124") {
		t.Fatalf("text %q", p.Text)
	}
	if r.tk.State().Live {
		t.Fatal("D account must show PAPER")
	}
}

func TestConfirmRequired(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.5)
	if err := r.tk.Confirm("", "SGRX"); err == nil {
		t.Fatal("confirm without prepare must fail")
	}
	p, _ := r.tk.Prepare("BUY", 1000, "SGRX")
	if err := r.tk.Confirm("bogus", "SGRX"); err == nil {
		t.Fatal("wrong token accepted")
	}
	// the wrong token consumed the pending order: the right one is gone too
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("a pending order must not survive a failed confirm")
	}
	p, _ = r.tk.Prepare("BUY", 1000, "SGRX")
	r.advance(31 * time.Second)
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("expired confirmation accepted")
	}
	p, _ = r.tk.Prepare("BUY", 1000, "SGRX")
	r.tk.Abort()
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("aborted confirmation accepted")
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent without a valid confirm", n)
	}
	// double click on confirm sends one order pair, not two
	p, _ = r.tk.Prepare("BUY", 1000, "SGRX")
	_ = r.tk.Confirm(p.Token, "SGRX")
	_ = r.tk.Confirm(p.Token, "SGRX")
	if n := len(r.f.placesCopy()); n != 2 {
		t.Fatalf("double confirm sent %d orders", n)
	}
}

func TestRefusals(t *testing.T) {
	r := newRig(t, nil)
	if _, err := r.tk.Prepare("BUY", 1000, "SGRX"); err == nil {
		t.Fatal("prepare while disconnected")
	}
	r.ready("SGRX", "111", 1.5)
	if _, err := r.tk.Prepare("BUY", 6000, "SGRX"); err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("over the cap: %v", err)
	}
	if _, err := r.tk.Prepare("BUY", 1, "SGRX"); err == nil || !strings.Contains(err.Error(), "quantity is 0") {
		t.Fatalf("qty 0: %v", err)
	}
	if _, err := r.tk.Prepare("BUY", 0, "SGRX"); err == nil {
		t.Fatal("position $ 0 accepted")
	}
	r.seedBars("ZERO", 30, 0) // zero closes are dropped -> no EMA
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatal("orders sent")
	}
}

func TestStaleEMARefused(t *testing.T) {
	r := newRig(t, nil)
	r.connect()
	r.seedBars("SGRX", 30, 1.5)
	r.show("111")
	r.advance(3 * time.Minute) // no bar for 3 minutes: fine (thin pre-market), StaleMin is 15
	if _, err := r.tk.Prepare("BUY", 1000, "SGRX"); err != nil {
		t.Fatalf("3m-old EMA refused: %v", err)
	}
	if v := r.tk.State(); !v.EMAOK || v.Quote.AgeSec < 120 {
		t.Fatalf("%+v", v.Quote)
	}
	r.advance(14 * time.Minute) // newest bar now 16m old
	if r.tk.State().EMAOK {
		t.Fatal("16m-old EMA usable")
	}
	if _, err := r.tk.Prepare("BUY", 1000, "SGRX"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale EMA at prepare: %v", err)
	}
	// stale between prepare and confirm
	r2 := newRig(t, nil)
	r2.ready("SGRX", "111", 1.5)
	p, err := r2.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	r2.advance(29 * time.Second) // still within the confirm TTL...
	r2.mu.Lock()
	r2.now = r2.now.Add(0)
	r2.mu.Unlock()
	// ...but the bars are gone (dropped book = 9EMA loading): refused
	r2.tk.book.drop("SGRX")
	if err := r2.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "9EMA") {
		t.Fatalf("stale EMA at confirm: %v", err)
	}
	// too few bars for a 9 EMA
	r3 := newRig(t, nil)
	r3.connect()
	r3.seedBars("SGRX", 5, 1.5)
	r3.show("111")
	if _, err := r3.tk.Prepare("BUY", 1000, "SGRX"); err == nil {
		t.Fatal("EMA with 5 bars accepted")
	}
	if len(r.f.placesCopy())+len(r2.f.placesCopy())+len(r3.f.placesCopy()) != 0 {
		t.Fatal("orders sent")
	}
}

func TestSymbolMismatchRefused(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.5)
	r.seedBars("ABCD", 30, 3)
	if _, err := r.tk.Prepare("BUY", 1000, "ABCD"); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("ticket/TWS mismatch at prepare: %v", err)
	}
	p, err := r.tk.Prepare("BUY", 1000, "SGRX")
	if err != nil {
		t.Fatal(err)
	}
	r.show("222") // TWS switches to ABCD before the confirm click
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil {
		t.Fatal("confirm after TWS changed symbol accepted")
	}
	// the pane still shows the old symbol
	p, _ = r.tk.Prepare("BUY", 1000, "ABCD")
	if err := r.tk.Confirm(p.Token, "SGRX"); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("pane symbol mismatch: %v", err)
	}
	// TWS shows an unresolved contract: no symbol, nothing tradable
	r.f.w.DisplayGroupUpdated(groupReqID, "none")
	if s := r.tk.State(); s.Sym != "" {
		t.Fatalf("sym %q after none", s.Sym)
	}
	if n := len(r.f.placesCopy()); n != 0 {
		t.Fatalf("%d orders sent", n)
	}
}

func TestFallbackSymbolWithoutGroupEvents(t *testing.T) {
	r := newRig(t, nil)
	r.connect()
	r.tk.SetFallbackSymbol("sgrx")
	r.seedBars("SGRX", 30, 1.5)
	if s := r.tk.State(); s.Sym != "SGRX" || s.SymSource != "scanner" {
		t.Fatalf("fallback %+v", s)
	}
	r.show("222")
	if s := r.tk.State(); s.Sym != "ABCD" || s.SymSource != "TWS" {
		t.Fatalf("TWS must win over the fallback: %+v", s)
	}
}

func TestFollowModifiesSameOrderOnce(t *testing.T) {
	r := newRig(t, nil)
	r.f.nextID = 99 // the what-if margin check takes 99; the real orders 100/101
	r.ready("SGRX", "111", 1.50)
	p, _ := r.tk.Prepare("BUY", 1000, "SGRX")
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.tk.Tick(ctx) // no new closed bar: nothing
	if n := len(r.f.placesCopy()); n != 2 {
		t.Fatalf("tick without a new bar sent %d", n-2)
	}
	r.f.w.OrderStatus(100, "Submitted", Qty(0), Qty(666), 0, 9100, 0, 0, 80, "", 0)
	r.closeBar("SGRX", 1.70) // EMA rises: 1.5 + 0.2*0.2 = 1.54
	r.tk.Tick(ctx)
	r.tk.Tick(ctx) // same bar again: no second modify
	ps := r.f.placesCopy()
	if len(ps) != 4 {
		t.Fatalf("want 1 parent + 1 TP modify, got %d sends", len(ps)-2)
	}
	if ps[2].id != 100 || !near(ps[2].o.LmtPrice, 1.54) || !ps[2].o.Transmit || ps[2].o.TotalQuantity.Int() != 666 {
		t.Fatalf("parent modify %+v", ps[2].o)
	}
	if ps[3].id != 101 || ps[3].o.ParentID != 100 || !near(ps[3].o.LmtPrice, 1.70) {
		t.Fatalf("TP modify %+v", ps[3].o)
	}
	ids := map[int64]bool{}
	for _, x := range ps {
		ids[x.id] = true
	}
	if len(ids) != 2 {
		t.Fatalf("follow must modify, never add orders: ids %v", ids)
	}

	// partial fill: TP moves to the fill average; the entry keeps following
	r.f.w.OrderStatus(100, "Submitted", Qty(200), Qty(466), 1.53, 9100, 0, 1.53, 80, "", 0)
	ps = r.f.placesCopy()
	if len(ps) != 5 || ps[4].id != 101 || !near(ps[4].o.LmtPrice, 1.69) { // 1.53*1.1 = 1.683 -> 1.69
		t.Fatalf("TP after partial %+v", ps[len(ps)-1])
	}
	r.closeBar("SGRX", 1.90)
	r.tk.Tick(ctx)
	ps = r.f.placesCopy()
	if len(ps) != 6 || ps[5].id != 100 {
		t.Fatalf("partially filled entry must keep following (no TP re-price): %d sends", len(ps))
	}

	// full fill: no more entry modifies
	r.f.w.OrderStatus(100, "Filled", Qty(666), Qty(0), 1.53, 9100, 0, 1.53, 80, "", 0)
	before := len(r.f.placesCopy())
	r.closeBar("SGRX", 2.50)
	r.tk.Tick(ctx)
	r.closeBar("SGRX", 2.60)
	r.tk.Tick(ctx)
	if n := len(r.f.placesCopy()); n != before {
		t.Fatalf("modified after the fill: %d sends", n-before)
	}
	if v := r.tk.State().Orders[0]; v.Following || v.Filled != 666 {
		t.Fatalf("after fill %+v", v)
	}
}

func TestFollowStopsOnCancelAndClose(t *testing.T) {
	r := newRig(t, nil)
	r.f.nextID = 99 // the what-if margin check takes 99; the real orders 100/101
	r.ready("SGRX", "111", 1.50)
	p, _ := r.tk.Prepare("BUY", 1000, "SGRX")
	_ = r.tk.Confirm(p.Token, "SGRX")
	if err := r.tk.Cancel(100); err != nil {
		t.Fatal(err)
	}
	if len(r.f.cancels) != 1 || r.f.cancels[0] != 100 {
		t.Fatalf("cancels %v", r.f.cancels)
	}
	r.closeBar("SGRX", 1.9)
	r.tk.Tick(context.Background())
	if n := len(r.f.placesCopy()); n != 2 {
		t.Fatal("followed after cancel")
	}

	// 16:00 ET
	r2 := newRig(t, nil)
	r2.mu.Lock()
	r2.now = time.Date(2026, 10, 14, 19, 58, 30, 0, time.UTC) // 15:58:30 ET
	r2.mu.Unlock()
	r2.ready("SGRX", "111", 1.50)
	p, _ = r2.tk.Prepare("BUY", 1000, "SGRX")
	_ = r2.tk.Confirm(p.Token, "SGRX")
	r2.closeBar("SGRX", 1.9) // 15:59:30
	r2.closeBar("SGRX", 1.9) // 16:00:30
	r2.tk.Tick(context.Background())
	if n := len(r2.f.placesCopy()); n != 2 {
		t.Fatalf("followed after 16:00 ET: %d sends", n-2)
	}
	if v := r2.tk.State().Orders[0]; v.Following {
		t.Fatal("still following after 16:00 ET")
	}
}

func TestReconnectReconcileIgnoresForeign(t *testing.T) {
	aud := &MemAudit{Recs: []auditRec{
		{Ev: "place", ID: 500, ClientID: 80, Sym: "SGRX", Side: "BUY", Qty: 100, Limit: 1.5},
		{Ev: "place", ID: 501, ParentID: 500, ClientID: 80, Sym: "SGRX", Side: "BUY", Qty: 100, Limit: 1.65},
		{Ev: "perm", ID: 500, PermID: 9500},
		{Ev: "place", ID: 510, ClientID: 80, Sym: "ABCD", Side: "BUY", Qty: 100, Limit: 3},
		{Ev: "perm", ID: 510, PermID: 9510},
	}}
	r := newRig(t, aud)
	order := func(id, parent, perm int64, ref, action string, lmt float64) *Order {
		o := &Order{}
		o.Action, o.OrderType, o.LmtPrice, o.TIF = action, "LMT", lmt, "DAY"
		o.TotalQuantity = Qty(100)
		o.PermID, o.ParentID, o.OrderRef, o.ClientID = perm, parent, ref, 80
		return o
	}
	ct := func(sym string) *Contract { c := &Contract{}; c.Symbol = sym; return c }
	st := &OrderState{Status: "Submitted"}
	r.f.onOpen = func(w Wrapper) {
		w.OpenOrder(500, ct("SGRX"), order(500, 0, 9500, "oticket:SGRX", "BUY", 1.5), st)
		w.OpenOrder(501, ct("SGRX"), order(501, 500, 9501, "oticket:SGRX:tp", "SELL", 1.65), st)
		w.OpenOrder(510, ct("ABCD"), order(510, 0, 7777, "oticket:ABCD", "BUY", 3), st) // perm id differs: someone else's id reuse
		w.OpenOrder(600, ct("XYZ"), order(600, 0, 9600, "manual", "BUY", 1), st)        // foreign
		w.OpenOrder(601, ct("XYZ"), order(601, 0, 9601, "oticket:XYZ", "BUY", 1), st)   // our tag but not in the audit
		w.OpenOrder(1, ct("QQQ"), order(1, 0, 9001, "", "SELL", 400), st)               // typed in TWS
	}
	r.ready("SGRX", "111", 1.5)
	s := r.tk.State()
	if len(s.Orders) != 1 || s.Orders[0].ID != 500 || s.Orders[0].ChildID != 501 || !near(s.Orders[0].TP, 1.65) {
		t.Fatalf("adopted %+v", s.Orders)
	}
	if !s.Orders[0].Following {
		t.Fatal("an adopted working DAY entry resumes following")
	}
	for _, id := range []int64{510, 600, 601, 1} {
		if err := r.tk.Cancel(id); err == nil {
			t.Fatalf("cancel of foreign order %d accepted", id)
		}
	}
	_ = r.tk.CancelAll()
	if len(r.f.cancels) != 1 || r.f.cancels[0] != 500 {
		t.Fatalf("cancel all touched %v", r.f.cancels)
	}
	// follow modifies nothing foreign
	r2 := newRig(t, aud)
	r2.f.onOpen = r.f.onOpen
	r2.ready("SGRX", "111", 1.5)
	r2.closeBar("SGRX", 1.9)
	r2.tk.Tick(context.Background())
	for _, p := range r2.f.placesCopy() {
		if p.id != 500 && p.id != 501 {
			t.Fatalf("modified foreign order %d", p.id)
		}
	}
	// new ids start above every audited id
	p, _ := r2.tk.Prepare("BUY", 1000, "SGRX")
	_ = r2.tk.Confirm(p.Token, "SGRX")
	ps := r2.f.placesCopy()
	if last := ps[len(ps)-2].id; last <= 510 {
		t.Fatalf("new order id %d collides with the audit", last)
	}
}

func TestHeartbeatLossAndSettings(t *testing.T) {
	r := newRig(t, nil)
	if err := r.tk.SetSettings(context.Background(), Settings{Port: 7496, ClientID: 77, MaxPositionUSD: 5000}); err == nil {
		t.Fatal("client id 77 accepted")
	}
	if err := r.tk.SetSettings(context.Background(), Settings{Port: 7497, ClientID: 81, MaxPositionUSD: 2500}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.meta[MetaKey], `"port":7497`) {
		t.Fatalf("settings not persisted: %q", r.meta[MetaKey])
	}
	r.connect()
	if err := r.tk.SetSettings(context.Background(), DefaultSettings()); err == nil {
		t.Fatal("settings changed while connected")
	}
	if s := r.tk.State(); s.Settings.MaxPositionUSD != 2500 || s.ClientID != 81 {
		t.Fatalf("settings %+v", s.Settings)
	}
}

func TestStartsDisconnected(t *testing.T) {
	r := newRig(t, nil)
	r.tk.Tick(context.Background())
	if r.f.conns != 0 || r.tk.State().Conn != "disconnected" {
		t.Fatal("the ticket must not connect before the user clicks Connect")
	}
}

func TestSharesOverride(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	p, err := r.tk.PrepareQty("BUY", 999999, 500, "SGRX") // position $ ignored
	if err != nil {
		t.Fatal(err)
	}
	if p.Qty != 500 || p.Text != "BUY 500 SGRX @ 1.50 (9EMA) ~$750, TP +10% 1.65, risk $10 (stop 1.48), SL 1% under 9EMA (soft)" {
		t.Fatalf("%+v", p)
	}
	if _, err := r.tk.PrepareQty("BUY", 0, 4000, "SGRX"); err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("shares over the cap: %v", err)
	}
	if _, err := r.tk.PrepareQty("BUY", 0, -1, "SGRX"); err == nil {
		t.Fatal("negative shares accepted")
	}
	p, _ = r.tk.PrepareQty("BUY", 1000, 0, "SGRX") // empty shares: sized from $
	if p.Qty != 666 {
		t.Fatalf("qty %d", p.Qty)
	}
	if err := r.tk.Confirm(p.Token, "SGRX"); err != nil {
		t.Fatal(err)
	}
	if q := r.f.placesCopy()[0].o.TotalQuantity.Int(); q != 666 {
		t.Fatalf("sent %d", q)
	}
}
