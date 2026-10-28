package main

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestNoteFromEventsPicksNearbyNotesOfTheSymbol(t *testing.T) {
	const at = int64(1_000_000)
	evs := []core.FlagEvent{
		{Sym: "ABC", Ts: at - 60_000, Label: "GREEN DILDO", Note: "+9% body"},
		{Sym: "ABC", Ts: at - 10*60_000, Label: "OLD", Note: "too early"},
		{Sym: "XYZ", Ts: at, Label: "GREEN DILDO", Note: "other name"},
		{Sym: "ABC", Ts: at, Label: "FLAG", Note: ""},
		{Sym: "ABC", Ts: at + 10_000, Label: "IGNITION", Note: "skip: up 7%"},
		{Sym: "ABC", Ts: at + 120_000, Label: "LATER", Note: "too late"},
	}
	if got := noteFromEvents(evs, "ABC", at); got != "GREEN DILDO: +9% body; IGNITION: skip: up 7%" {
		t.Fatalf("got %q", got)
	}
	if got := noteFromEvents(evs, "NONE", at); got != "" {
		t.Fatalf("got %q", got)
	}
	if signalNoteFor(nil) != nil {
		t.Fatal("no signal log, no note func")
	}
}
