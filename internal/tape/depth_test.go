package tape

import (
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Real Level 2 (internal/twslink) is single-target: only the symbol most
// recently selected across every window should ever show real depth.
func TestDepthTargetOnlyAppliesToTargetSymbol(t *testing.T) {
	e := newTest("AAA", "BBB")

	e.SetDepthTarget("AAA")
	applyCtl(e)
	e.SetDepth("AAA", []core.DepthLevel{{Price: 10, Size: 100}}, []core.DepthLevel{{Price: 10.05, Size: 200}})
	applyCtl(e)

	a := snap(e, "AAA")
	if !a.HasDepth || len(a.BookBids) != 1 || len(a.BookAsks) != 1 || a.DepthNote != "" {
		t.Fatalf("target symbol should show real depth: %+v", a)
	}

	b := snap(e, "BBB")
	if b.HasDepth || len(b.BookBids) != 0 || len(b.BookAsks) != 0 {
		t.Fatalf("non-target symbol must never show depth: %+v", b)
	}
	if !strings.Contains(b.DepthNote, "AAA") {
		t.Fatalf("non-target symbol should explain who holds it: %q", b.DepthNote)
	}
}

// SetDepth/SetDepthUnavailable for a symbol that ISN'T the current target
// must be a no-op -- guards a late update arriving just after a switch.
func TestSetDepthIgnoresNonTargetSymbol(t *testing.T) {
	e := newTest("AAA", "BBB")
	e.SetDepthTarget("AAA")
	applyCtl(e)

	e.SetDepth("BBB", []core.DepthLevel{{Price: 1, Size: 1}}, nil)
	applyCtl(e)

	b := snap(e, "BBB")
	if b.HasDepth || len(b.BookBids) != 0 {
		t.Fatalf("SetDepth for a non-target symbol must be ignored: %+v", b)
	}
}

// Switching the target away from a symbol must clear ITS depth fields, not
// just stop displaying them -- otherwise switching back to that symbol later
// (before its TWS subscription has genuinely delivered fresh data again)
// would redisplay a stale book as if it were live.
func TestSwitchingDepthTargetClearsPreviousSymbolsDepth(t *testing.T) {
	e := newTest("AAA", "BBB")

	e.SetDepthTarget("AAA")
	applyCtl(e)
	e.SetDepth("AAA", []core.DepthLevel{{Price: 10, Size: 100}}, nil)
	applyCtl(e)
	if a := snap(e, "AAA"); !a.HasDepth {
		t.Fatalf("sanity: AAA should have depth before switching away: %+v", a)
	}

	e.SetDepthTarget("BBB")
	applyCtl(e)

	// Switch back to AAA without ever re-sending fresh depth for it -- the
	// old book must NOT reappear.
	e.SetDepthTarget("AAA")
	applyCtl(e)
	a := snap(e, "AAA")
	if a.HasDepth || len(a.BookBids) != 0 {
		t.Fatalf("stale depth reappeared after switching back: %+v", a)
	}
}

func TestSetDepthUnavailablePublishesNote(t *testing.T) {
	e := newTest("AAA")
	e.SetDepthTarget("AAA")
	applyCtl(e)

	e.SetDepthUnavailable("AAA", "Market depth data has been HALTED. Please re-subscribe.")
	applyCtl(e)

	a := snap(e, "AAA")
	if a.HasDepth {
		t.Fatal("HasDepth must be false after SetDepthUnavailable")
	}
	if a.DepthNote != "Market depth data has been HALTED. Please re-subscribe." {
		t.Fatalf("DepthNote = %q", a.DepthNote)
	}
	if len(a.BookBids) != 0 || len(a.BookAsks) != 0 {
		t.Fatalf("book should be cleared: bids=%+v asks=%+v", a.BookBids, a.BookAsks)
	}
}

// Before anything has ever been selected anywhere (depthTarget == ""), no
// symbol should get the "follows X" note -- there's no X yet, and the
// frontend's own generic fallback text already covers this honestly.
func TestNoDepthTargetYetLeavesNoteEmpty(t *testing.T) {
	e := newTest("AAA", "BBB")
	for _, sym := range []string{"AAA", "BBB"} {
		if s := snap(e, sym); s.DepthNote != "" || s.HasDepth {
			t.Fatalf("%s before any selection: %+v", sym, s)
		}
	}
}
