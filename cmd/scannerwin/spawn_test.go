package main

import (
	"io"
	"reflect"
	"testing"
)

func TestSpawnArgs(t *testing.T) {
	tests := []struct {
		name          string
		pane          Pane
		sym, id, base string
		want          []string
	}{
		{
			name: "main default base",
			pane: PaneMain, base: DefaultBase,
			want: []string{"--pane", "main", "--base", DefaultBase},
		},
		{
			name: "tape with sym",
			pane: PaneTape, sym: "AAPL", base: DefaultBase,
			want: []string{"--pane", "tape", "--sym", "AAPL", "--base", DefaultBase},
		},
		{
			name: "charts custom base",
			pane: PaneCharts, base: "http://127.0.0.1:9999",
			want: []string{"--pane", "charts", "--base", "http://127.0.0.1:9999"},
		},
		{
			name: "scan with id",
			pane: PaneScan, id: "pm-gap-fade", base: DefaultBase,
			want: []string{"--pane", "scan", "--id", "pm-gap-fade", "--base", DefaultBase},
		},
		{
			name: "review default base",
			pane: PaneReview, base: DefaultBase,
			want: []string{"--pane", "review", "--base", DefaultBase},
		},
		{
			name: "signal without sym",
			pane: PaneSignal, base: DefaultBase,
			want: []string{"--pane", "signal", "--base", DefaultBase},
		},
		{
			name: "signal with sym",
			pane: PaneSignal, sym: "ATYR", base: DefaultBase,
			want: []string{"--pane", "signal", "--sym", "ATYR", "--base", DefaultBase},
		},
		{
			name: "empty base omitted",
			pane: PaneCharts,
			want: []string{"--pane", "charts"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SpawnArgs(tt.pane, tt.sym, tt.id, tt.base)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SpawnArgs(%q,%q,%q,%q) = %q, want %q",
					tt.pane, tt.sym, tt.id, tt.base, got, tt.want)
			}
		})
	}
}

// TestSpawnArgsRoundTrip is the property the spawner actually relies on: the
// argv built for a child must parse back (via the child's own ParseArgs) into
// exactly the options the parent intended.
func TestSpawnArgsRoundTrip(t *testing.T) {
	cases := []Options{
		{Pane: PaneMain, Base: DefaultBase},
		{Pane: PaneTape, Sym: "BRK.A", Base: DefaultBase},
		{Pane: PaneTape, Sym: "weird sym", Base: "http://127.0.0.1:8080"},
		{Pane: PaneCharts, Base: DefaultBase},
		{Pane: PaneScan, ID: "pm-gap-fade", Base: DefaultBase},
		{Pane: PaneReview, Base: DefaultBase},
		{Pane: PaneDayLog, Base: DefaultBase},
		{Pane: PaneSignal, Base: DefaultBase},
		{Pane: PaneSignal, Sym: "ATYR", Base: DefaultBase},
	}
	for _, want := range cases {
		args := SpawnArgs(want.Pane, want.Sym, want.ID, want.Base)
		got, err := ParseArgs(args, io.Discard)
		if err != nil {
			t.Fatalf("ParseArgs(SpawnArgs(%+v)) error: %v", want, err)
		}
		if got != want {
			t.Fatalf("round trip: got %+v, want %+v (args %q)", got, want, args)
		}
	}
}

