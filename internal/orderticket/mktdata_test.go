package orderticket

import (
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func mdCalls(r *rig) ([]mdCall, []int64) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return append([]mdCall(nil), r.f.mdReqs...), append([]int64(nil), r.f.mdCancels...)
}

func TestMktDataOneSubscriptionSwitchAndCancel(t *testing.T) {
	r := newRig(t, nil)
	r.connect()
	if reqs, _ := mdCalls(r); len(reqs) != 0 {
		t.Fatalf("subscribed without a symbol %v", reqs)
	}
	r.seedBars("SGRX", 30, 1.5)
	r.show("111")
	reqs, _ := mdCalls(r)
	if len(reqs) != 1 || reqs[0].sym != "SGRX" || reqs[0].generic != "236" || reqs[0].id < mdReqBase {
		t.Fatalf("%+v", reqs)
	}
	first := reqs[0].id
	r.show("111") // same symbol: nothing new
	r.show("222") // switch: cancel old, then request new
	reqs, cans := mdCalls(r)
	if len(reqs) != 2 || reqs[1].sym != "ABCD" || len(cans) != 1 || cans[0] != first || reqs[1].id == first {
		t.Fatalf("switch: reqs %+v cancels %v", reqs, cans)
	}
	r.tk.Disconnect()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, cans = mdCalls(r); len(cans) == 2 && cans[1] == reqs[1].id {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not cancelled on disconnect: %v", cans)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(r.f.placesCopy()) != 0 {
		t.Fatal("order traffic from market data")
	}
}

func TestLiveShortableTicksAndFallback(t *testing.T) {
	r := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 12.5}, 12*time.Minute)
	reqs, _ := mdCalls(r)
	id := reqs[len(reqs)-1].id
	r.f.w.TickSize(id, tickShortableShares, 2300)
	r.f.w.TickGeneric(id, tickShortable, 3)
	si := r.tk.State().Short
	if !si.Live || si.Source != "TWS live" || si.Avail != 2300 || si.Fee != 12.5 || si.LiveIndicator != 3 || si.FileAvail != 150000 {
		t.Fatalf("%+v", si)
	}
	// confirm note uses the live count; fewer than wanted -> amber, not blocked
	p, err := r.tk.Prepare("SELL", 1000, "SGRX") // 666 sh vs 2300 live: fine
	if err != nil || !strings.Contains(p.Text, "shortable 2.3K (fee 12.5%)") {
		t.Fatalf("%q %v", p.Text, err)
	}
	r.f.w.TickSize(id, tickShortableShares, 100)
	if p, _ = r.tk.Prepare("SELL", 1000, "SGRX"); !strings.Contains(p.Warn, "only 100 shares available to short") {
		t.Fatalf("%q", p.Warn)
	}
	// a tick for another (old) request id is ignored
	r.f.w.TickSize(id+999, tickShortableShares, 5)
	if r.tk.State().Short.Avail != 100 {
		t.Fatal("foreign tick applied")
	}
	// no tick for > 60 s: back to the file
	r.advance(61 * time.Second)
	si = r.tk.State().Short
	if si.Live || si.Source == "TWS live" || si.Avail != 150000 {
		t.Fatalf("no fallback %+v", si)
	}
}

func TestLiveShortableIndicatorOnlyAndErrors(t *testing.T) {
	r := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 1}, 12*time.Minute)
	reqs, _ := mdCalls(r)
	id := reqs[len(reqs)-1].id
	r.f.w.TickGeneric(id, tickShortable, 1.0) // not available
	if si := r.tk.State().Short; !si.Live || si.Shortable {
		t.Fatalf("%+v", si)
	}
	// market-data error: tile only, never the banner
	r2 := shortRig(t, &core.ShortRef{Ticker: "SGRX", Available: 150000, FeeRate: 1}, 12*time.Minute)
	reqs, _ = mdCalls(r2)
	id2 := reqs[len(reqs)-1].id
	r2.f.w.Error(id2, 0, 10089, "Requested market data requires additional subscription for API", "")
	s := r2.tk.State()
	if strings.Contains(s.Note, "10089") {
		t.Fatalf("market-data error on the banner: %q", s.Note)
	}
	if si := s.Short; si.Live || si.Avail != 150000 || !strings.Contains(si.LiveErr, "IB 10089") || !strings.Contains(si.Note, "live: IB 10089") {
		t.Fatalf("%+v", si)
	}
	// a late error for an old subscription is dropped too
	r2.f.w.Error(id2-1, 0, 354, "not subscribed", "")
	if strings.Contains(r2.tk.State().Note, "354") {
		t.Fatal("old subscription error on the banner")
	}
}
