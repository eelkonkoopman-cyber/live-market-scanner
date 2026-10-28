package main

import "testing"

func TestPaneURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		pane Pane
		sym  string
		id   string
		want string
	}{
		{"main", "http://127.0.0.1:7878", PaneMain, "", "", "http://127.0.0.1:7878/"},
		{"main trailing slash", "http://127.0.0.1:7878/", PaneMain, "", "", "http://127.0.0.1:7878/"},
		{"tape", "http://127.0.0.1:7878", PaneTape, "AAPL", "", "http://127.0.0.1:7878/?pane=tape&sym=AAPL"},
		{"tape dotted class share", "http://127.0.0.1:7878", PaneTape, "BRK.A", "", "http://127.0.0.1:7878/?pane=tape&sym=BRK.A"},
		{"tape needs escaping like encodeURIComponent", "http://127.0.0.1:7878", PaneTape, "A B&C", "", "http://127.0.0.1:7878/?pane=tape&sym=A%20B%26C"},
		{"charts", "http://127.0.0.1:7878", PaneCharts, "", "", "http://127.0.0.1:7878/?pane=charts"},
		{"scan", "http://127.0.0.1:7878", PaneScan, "", "pm-gap-fade", "http://127.0.0.1:7878/?pane=scan&id=pm-gap-fade"},
		{"scan id escaping", "http://127.0.0.1:7878", PaneScan, "", "my scan/1", "http://127.0.0.1:7878/?pane=scan&id=my%20scan%2F1"},
		{"review", "http://127.0.0.1:7878", PaneReview, "", "", "http://127.0.0.1:7878/?pane=review"},
		{"daylog", "http://127.0.0.1:7878", PaneDayLog, "", "", "http://127.0.0.1:7878/?pane=daylog"},
		{"order", "http://127.0.0.1:7878", PaneOrder, "", "", "http://127.0.0.1:7878/?pane=order"},
		{"signal no sym", "http://127.0.0.1:7878", PaneSignal, "", "", "http://127.0.0.1:7878/?pane=signal"},
		{"signal with sym", "http://127.0.0.1:7878", PaneSignal, "ATYR", "", "http://127.0.0.1:7878/?pane=signal&sym=ATYR"},
		{"signal sym needs escaping like encodeURIComponent", "http://127.0.0.1:7878", PaneSignal, "A B&C", "", "http://127.0.0.1:7878/?pane=signal&sym=A%20B%26C"},
		{"custom base", "http://localhost:9999", PaneCharts, "", "", "http://localhost:9999/?pane=charts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PaneURL(c.base, c.pane, c.sym, c.id)
			if err != nil {
				t.Fatalf("PaneURL: %v", err)
			}
			if got != c.want {
				t.Errorf("PaneURL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPaneURLErrors(t *testing.T) {
	if _, err := PaneURL("http://x", Pane("bogus"), "", ""); err == nil {
		t.Error("unknown pane: want error")
	}
	if _, err := PaneURL("http://x", PaneTape, "", ""); err == nil {
		t.Error("tape without sym: want error")
	}
	if _, err := PaneURL("http://x", PaneScan, "", ""); err == nil {
		t.Error("scan without id: want error")
	}
	if _, err := PaneURL("", PaneMain, "", ""); err == nil {
		t.Error("empty base: want error")
	}
	if _, err := PaneURL("///", PaneMain, "", ""); err == nil {
		t.Error("slashes-only base: want error")
	}
}

func TestDefaultSize(t *testing.T) {
	cases := []struct {
		pane Pane
		w, h int
	}{
		{PaneMain, 1440, 900},
		{PaneTape, 920, 820},    // mirrors ScanTabs/SymbolPanel window.open feature string
		{PaneCharts, 1600, 900}, // mirrors ScanTabs
		{PaneScan, 560, 780},    // mirrors TileFrame
		{PaneReview, 1100, 800}, // mirrors ScanTabs' ⧉ Review launcher
		{PaneDayLog, 1300, 850}, // mirrors the Day Log launcher in ScanTabs
		{PaneOrder, 320, 260},
		{PaneSignal, 1600, 900}, // mirrors ScanTabs' ⧉ Signal launcher (matches charts' size — addendum 2's grid)
	}
	for _, c := range cases {
		if w, h := DefaultSize(c.pane); w != c.w || h != c.h {
			t.Errorf("DefaultSize(%s) = %dx%d, want %dx%d", c.pane, w, h, c.w, c.h)
		}
	}
}
