package orderticket

// wrapper is the Wrapper for one session (gen); callbacks from an older
// session are ignored by the ticket.
type wrapper struct {
	t   *Ticket
	gen int64
}

var _ Wrapper = (*wrapper)(nil)

func (w *wrapper) NextValidID(id int64)              { w.t.onNextValidID(w.gen, id) }
func (w *wrapper) ManagedAccounts(accounts []string) { w.t.onAccounts(w.gen, accounts) }
func (w *wrapper) CurrentTime(int64)                 { w.t.onBeat(w.gen) }
func (w *wrapper) ConnectionClosed()                 { w.t.onClosed(w.gen) }
func (w *wrapper) Error(reqID, errTime, code int64, msg, adv string) {
	w.t.onError(w.gen, reqID, code, msg)
}
func (w *wrapper) DisplayGroupUpdated(reqID int64, info string) {
	w.t.onDisplayGroup(w.gen, reqID, info)
}
func (w *wrapper) ContractDetails(reqID int64, cd *ContractDetails) {
	w.t.onContractDetails(w.gen, reqID, cd)
}
func (w *wrapper) ContractDetailsEnd(int64) {}
func (w *wrapper) OpenOrder(id int64, c *Contract, o *Order, st *OrderState) {
	w.t.onOpenOrder(w.gen, id, c, o, st)
}
func (w *wrapper) OpenOrderEnd() {}
func (w *wrapper) AccountSummary(reqID int64, account, tag, value, currency string) {
	w.t.onAccountSummary(w.gen, reqID, account, tag, value, currency)
}
func (w *wrapper) AccountSummaryEnd(int64)         {}
func (w *wrapper) TickPrice(int64, int64, float64) {}
func (w *wrapper) TickSize(reqID, tickType int64, size float64) {
	w.t.onTick(w.gen, reqID, tickType, size)
}
func (w *wrapper) TickGeneric(reqID, tickType int64, value float64) {
	w.t.onTick(w.gen, reqID, tickType, value)
}
func (w *wrapper) TickString(int64, int64, string)             {}
func (w *wrapper) TickReqParams(int64, float64, string, int64) {}
func (w *wrapper) OrderStatus(id int64, status string, filled, remaining Qty, avg float64,
	permID, parentID int64, lastFill float64, clientID int64, whyHeld string, mktCap float64) {
	w.t.onOrderStatus(w.gen, statusMsg{ID: id, Status: status, Filled: filled.Int(), Remaining: remaining.Int(),
		Avg: avg, PermID: permID, ParentID: parentID, LastFill: lastFill, ClientID: clientID, WhyHeld: whyHeld})
}
