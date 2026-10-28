package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestRollupCompareAndReport(t *testing.T) {
	c := newCollector(nil)
	// Massive-style seconds in minute 0 and minute 1 (minute 1 = newest, skipped).
	for i, px := range []float64{10, 10.5, 9.8} {
		ms := int64(i+1) * 1000
		c.agg("massive", core.Agg{Sym: "AAA", Open: px, High: px, Low: px, Close: px, Vol: 100, EndMs: ms})
	}
	c.agg("massive", core.Agg{Sym: "AAA", Open: 11, High: 11, Low: 11, Close: 11, Vol: 5, EndMs: 61_000})
	c.agg("massive", core.Agg{Sym: "ONLYM", Open: 1, High: 1, Low: 1, Close: 1, Vol: 5, EndMs: 1000})
	c.bar("alpaca bars", "AAA", core.Bar{T: 0, O: 10, H: 10.5, L: 9.8, C: 9.8, V: 330}, false)
	c.bar("alpaca bars", "AAA", core.Bar{T: 60_000, O: 11, H: 11, L: 11, C: 11, V: 5}, false)

	got := c.minutes["massive"][minKey{"AAA", 0}]
	if got.O != 10 || got.H != 10.5 || got.L != 9.8 || got.C != 9.8 || got.V != 300 {
		t.Fatalf("rollup %+v", got)
	}
	diffs, onlyA, onlyB := compare(c.minutes["massive"], c.minutes["alpaca bars"])
	if len(diffs) != 1 || diffs[0].volA != 300 || diffs[0].volB != 330 || diffs[0].mins != 1 || diffs[0].hlMiss != 0 {
		t.Fatalf("diffs %+v", diffs)
	}
	if len(onlyA) != 1 || onlyA[0] != "ONLYM" || len(onlyB) != 0 {
		t.Fatalf("coverage %v %v", onlyA, onlyB)
	}
	c.latency("massive agg", 120)
	var buf bytes.Buffer
	c.report(&buf, 10)
	for _, want := range []string{"massive vs alpaca bars", "+10.00", "p50=120"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, buf.String())
		}
	}
}
