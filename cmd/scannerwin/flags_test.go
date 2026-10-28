package main

import (
	"io"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	o, err := ParseArgs(nil, io.Discard)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if o.Pane != PaneMain {
		t.Errorf("default pane = %q, want main", o.Pane)
	}
	if o.Base != DefaultBase {
		t.Errorf("default base = %q, want %q", o.Base, DefaultBase)
	}
}

func TestParseArgsFull(t *testing.T) {
	o, err := ParseArgs([]string{"--pane", "tape", "--sym", "TSLA", "--base", "http://localhost:8080"}, io.Discard)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if o.Pane != PaneTape || o.Sym != "TSLA" || o.Base != "http://localhost:8080" {
		t.Errorf("ParseArgs = %+v", o)
	}

	o, err = ParseArgs([]string{"--pane=scan", "--id=pm-gap-fade"}, io.Discard)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if o.Pane != PaneScan || o.ID != "pm-gap-fade" {
		t.Errorf("ParseArgs = %+v", o)
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := [][]string{
		{"--pane", "bogus"},         // unknown pane
		{"--pane", "tape"},          // tape without --sym
		{"--pane", "scan"},          // scan without --id
		{"--frobnicate"},            // unknown flag
		{"--pane", "main", "extra"}, // stray positional arg
	}
	for _, args := range cases {
		if _, err := ParseArgs(args, io.Discard); err == nil {
			t.Errorf("ParseArgs(%v): want error, got nil", args)
		}
	}
}
