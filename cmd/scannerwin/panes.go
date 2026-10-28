// Package main implements scannerwin.exe — a tiny native shell that hosts one
// Live Market Scanner pane (main / tape / charts / scan / review / signal) in
// a BORDERLESS WebView2 window: no browser chrome, no title bar, still
// movable (injected drag pill) and resizable (WS_THICKFRAME edge-resize stays
// on).
//
// Library choice — github.com/jchv/go-webview2, verified against its source
// (repo read 2026-08-16, master = v0.0.0-20260205173254):
//   - Pure Go, no CGo: single static exe, no C toolchain risk. The alternative
//     github.com/webview/webview_go wraps the C webview library via CGo —
//     rejected for that reason alone.
//   - Window() unsafe.Pointer returns the raw HWND (webview.go), so we can
//     restyle the window ourselves with SetWindowLongPtrW after creation.
//   - Bind(name, func) gives JS->Go promises over window.external.invoke
//     (webview.go: Bind/msgcb/callbinding) — used for winDrag/winMinimize/
//     winClose from the injected overlay.
//   - Init(js) maps to WebView2's AddScriptToExecuteOnDocumentCreated
//     (pkg/edge/chromium.go:130), i.e. runs on every new document before
//     window.onload — used to inject the drag overlay.
//   - Embeds WebView2Loader.dll via go-winloader; the WebView2 runtime itself
//     ships with Windows 11.
//   - WebView2 user data folder: WebViewOptions.DataPath is left empty, so the
//     library defaults it to %AppData%\<exe base name> (pkg/edge/chromium.go
//     Embed: GetModuleFileName → filepath.Join(os.Getenv("AppData"), exeName))
//     — i.e. every pane process shares %AppData%\scannerwin.exe. Sharing one
//     user data folder across processes is supported by WebView2 on the
//     condition that all of them create the environment with the same options
//     (else creation fails with ERROR_INVALID_STATE — learn.microsoft.com,
//     "Manage user data folders"). Ours are byte-identical: same exe, nil
//     environment options, default browser path. The panes therefore share a
//     single Chromium browser-process tree, which is the cheap case; the last
//     pane process to exit takes the tree down with it.
//   - The library does NOT declare DPI awareness (no SetProcessDpiAwareness*
//     anywhere in the repo), so we declare Per-Monitor-V2 ourselves at startup
//     — exactly once, before any HWND exists.
//   - Its edge package exposes the AddNewWindowRequested vtable slot but no
//     high-level event wrapper. We do NOT wire it: multi-window spawning is
//     done by overriding window.open in injected JS (nativeShellJS) and
//     routing pane URLs to a bound Go func that spawns a new scannerwin.exe
//     process (spawn.go). Non-pane URLs fall through to the original
//     window.open, where WebView2's unhandled-NewWindowRequested default
//     applies ("the target content will be opened on a popup window" —
//     ICoreWebView2::add_NewWindowRequested docs).
//
// This repo takes pride in avoiding dependencies, and go.mod grows one here
// anyway: hand-rolling WebView2 COM hosting (environment creation, controller
// completed-handler chains, the loader DLL dance) is not a reasonable
// alternative to a 300-star library we can read end to end.
//
// Backend-not-up UX (v1, honest limitation): if scanner.exe is not listening
// on the base URL yet, the WebView shows Edge's own connection-error page.
// Once the user retries/reloads and the page loads, the web app self-connects
// and self-reconnects over its WebSocket. A pre-navigation TCP probe with a
// friendly retry page is v2.
package main

import (
	"fmt"
	"net/url"
	"strings"
)

// Pane is one of the scanner's window kinds. The React bundle dispatches
// client-side on ?pane= (web/src/main.tsx): scan → ScanPane(id), tape →
// TapePane(sym), charts → ChartsPane, review → ReviewPane, signal →
// SignalPane(sym), anything else → main App.
type Pane string

