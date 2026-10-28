package dildopb

import (
	"sync"
	"testing"
)

func TestParseDefaultsAndValidation(t *testing.T) {
	if p, ok := Parse(nil); !ok || p != DefaultParams() {
		t.Fatalf("absent -> defaults, got %+v", p)
	}
	if p, ok := Parse([]byte("{nope")); ok || p != DefaultParams() {
		t.Fatalf("malformed -> defaults,false, got %+v %v", p, ok)
	}
	p, _ := Parse([]byte(`{"minBodyPct":12.5,"priorBars":6,"floor":"open","enabled":false,"bogus":1,
		"minVolX":-1,"pbWindowMin":0,"emaPeriod":9.5,"maxTopPct":150,"minPrice":"x","touchPct":2}`))
	want := DefaultParams()
	want.MinBodyPct, want.PriorBars, want.Floor, want.Enabled, want.TouchPct = 12.5, 6, FloorOpen, false, 2
	if p != want {
		t.Fatalf("got %+v want %+v", p, want)
	}
	if q, _ := Parse(Marshal(want)); q != want {
		t.Fatalf("round trip %+v != %+v", q, want)
	}
	if q, _ := Parse([]byte(`{"floor":"x"}`)); q.Floor != FloorMid {
		t.Fatal("unknown floor must default")
	}
}

func TestLiveSwapRace(t *testing.T) {
	t.Cleanup(ResetLive)
	if Live() != DefaultParams() {
		t.Fatal("unset live must be the defaults")
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if g%2 == 0 {
					p := DefaultParams()
					p.MinBodyPct = float64(i)
					SetLive(p)
				} else if p := Live(); p.EmaPeriod != 9 {
					t.Error("torn read")
				}
			}
		}(g)
	}
	wg.Wait()
}
