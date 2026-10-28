//go:build windows

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// spawnDetached starts exe with args as an independent process and lets go of
// it: no Wait (Windows has no zombie reaping to do) and an explicit Release so
// the process handle does not linger for the parent's lifetime. The child is
// NOT tied to this process — Windows only ties lifetimes via job objects,
// which we deliberately do not create — so closing the window that spawned a
// pane never closes the pane. scannerwin.exe is linked with -H windowsgui, so
// the child opens no console window.
func spawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// windowTitle labels the window for alt-tab / taskbar (there is no visible
// title bar to show it in).
func windowTitle(o Options) string {
	switch o.Pane {
	case PaneTape:
		return "Tape — " + o.Sym
	case PaneCharts:
		return "Charts Monitor"
	case PaneReview:
		return "Morning Review"
	case PaneDayLog:
		return "Day Log"
	case PaneOrder:
		return "Order Ticket"
	case PaneSignal:
		if o.Sym != "" {
			return "Signal — " + o.Sym
		}
		return "Signal"
	case PaneScan:
		return "Scan — " + o.ID
	default:
		return "Market Scanner"
	}
}

func main() {
	// stderr is invisible under -H windowsgui (Explorer/double-click launches
	// attach no console), so every fatal pre-window error is ALSO shown in a
	// message box — a garbage --pane must fail loudly, not silently do nothing.
	// diag captures flag's own error+usage output for that box.
	var diag bytes.Buffer
	opts, err := ParseArgs(os.Args[1:], io.MultiWriter(os.Stderr, &diag))
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			alertBox("scannerwin", strings.TrimSpace(diag.String()), mbIconInfo)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "scannerwin:", err)
		msg := strings.TrimSpace(diag.String())
		if msg == "" { // post-parse validation error: flag printed nothing
			msg = err.Error()
		}
		alertBox("scannerwin — bad command line", msg, mbIconError)
		os.Exit(2)
	}
	paneURL, err := PaneURL(opts.Base, opts.Pane, opts.Sym, opts.ID)
	if err != nil { // already validated in ParseArgs; belt and braces
		fmt.Fprintln(os.Stderr, "scannerwin:", err)
		alertBox("scannerwin", err.Error(), mbIconError)
		os.Exit(2)
	}
	if os.Getenv("SCANNERWIN_DEBUG") == "1" {
		// Turns on the overlay's HTTP diagnostics beacons (see overlay.go).
		if strings.Contains(paneURL, "?") {
			paneURL += "&swdbg=1"
		} else {
			paneURL += "?swdbg=1"
		}
	}

	// Before any HWND exists (see win32_windows.go for why this is the one
	// and only DPI declaration).
	declarePerMonitorV2DPI()

	statePath, spErr := StatePath()
	if spErr != nil {
		log.Printf("scannerwin: no home dir, geometry will not persist: %v", spErr)
	}
	var saved Rect
	hasSaved := false
	if statePath != "" {
		saved, hasSaved = LoadStates(statePath)[string(opts.Pane)]
	}

	defW, defH := DefaultSize(opts.Pane)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title:  windowTitle(opts),
			Width:  uint(defW),
			Height: uint(defH),
		},
	})
	if w == nil {
		const msg = "failed to create WebView2 window (is the WebView2 runtime installed?)"
		fmt.Fprintln(os.Stderr, "scannerwin: "+msg)
		alertBox("scannerwin", msg, mbIconError)
		os.Exit(1)
	}
	defer w.Destroy()

	hwnd := uintptr(w.Window())
	makeBorderless(hwnd)
	if opts.Pane == PaneOrder {
		makeTopmost(hwnd) // the Order Ticket floats over TWS
	}
	if hasSaved {
		setWindowRect(hwnd, ClampToScreen(saved, virtualScreen()))
	}
	// Show explicitly, once the saved geometry is applied. go-webview2's own
	// ShowWindow(SW_SHOW) is this process's FIRST ShowWindow call, which Windows
	// replaces with the launcher's STARTUPINFO show state — SW_HIDE from a
	// HideWindow spawn, or minimized via a minimized shortcut — and can leave
	// the window invisible with no error anywhere. A second call is always
	// honored, so no launch path can hide the window again.
	_, _, _ = procShowWindow.Call(hwnd, swShowNormal)

	// lastRect is the newest non-minimized geometry, refreshed by the 30s
	// snapshot ticker, by winClose, and by the drag handler. It is what gets
	// persisted after Run() unwinds — by then the HWND is already destroyed
	// and GetWindowRect can no longer be asked.
	var (
		rectMu   sync.Mutex
		lastRect Rect
		hasRect  bool
	)
	snapshot := func() {
		if isIconic(hwnd) {
			return
		}
		if r, ok := getWindowRect(hwnd); ok {
			rectMu.Lock()
			lastRect, hasRect = r, true
			rectMu.Unlock()
		}
	}
	var saveMu sync.Mutex // serializes SaveState's read-modify-write (ticker goroutine vs winClose)
	persist := func() {
		if statePath == "" {
			return
		}
		rectMu.Lock()
		r, ok := lastRect, hasRect
		rectMu.Unlock()
		if !ok {
			return
		}
		saveMu.Lock()
		defer saveMu.Unlock()
		if err := SaveState(statePath, opts.Pane, r); err != nil {
			log.Printf("scannerwin: saving window state: %v", err)
		}
	}

	must := func(err error) {
		if err != nil {
			log.Fatalf("scannerwin: bind: %v", err)
		}
	}
	must(w.Bind("winDrag", func() {
		// Return immediately (the JS promise resolves and the UI thread keeps
		// pumping); the goroutine follows the physical cursor until mouse-up.
		// See dragLoop for why the WM_NCLBUTTONDOWN trick cannot work here.
		dbgf("winDrag: bound call fired")
		go dragLoop(hwnd, snapshot)
	}))
	must(w.Bind("winMinimize", func() {
		minimizeWindow(hwnd)
	}))
	must(w.Bind("winClose", func() {
		snapshot()
		persist()
		requestClose(hwnd)
	}))

	// Multi-window spawning: each pane is its own scannerwin.exe process (see
	// SpawnArgs for the crash-isolation rationale). os.Executable is resolved
	// once up front; if that fails the binds return the error, the JS promise
	// rejects, and the window.open override falls back to a WebView2 popup.
	exe, exeErr := os.Executable()
	spawn := func(pane Pane, sym, id string) error {
		if exeErr != nil {
			return fmt.Errorf("cannot locate own executable: %w", exeErr)
		}
		// Validate before forking so a bad request fails here (visible to the
		// JS caller) instead of in a child that dies with no console.
		if _, err := PaneURL(opts.Base, pane, sym, id); err != nil {
			return err
		}
		return spawnDetached(exe, SpawnArgs(pane, sym, id, opts.Base))
	}
	// openPane: direct spawn entry (pane, sym, id as separate args) for any
	// future in-app caller that knows what it wants.
	must(w.Bind("openPane", func(pane, sym, id string) error {
		return spawn(Pane(pane), sym, id)
	}))
	// openPaneURL: the window.open override's router (see nativeShellJS).
	// Returns handled=false for non-pane URLs so JS falls through.
	must(w.Bind("openPaneURL", func(raw string) (bool, error) {
		pane, sym, id, ok := ParsePaneURL(raw, opts.Base)
		if !ok {
			return false, nil
		}
		if err := spawn(pane, sym, id); err != nil {
			log.Printf("scannerwin: spawn %s: %v", pane, err)
			return false, err
		}
		return true, nil
	}))

	w.Init(overlayJS)
	w.Init(nativeShellJS)

	// Periodic geometry snapshot + save, so a non-graceful exit (taskkill,
	// crash) loses at most 30s of window arranging.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				snapshot() // GetWindowRect is safe cross-thread
				persist()
			case <-stop:
				return
			}
		}
	}()

	snapshot() // baseline in case the user closes within the first 30s
	w.Navigate(paneURL)
	w.Run()
	close(stop)
	persist()
}