const (
	PaneMain   Pane = "main"
	PaneTape   Pane = "tape"
	PaneCharts Pane = "charts"
	PaneScan   Pane = "scan"
	PaneReview Pane = "review"
	PaneDayLog Pane = "daylog" // the Day Log window: every alert of an ET day with its outcome, plus the top gainers and losers
	PaneOrder  Pane = "order"  // the Order Ticket: a small ALWAYS-ON-TOP window that follows the TWS ticker
	PaneSignal Pane = "signal" // the short-fade signal window; sym is OPTIONAL (follows the global selection when absent)
)

// ValidPane reports whether p names a known pane.
func ValidPane(p Pane) bool {
	switch p {
	case PaneMain, PaneTape, PaneCharts, PaneScan, PaneReview, PaneSignal, PaneDayLog, PaneOrder:
		return true
	}
	return false
}

// DefaultSize returns the outer window size a pane opens at when no saved
// geometry exists. tape/charts/scan/review/signal mirror the frontend's own
// window.open feature strings (web/src/components: ScanTabs 920x820 + 1600x900
// (charts) + 1100x800 + 1600x900 (signal), TileFrame 560x780); main gets a
// comfortable primary-workspace size. Signal matches charts' 1600x900 (not
// its own original 1400x900) since addendum 2 turned it into a grid of
// signal charts plus a watchlist sidebar — the same shape of window as the
// charts monitor, not a single dedicated chart anymore.
func DefaultSize(p Pane) (w, h int) {
	switch p {
	case PaneTape:
		return 920, 820
	case PaneCharts:
		return 1600, 900
	case PaneScan:
		return 560, 780
	case PaneReview:
		return 1100, 800
	case PaneDayLog:
		return 1300, 850
	case PaneOrder:
		return 320, 260
	case PaneSignal:
		return 1600, 900
	default: // PaneMain
		return 1440, 900
	}
}

// jsEscape mirrors JavaScript's encodeURIComponent closely enough for our
// query values. Go's url.QueryEscape differs on one character class we can
// actually hit: space becomes "+" in Go but "%20" in JS, so swap it. (The
// remaining divergent characters — !'()*~ — cannot appear in ticker symbols
// or scan ids, which are server-seeded slugs like "pm-gap-fade".)
func jsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// PaneURL builds the exact URL the frontend itself uses for each pane
// (web/src: ScanTabs.tsx, TileFrame.tsx, SymbolPanel.tsx — param order is
// pane first, then sym/id). base defaults to http://127.0.0.1:7878 upstream;
// trailing slashes are tolerated.
//
//	main   → {base}/
//	tape   → {base}/?pane=tape&sym={sym}
//	charts → {base}/?pane=charts
//	scan   → {base}/?pane=scan&id={id}
//	review → {base}/?pane=review
//	daylog → {base}/?pane=daylog
//	order  → {base}/?pane=order
//	signal → {base}/?pane=signal  (sym optional: {base}/?pane=signal&sym={sym} when given)
func PaneURL(base string, pane Pane, sym, id string) (string, error) {
	if !ValidPane(pane) {
		return "", fmt.Errorf("unknown pane %q (want main|tape|charts|scan|review|signal|daylog|order)", pane)
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		return "", fmt.Errorf("empty base URL")
	}
	switch pane {
	case PaneTape:
		if sym == "" {
			return "", fmt.Errorf("--pane tape requires --sym")
		}
		return base + "/?pane=tape&sym=" + jsEscape(sym), nil
	case PaneScan:
		if id == "" {
			return "", fmt.Errorf("--pane scan requires --id")
		}
		return base + "/?pane=scan&id=" + jsEscape(id), nil
	case PaneCharts:
		return base + "/?pane=charts", nil
	case PaneReview:
		return base + "/?pane=review", nil
	case PaneDayLog:
		return base + "/?pane=daylog", nil
	case PaneOrder:
		return base + "/?pane=order", nil
	case PaneSignal:
		if sym == "" {
			return base + "/?pane=signal", nil
		}
		return base + "/?pane=signal&sym=" + jsEscape(sym), nil
	default: // PaneMain
		return base + "/", nil
	}
}
