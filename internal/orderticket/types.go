package orderticket

// The ticket's own TWS types: only the fields it sends or reads. The raw
// client (twsclient.go) encodes/decodes them; tests use them directly.

// Qty is an IB Decimal quantity (whole shares for this ticket).
type Qty float64

// Int truncates to whole shares.
func (q Qty) Int() int64 { return int64(q) }

// Contract is the subset of IB's Contract the ticket uses.
type Contract struct {
	ConID    int64
	Symbol   string
	SecType  string
	Exchange string
	Currency string
}

// ContractDetails carries the resolved contract (contractData).
type ContractDetails struct {
	Contract Contract
}

// Order is the subset of IB's Order the ticket sends (placeOrder) or reads
// back (openOrder). Every other placeOrder field goes out at IB's default.
type Order struct {
	Action        string
	OrderType     string
	TotalQuantity Qty
	LmtPrice      float64
	TIF           string
	OcaGroup      string
	Account       string
	OrderRef      string
	Transmit      bool
	ParentID      int64
	OutsideRTH    bool
	// WhatIf asks TWS for a margin/commission preview instead of an order.
	// Set ONLY by the ticket's what-if check; real orders never carry it.
	WhatIf bool
	// read back from openOrder only
	ClientID int64
	PermID   int64
}

// OrderState: openOrder's trailing state (decodeOrderTail). Margin fields are
// IB's raw strings (as ibapi keeps them); unset doubles decode to 0. Parsed is
// false when the tail was not (or could not be) decoded.
type OrderState struct {
	Status               string
	WhatIf               bool
	InitMarginBefore     string
	MaintMarginBefore    string
	EquityWithLoanBefore string
	InitMarginChange     string
	MaintMarginChange    string
	EquityWithLoanChange string
	InitMarginAfter      string
	MaintMarginAfter     string
	EquityWithLoanAfter  string
	Commission           float64
	MinCommission        float64
	MaxCommission        float64
	CommissionCurrency   string
	WarningText          string
	Parsed               bool
}

// Wrapper receives decoded TWS callbacks (one per session; see wrapper.go).
type Wrapper interface {
	NextValidID(id int64)
	ManagedAccounts(accounts []string)
	CurrentTime(t int64)
	ConnectionClosed()
	Error(reqID, errTime, code int64, msg, adv string)
	DisplayGroupUpdated(reqID int64, info string)
	ContractDetails(reqID int64, cd *ContractDetails)
	ContractDetailsEnd(reqID int64)
	OpenOrder(id int64, c *Contract, o *Order, st *OrderState)
	OpenOrderEnd()
	AccountSummary(reqID int64, account, tag, value, currency string)
	AccountSummaryEnd(reqID int64)
	// market data (mktdata.go)
	TickPrice(reqID, tickType int64, price float64)
	TickSize(reqID, tickType int64, size float64)
	TickGeneric(reqID, tickType int64, value float64)
	TickString(reqID, tickType int64, value string)
	TickReqParams(reqID int64, minTick float64, bboExchange string, snapshotPermissions int64)
	OrderStatus(id int64, status string, filled, remaining Qty, avg float64,
		permID, parentID int64, lastFill float64, clientID int64, whyHeld string, mktCap float64)
}
