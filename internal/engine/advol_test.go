package engine

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestPmVolXLiveDuringPreMarket(t *testing.T) {
	ts := newTS5()
	ts.adv10 = 1_000_000
	ts.sessVol = 3_500_000 // day volume during pre-market = pre-market volume so far
	r := ts.toRow(0, nil, nil, nil, core.SessionPre)
	if r.PmVolX != 3.5 || r.Adv10 != 1_000_000 {
		t.Fatalf("pmVolX want 3.5 (adv10 1M), got %v (%v)", r.PmVolX, r.Adv10)
	}
}

func TestPmVolXFrozenAfterTheOpenOnlyIfComplete(t *testing.T) {
	ts := newTS5()
	ts.adv10 = 2_000_000
	ts.sessVol = 9_000_000 // includes regular-session volume now: must NOT be used
	ts.frozenPre = frozenScope{set: true, vol: 4_000_000, complete: true}
	if r := ts.toRow(0, nil, nil, nil, core.SessionRegular); r.PmVolX != 2 {
		t.Fatalf("after 09:30 use the frozen pre-market volume: want 2 got %v", r.PmVolX)
	}
	ts.frozenPre.complete = false // the scanner only saw part of the pre-market
	if r := ts.toRow(0, nil, nil, nil, core.SessionRegular); !core.IsNull(r.PmVolX) {
		t.Fatalf("a partial pre-market volume would understate it: want null, got %v", r.PmVolX)
	}
}

func TestPmVolXNullWithoutAdv10(t *testing.T) {
	ts := newTS5()
	ts.sessVol = 5_000_000
	if r := ts.toRow(0, nil, nil, nil, core.SessionPre); !core.IsNull(r.PmVolX) || !core.IsNull(r.Adv10) {
		t.Fatalf("no 10-day average yet: want nulls, got %v %v", r.PmVolX, r.Adv10)
	}
}

func TestAdv10SurvivesTheDayReset(t *testing.T) {
	ts := newTS5()
	ts.adv10 = 123
	ts.resetSession()
	if ts.adv10 != 123 {
		t.Fatal("adv10 is replaced by the loader, not cleared at 04:00")
	}
}
