package feed

import "testing"

func TestDecodeFrameMixed(t *testing.T) {
	frame := []byte(`[
	  {"ev":"status","status":"success","message":"subscribed to: A.*"},
	  {"ev":"A","sym":"AAPL","v":100,"av":2000,"op":150.0,"vw":150.5,"o":150.1,"c":150.7,"h":151.0,"l":149.9,"a":150.4,"z":50,"s":1700000000000,"e":1700000001000,"otc":false},
	  {"ev":"A","sym":"MSFT","v":40,"av":900,"op":400,"vw":401,"o":400.5,"c":402,"h":402.5,"l":399.5,"a":401.2,"z":10,"s":1700000000000,"e":1700000001000}
	]`)
	aggs, sts, _ := decodeFrame(frame)
	if len(aggs) != 2 {
		t.Fatalf("want 2 aggs got %d", len(aggs))
	}
	if len(sts) != 1 || sts[0].Status != "success" {
		t.Fatalf("status parse: %+v", sts)
	}
	a := aggs[0]
	if a.Sym != "AAPL" || a.Close != 150.7 || a.High != 151.0 || a.Low != 149.9 ||
		a.Vol != 100 || a.AccVol != 2000 || a.DayOpen != 150.0 || a.DayVWAP != 150.4 ||
		a.EndMs != 1700000001000 {
		t.Fatalf("AAPL agg mismatch: %+v", a)
	}
}

func TestDecodeFrameBareObject(t *testing.T) {
	aggs, sts, _ := decodeFrame([]byte(`{"ev":"status","status":"auth_success"}`))
	if len(aggs) != 0 || len(sts) != 1 || sts[0].Status != "auth_success" {
		t.Fatalf("bare object: aggs=%v sts=%+v", aggs, sts)
	}
}

func TestDecodeFrameTradesAndQuotes(t *testing.T) {
	frame := []byte(`[
	  {"ev":"Q","sym":"ABCD","bx":4,"bp":4.50,"bs":12,"ax":7,"ap":4.52,"as":30,"t":1700000000000,"q":50000},
	  {"ev":"T","sym":"ABCD","x":4,"i":"77","z":3,"p":4.52,"s":1500,"c":[0,12],"t":1700000000100,"q":3663},
	  {"ev":"A","sym":"ABCD","v":1500,"c":4.52,"s":1700000000000,"e":1700000001000}
	]`)
	aggs, _, tape := decodeFrame(frame)
	if len(aggs) != 1 {
		t.Fatalf("aggregates must still decode alongside tape events: %d", len(aggs))
	}
	if len(tape) != 2 {
		t.Fatalf("want 2 tape events got %d", len(tape))
	}
	q := tape[0].Quote
	if q == nil || q.Sym != "ABCD" || q.Bid != 4.50 || q.Ask != 4.52 || q.TS != 1700000000000 {
		t.Fatalf("quote mismatch: %+v", q)
	}
	// bs/as arrive as ROUND LOTS on the wire; they must reach core as shares so
	// they're comparable with a trade's size.
	if q.BidSz != 1200 || q.AskSz != 3000 {
		t.Fatalf("quote sizes must be normalized to shares, got bid %d ask %d", q.BidSz, q.AskSz)
	}
	tr := tape[1].Trade
	if tr == nil || tr.Sym != "ABCD" || tr.Price != 4.52 || tr.Size != 1500 || tr.TS != 1700000000100 {
		t.Fatalf("trade mismatch: %+v", tr)
	}
	if len(tr.Conds) != 2 || tr.Conds[1] != 12 {
		t.Fatalf("conditions lost: %+v", tr.Conds)
	}
}