func TestParsePaneURL(t *testing.T) {
	const base = DefaultBase
	tests := []struct {
		name     string
		raw      string
		wantPane Pane
		wantSym  string
		wantID   string
		wantOK   bool
	}{
		// The four shapes web/src actually emits (relative, root path).
		{name: "tape relative", raw: "/?pane=tape&sym=AAPL", wantPane: PaneTape, wantSym: "AAPL", wantOK: true},
		{name: "scan relative", raw: "/?pane=scan&id=pm-gap-fade", wantPane: PaneScan, wantID: "pm-gap-fade", wantOK: true},
		{name: "charts relative", raw: "/?pane=charts", wantPane: PaneCharts, wantOK: true},
		{name: "review relative", raw: "/?pane=review", wantPane: PaneReview, wantOK: true},
		{name: "daylog relative", raw: "/?pane=daylog", wantPane: PaneDayLog, wantOK: true},
		{name: "order relative", raw: "/?pane=order", wantPane: PaneOrder, wantOK: true},
		{name: "main relative", raw: "/?pane=main", wantPane: PaneMain, wantOK: true},
		// signal: sym is OPTIONAL (the window follows the global selection when absent).
		{name: "signal relative no sym", raw: "/?pane=signal", wantPane: PaneSignal, wantOK: true},
		{name: "signal relative with sym", raw: "/?pane=signal&sym=ATYR", wantPane: PaneSignal, wantSym: "ATYR", wantOK: true},
		// encodeURIComponent output percent-decodes.
		{name: "encoded sym", raw: "/?pane=tape&sym=BF%20B", wantPane: PaneTape, wantSym: "BF B", wantOK: true},
		{name: "encoded dot sym", raw: "/?pane=tape&sym=BRK.A", wantPane: PaneTape, wantSym: "BRK.A", wantOK: true},
		// No leading slash / no path at all.
		{name: "query only", raw: "?pane=charts", wantPane: PaneCharts, wantOK: true},
		// Absolute forms pointing at our own origin.
		{name: "absolute same origin", raw: "http://127.0.0.1:7878/?pane=charts", wantPane: PaneCharts, wantOK: true},
		{name: "absolute same origin no slash", raw: "http://127.0.0.1:7878?pane=tape&sym=TSLA", wantPane: PaneTape, wantSym: "TSLA", wantOK: true},
		{name: "protocol relative same host", raw: "//127.0.0.1:7878/?pane=charts", wantPane: PaneCharts, wantOK: true},
		// External / foreign — must fall through.
		{name: "external https", raw: "https://example.com/", wantOK: false},
		{name: "external with pane params", raw: "https://example.com/?pane=tape&sym=A", wantOK: false},
		{name: "wrong port", raw: "http://127.0.0.1:7979/?pane=charts", wantOK: false},
		{name: "protocol relative foreign host", raw: "//evil.example/?pane=charts", wantOK: false},
		{name: "mailto", raw: "mailto:x@example.com", wantOK: false},
		{name: "javascript scheme", raw: "javascript:alert(1)", wantOK: false},
		// Same origin but not a pane URL.
		{name: "root no pane", raw: "/", wantOK: false},
		{name: "other path", raw: "/docs?pane=charts", wantOK: false},
		{name: "unknown pane", raw: "/?pane=bogus", wantOK: false},
		{name: "tape missing sym", raw: "/?pane=tape", wantOK: false},
		{name: "scan missing id", raw: "/?pane=scan", wantOK: false},
		// Garbage.
		{name: "empty", raw: "", wantOK: false},
		{name: "no scheme colon", raw: "://bad", wantOK: false},
		{name: "bad percent escape", raw: "/?pane=tape&sym=%zz", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, sym, id, ok := ParsePaneURL(tt.raw, base)
			if ok != tt.wantOK {
				t.Fatalf("ParsePaneURL(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if pane != tt.wantPane || sym != tt.wantSym || id != tt.wantID {
				t.Fatalf("ParsePaneURL(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tt.raw, pane, sym, id, tt.wantPane, tt.wantSym, tt.wantID)
			}
		})
	}
}

// TestParsePaneURLBaseVariants: trailing slash on base is tolerated; a
// hopeless base rejects absolute URLs rather than matching them.
func TestParsePaneURLBaseVariants(t *testing.T) {
	if _, _, _, ok := ParsePaneURL("http://127.0.0.1:7878/?pane=charts", "http://127.0.0.1:7878/"); !ok {
		t.Fatal("trailing-slash base should still match its own origin")
	}
	if _, _, _, ok := ParsePaneURL("http://127.0.0.1:7878/?pane=charts", "not a url"); ok {
		t.Fatal("unparseable base must not match absolute URLs")
	}
	// Relative pane URLs are origin-independent and must work regardless.
	if _, _, _, ok := ParsePaneURL("/?pane=charts", "not a url"); !ok {
		t.Fatal("relative pane URL should not consult base")
	}
}
