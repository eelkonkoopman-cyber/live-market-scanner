package twslink

import (
	"strconv"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// watchDepth moves the one market-depth subscription to conID and requests
// real Level 2 (reqMktDepth, numRows=10, isSmartDepth=true — matching the
// original standalone Python tape reader's own IBKR call). Exactly one line
// is ever held: the previous subscription is cancelled first, so this can
// never grow into the account's market-data line budget no matter how fast
// symbols are switched — draws from the same scarce pool as watchShort.
func (c *Client) watchDepth(conID int64) {
	if !c.cfg.LiveDepth {
		return
	}
	if c.depthOpen {
		if err := c.send(outCancelMktDepth, 1, depthReqID, 1); err != nil {
			logx.L().Debug("TWS cancelMktDepth failed", "err", err)
		}
		c.depthOpen = false
	}
	c.bookBids = nil
	c.bookAsks = nil
	// Contract fields are positional; conId alone identifies it, so the rest
	// stay empty apart from the routing/type hints (same shape as watchShort).
	err := c.send(
		outReqMktDepth, 5, depthReqID,
		conID,
		"",    // symbol (conId wins)
		"STK", // secType
		"",    // lastTradeDateOrContractMonth
		"",    // strike (unset)
		"",    // right
		"",    // multiplier
		c.cfg.Exchange,
		"",    // primaryExchange
		"USD", // currency
		"",    // localSymbol
		"",    // tradingClass
		depthNumRows,
		1,  // isSmartDepth
		"", // mktDataOptions
	)
	if err != nil {
		logx.L().Debug("TWS reqMktDepth failed", "err", err)
		return
	}
	c.depthOpen = true
}

// onDepth applies one incremental book update and publishes the resulting
// snapshot. MARKET_DEPTH (non-L2, single-market-maker venues like ARCA) and
// MARKET_DEPTH_L2 (multi-market-maker venues like NASDAQ) carry the same
// operation/side/price/size tuple, but MARKET_DEPTH_L2 has an extra
// marketMaker field between position and operation — the two need distinct
// field offsets, not a shared parser.
//
//	non-L2: [msgId, version, reqId, position, operation, side, price, size]
//	L2:     [msgId, version, reqId, position, marketMaker, operation, side, price, size, isSmartDepth?]
func (c *Client) onDepth(f []string, isL2 bool) {
	want, opIdx := 8, 4
	if isL2 {
		want, opIdx = 9, 5
	}
	if len(f) < want || f[2] != strconv.Itoa(depthReqID) {
		return // malformed, or not our subscription
	}
	position, err := strconv.Atoi(f[3])
	if err != nil || position < 0 {
		return
	}
	operation, err := strconv.Atoi(f[opIdx])
	if err != nil {
		return
	}
	side, err := strconv.Atoi(f[opIdx+1])
	if err != nil {
		return
	}
	price, err := strconv.ParseFloat(f[opIdx+2], 64)
	if err != nil {
		return
	}
	size, err := strconv.ParseFloat(f[opIdx+3], 64)
	if err != nil {
		return
	}

	sidePtr := &c.bookAsks // side 0 = ask
	if side == 1 {
		sidePtr = &c.bookBids // side 1 = bid
	}
	*sidePtr = applyDepthOp(*sidePtr, position, operation, core.DepthLevel{Price: price, Size: size})

	c.publishDepth()
}

// applyDepthOp mutates one book side (already sorted best-first, one slot per
// TWS "position") per TWS's documented operation semantics: 0 = insert
// (shift everything at/after position down), 1 = update (in place), 2 =
// delete (remove and shift up). An out-of-range position is ignored rather
// than panicking — a stale update from a subscription that's mid-switch must
// never crash the link.
func applyDepthOp(levels []core.DepthLevel, position, operation int, lvl core.DepthLevel) []core.DepthLevel {
	switch operation {
	case 0: // insert
		if position > len(levels) {
			return levels
		}
		levels = append(levels, core.DepthLevel{})
		copy(levels[position+1:], levels[position:])
		levels[position] = lvl
	case 1: // update
		if position >= len(levels) {
			return levels
		}
		levels[position] = lvl
	case 2: // delete
		if position >= len(levels) {
			return levels
		}
		levels = append(levels[:position], levels[position+1:]...)
	}
	return levels
}

// publishDepth sends the current two-sided book snapshot, non-blocking.
func (c *Client) publishDepth() {
	select {
	case c.depthOut <- core.DepthUpdate{
		Sym:  c.want,
		Bids: append([]core.DepthLevel(nil), c.bookBids...),
		Asks: append([]core.DepthLevel(nil), c.bookAsks...),
	}:
	default: // UI behind; the next update supersedes
	}
}

// onDepthError reports why the depth subscription failed or closed (TWS's
// own "halted, please re-subscribe" / "reset" / cap-reached messages) and
// marks the line closed, so a later switch or reconnect starts clean instead
// of trying to cancel a subscription TWS has already dropped.
func (c *Client) onDepthError(msg string) {
	c.depthOpen = false
	c.bookBids = nil
	c.bookAsks = nil
	select {
	case c.depthOut <- core.DepthUpdate{Sym: c.want, Note: msg}:
	default:
	}
}
