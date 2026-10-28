package core

import (
	"math"
	"testing"
)

func TestPowerSetupPlan(t *testing.T) {
	e, s, tg := PowerSetupPlan(2.00)
	if e != 2.00 || math.Abs(s-1.88) > 1e-9 || math.Abs(tg-2.36) > 1e-9 { // R = 0.12, target = entry + 3R
		t.Fatal(e, s, tg)
	}
}

func TestPowerFilterTag(t *testing.T) {
	PsMinGapPct = 10 // the gap filter enabled
	defer func() { PsMinGapPct = 0 }()
	cases := []struct {
		fo, sl, gp float64
		want       string
	}{
		{15, 2.8, 20, "A✓ slope✓ gap✓"},
		{23.3, 3, 20, "A✗ slope✓ gap✓"},
		{20.99, 2.1, 10, "A✓ slope✓ gap✓"}, // 21 is exclusive, 2.1 and 10 inclusive
		{21, 2.09, 9.99, "A✗ slope✗ gap✗"},
		{15, Null, 12, "A✓ slope? gap✓"},
	}
	for _, c := range cases {
		if got := PowerFilterTag(c.fo, c.sl, c.gp); got != c.want {
			t.Errorf("%v: %q want %q", c, got, c.want)
		}
	}
	if PsPassAll(15, Null, 12) || !PsPassAll(15, 3, 12) {
		t.Fatal("PsPassAll")
	}
}

// The gap filter is OFF by default: no gap in the tag, any gap (even unknown) passes.
func TestPowerFilterGapOffByDefault(t *testing.T) {
	if PsMinGapPct != 0 || !PsPassAll(15, 3, Null) || PsPassAll(21, 3, 50) {
		t.Fatal("defaults")
	}
	if got := PowerFilterTag(15, 1.9, 2); got != "A✓ slope✗" {
		t.Fatal(got)
	}
}

func TestQuietWindows(t *testing.T) {
	cases := map[int]string{239: "", 4 * 60: "", 5 * 60: "05–07 ET window", 6*60 + 59: "05–07 ET window", 7 * 60: "", 15*60 + 59: "", 16 * 60: "16–20 ET window", 19*60 + 59: "16–20 ET window", 20 * 60: ""}
	for m, want := range cases {
		if got := PsQuietName(m); got != want {
			t.Errorf("%d: %q want %q", m, got, want)
		}
	}
	w, ok := ParseQuietWindows("05:00-07:00, 16:00-20:00")
	if !ok || len(w) != 2 || w[1].StartMin != 960 {
		t.Fatal(w, ok)
	}
	if w, ok := ParseQuietWindows("none"); !ok || w != nil {
		t.Fatal("none")
	}
	if _, ok := ParseQuietWindows("7:00-5:00"); ok {
		t.Fatal("reversed accepted")
	}
}
