package feed

import (
	"encoding/json"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type envelope struct {
	Ev string `json:"ev"`
}

type statusMsg struct {
	Ev      string `json:"ev"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// aggMsg mirrors the Massive/Polygon "A" per-second aggregate (full schema).
type aggMsg struct {
	Ev  string  `json:"ev"`
	Sym string  `json:"sym"`
	V   int64   `json:"v"`  // tick volume in window
	AV  int64   `json:"av"` // accumulated daily volume
	OP  float64 `json:"op"` // official day open
	VW  float64 `json:"vw"` // window VWAP
	O   float64 `json:"o"`
	C   float64 `json:"c"`
	H   float64 `json:"h"`
	L   float64 `json:"l"`
	A   float64 `json:"a"` // day VWAP
	Z   int64   `json:"z"` // avg trade size
	S   int64   `json:"s"` // window start ms
	E   int64   `json:"e"` // window end ms
	OTC bool    `json:"otc"`
}

func (a aggMsg) toAgg() core.Agg {
	return core.Agg{
		Sym: a.Sym, Open: a.O, High: a.H, Low: a.L, Close: a.C, VWAP: a.VW,
		Vol: a.V, AccVol: a.AV, DayOpen: a.OP, DayVWAP: a.A,
		StartMs: a.S, EndMs: a.E, OTC: a.OTC,
	}
}

// tradeMsg mirrors the Massive/Polygon "T" trade event (time & sales).
type tradeMsg struct {
	Sym   string  `json:"sym"`
	Price float64 `json:"p"`
	Size  int64   `json:"s"`
	TS    int64   `json:"t"` // SIP timestamp, epoch ms
	Conds []int32 `json:"c"`
}

// quoteMsg mirrors the Massive/Polygon "Q" NBBO quote event. Note bs/as are
// ROUND LOTS (1 = 100 shares), unlike a trade's `s` which is already shares —
// toQuote normalizes both to shares so callers never mix units.
// Note also that `c`/`i` have inverted types vs the trade event (here c is a
// single int and i is an array), which is why these stay separate structs.
type quoteMsg struct {
	Sym     string  `json:"sym"`
	Bid     float64 `json:"bp"`
	BidLots int64   `json:"bs"`
	Ask     float64 `json:"ap"`
	AskLots int64   `json:"as"`
	TS      int64   `json:"t"` // SIP timestamp, epoch ms
}

const roundLot = 100

// decodeFrame parses one WS frame. Frames are JSON arrays of mixed events; a
// bare object (some error responses) is tolerated by wrapping it. Trade/quote
// events only arrive while a tape subscription is active.
func decodeFrame(data []byte) ([]core.Agg, []statusMsg, []core.TapeEvent) {
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) != nil {
		arr = []json.RawMessage{data}
	}
	var aggs []core.Agg
	var sts []statusMsg
	var tape []core.TapeEvent
	for _, raw := range arr {
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		switch env.Ev {
		case "A":
			var a aggMsg
			if json.Unmarshal(raw, &a) == nil {
				aggs = append(aggs, a.toAgg())
			}
		case "T":
			var t tradeMsg
			if json.Unmarshal(raw, &t) == nil {
				tape = append(tape, core.TapeEvent{Trade: &core.Trade{
					Sym: t.Sym, Price: t.Price, Size: t.Size, TS: t.TS, Conds: t.Conds,
				}})
			}
		case "Q":
			var q quoteMsg
			if json.Unmarshal(raw, &q) == nil {
				tape = append(tape, core.TapeEvent{Quote: &core.Quote{
					Sym: q.Sym, Bid: q.Bid, Ask: q.Ask, TS: q.TS,
					BidSz: q.BidLots * roundLot, AskSz: q.AskLots * roundLot,
				}})
			}
		case "status":
			var s statusMsg
			if json.Unmarshal(raw, &s) == nil {
				sts = append(sts, s)
			}
		}
	}
	return aggs, sts, tape
}
