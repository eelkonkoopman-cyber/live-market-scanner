package main

import (
	"net/url"
	"strings"
)

// SpawnArgs builds the argv (without the program name) for a child
// scannerwin.exe that opens the given pane. One PROCESS per window — not one
// window per process-instance — is deliberate: WebView2 hosting, the Win32
// message loop, and the injected JS all live inside a single process, so a
// crash (or a wedged WebView) in the tape window can never take down the main
// scanner window or the charts monitor. The cost (one WebView2 browser-process
// tree per window) is acceptable on a trading desktop; Chromium shares the
// browser process across same-user-data-dir instances anyway.
//
// base is always passed explicitly so children talk to the same scanner
// origin as their parent even when it is non-default. No dedupe across
// processes in v1: two charts windows is legal, matching browser behavior.
//
// The output round-trips through ParseArgs (property-tested in spawn_test.go).
func SpawnArgs(pane Pane, sym, id, base string) []string {
	args := []string{"--pane", string(pane)}
	if sym != "" {
		args = append(args, "--sym", sym)
	}
	if id != "" {
		args = append(args, "--id", id)
	}
	if base != "" {
		args = append(args, "--base", base)
	}
	return args
}

// ParsePaneURL classifies a window.open URL from the web app. It is the Go
// half of the injected window.open override (nativeShellJS): the JS side
// forwards the raw first argument of every window.open call here, and we
// decide whether it is one of the scanner's own pane URLs.
//
// Handled shapes (exactly what web/src emits — ScanTabs.tsx, TileFrame.tsx,
// SymbolPanel.tsx use relative URLs; absolute/protocol-relative forms are
// accepted iff they point at the scanner's own origin):
//
//	/?pane=tape&sym={sym}      (sym required, percent-decoded)
//	/?pane=scan&id={id}        (id required, percent-decoded)
//	/?pane=charts
//	/?pane=review
//	/?pane=daylog
//	/?pane=order
//	/?pane=signal[&sym={sym}]  (sym optional, percent-decoded)
//	/?pane=main                (never emitted today, but valid)
//
// ok=false means "not ours": external origins, non-http(s) schemes, other
// paths, missing pane param, missing required sym/id, or unparseable input.
// The JS override then falls through to the original window.open, whose
// unhandled-NewWindowRequested default in WebView2 is documented as "the
// target content will be opened on a popup window" (learn.microsoft.com,
// ICoreWebView2::add_NewWindowRequested) — i.e. a bare WebView2 popup with an
// OS title bar, NOT the user's default browser. The frontend has no such
// links today, so this path is a documented safety net, not a feature.
func ParsePaneURL(raw, base string) (pane Pane, sym, id string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", false
	}
	if u.Scheme != "" || u.Host != "" {
		// Absolute (http://host/...) or protocol-relative (//host/...):
		// must match the scanner origin, else it is an external link.
		b, berr := url.Parse(strings.TrimRight(base, "/"))
		if berr != nil || b.Host == "" {
			return "", "", "", false
		}
		if u.Scheme != "" && !strings.EqualFold(u.Scheme, b.Scheme) {
			return "", "", "", false
		}
		if !strings.EqualFold(u.Host, b.Host) {
			return "", "", "", false
		}
	}
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return "", "", "", false
	}
	// ParseQuery (not u.Query()) so malformed escapes/semicolons reject
	// instead of being silently dropped — anything the frontend didn't
	// build with encodeURIComponent falls through to the browser path.
	q, qerr := url.ParseQuery(u.RawQuery)
	if qerr != nil {
		return "", "", "", false
	}
	switch p := Pane(q.Get("pane")); p {
	case PaneTape:
		if q.Get("sym") == "" {
			return "", "", "", false
		}
		return p, q.Get("sym"), "", true
	case PaneScan:
		if q.Get("id") == "" {
			return "", "", "", false
		}
		return p, "", q.Get("id"), true
	case PaneCharts, PaneMain, PaneReview, PaneDayLog, PaneOrder:
		return p, "", "", true
	case PaneSignal:
		return p, q.Get("sym"), "", true
	}
	return "", "", "", false
}
