//go:build windows

package main

// Thin Win32 layer for the borderless shell. Every constant below is spelled
// with its symbolic name AND value, verified against the Windows SDK headers
// (wine-mirror/wine include/winuser.h + winuser.rh + windef.h, which mirror
// SDK values exactly) or against jchv/go-webview2's own internal/w32 where
// noted. Nothing here is guessed.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetWindowLongPtrW         = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW         = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procGetWindowRect             = user32.NewProc("GetWindowRect")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procShowWindow                = user32.NewProc("ShowWindow")
	procIsIconic                  = user32.NewProc("IsIconic")
	procMessageBoxW               = user32.NewProc("MessageBoxW")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procGetAsyncKeyState          = user32.NewProc("GetAsyncKeyState")
)

const (
	// winuser.h: #define GWL_STYLE (-16) (also go-webview2 internal/w32:106)
	gwlStyle = -16

	// winuser.rh: WS_CAPTION = WS_BORDER(0x00800000)|WS_DLGFRAME(0x00400000)
	wsCaption = 0x00C00000
	// winuser.rh: #define WS_THICKFRAME 0x00040000 — the resizable border;
	// kept ON so OS edge-resize keeps working without a title bar.
	wsThickFrame = 0x00040000

	// winuser.h SetWindowPos() flags
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020 // frame changed: send WM_NCCALCSIZE
	// winuser.h: #define SWP_ASYNCWINDOWPOS 0x4000 — posts the request to the
	// thread that owns the window instead of blocking on it, which is the
	// documented way to make cross-thread SetWindowPos deadlock-free. The
	// drag loop below runs on a plain goroutine, so this flag is load-bearing.
	swpAsyncWindowPos = 0x4000

	// winuser.rh window messages
	wmClose = 0x0010

	// winuser.h: #define VK_LBUTTON 0x01 — GetAsyncKeyState's high bit tells
	// us whether the physical left button is down right now, regardless of
	// which window (or which PROCESS) owns the mouse capture.
	vkLButton = 0x01

	// winuser.rh: #define SW_SHOWNORMAL 1
	swShowNormal = 1

	// winuser.rh: #define SW_MINIMIZE 6
	swMinimize = 6

	// winuser.h MessageBox() flags: MB_OK 0x0; MB_ICONHAND 0x00000010
	// (MB_ICONERROR is an alias); MB_ICONASTERISK 0x00000040
	// (MB_ICONINFORMATION is an alias); MB_SETFOREGROUND 0x00010000.
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbIconInfo      = 0x00000040
	mbSetForeground = 0x00010000

	// winuser.h GetSystemMetrics() indices for the virtual screen (bounding
	// box of all monitors; origin can be negative).
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79
)

// dpiAwarenessContextPerMonitorAwareV2 — windef.h:
// #define DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 ((DPI_AWARENESS_CONTEXT)-4)
// DPI_AWARENESS_CONTEXT is a pseudo-handle, so the value is the sign-extended
// integer -4.
var dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // two's-complement -4

// declarePerMonitorV2DPI opts the process into Per-Monitor-V2 DPI awareness.
// Must run before any HWND is created. go-webview2 was verified (full repo
// search) to declare NO DPI awareness itself, so declaring here is the single
// declaration — no manifest, no double-set. Failure (already set, or a
// pre-1703 Windows) is non-fatal: the shell still works, just DPI-scaled.
func declarePerMonitorV2DPI() {
	_, _, _ = procSetProcessDpiAwarenessCtx.Call(dpiAwarenessContextPerMonitorAwareV2)
}

// makeBorderless strips WS_CAPTION from the window go-webview2 created with
// WS_OVERLAPPEDWINDOW (0xCF0000, webview.go CreateWithOptions), keeping
// WS_THICKFRAME so the OS still provides edge-resize. Classic borderless
// trade-offs, accepted deliberately: (1) no system-drawn title bar, so no
// system close/minimize buttons — the injected overlay provides them;
// (2) a thin sizing border remains visible on some themes; (3) Windows may
// draw a 1px top line where the caption was. SWP_FRAMECHANGED forces the
// non-client area to recalculate immediately.
func makeBorderless(hwnd uintptr) {
	// Negative constant → uintptr needs a non-constant conversion (Go rejects
	// the constant one as overflow); the wrap to two's complement is intended.
	idx := gwlStyle
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	style &^= uintptr(wsCaption)
	style |= uintptr(wsThickFrame)
	_, _, _ = procSetWindowLongPtrW.Call(hwnd, uintptr(idx), style)
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

// makeTopmost pins the window above every non-topmost window
// (SetWindowPos with HWND_TOPMOST = (HWND)-1).
func makeTopmost(hwnd uintptr) {
	topmost := -1
	_, _, _ = procSetWindowPos.Call(hwnd, uintptr(topmost), 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoActivate)
}

// setWindowRect moves/sizes the window to an outer rect (virtual-screen
// coordinates, so negative x/y are valid).
func setWindowRect(hwnd uintptr, r Rect) {
	_, _, _ = procSetWindowPos.Call(hwnd, 0,
		uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H),
		swpNoZOrder|swpNoActivate)
}

// win32Rect mirrors winuser.h RECT: LONG left, top, right, bottom.
type win32Rect struct {
	Left, Top, Right, Bottom int32
}

// getWindowRect reads the current outer rect. Safe to call from any thread
// (plain user32 query, no message send). ok=false if the call failed (e.g.
// window already destroyed).
func getWindowRect(hwnd uintptr) (Rect, bool) {
	var rr win32Rect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rr)))
	if ret == 0 {
		return Rect{}, false
	}
	return Rect{
		X: int(rr.Left), Y: int(rr.Top),
		W: int(rr.Right - rr.Left), H: int(rr.Bottom - rr.Top),
	}, true
}

// virtualScreen returns the bounding box of all monitors.
func virtualScreen() Rect {
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCxVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCyVirtualScreen)
	return Rect{X: int(int32(x)), Y: int(int32(y)), W: int(int32(w)), H: int(int32(h))}
}

// point mirrors Win32 POINT (windef.h: two LONGs).
type point struct{ x, y int32 }

// cursorPos returns the physical-pixel cursor position (GetCursorPos).
func cursorPos() (point, bool) {
	var p point
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p, r != 0
}

// leftButtonDown reports whether the physical left mouse button is currently
// held (GetAsyncKeyState(VK_LBUTTON) high bit, winuser.h).
func leftButtonDown() bool {
	r, _, _ := procGetAsyncKeyState.Call(vkLButton)
	return r&0x8000 != 0
}

// dragTarget computes where the window's top-left belongs given where it and
// the cursor were when the drag started and where the cursor is now. Pure —
// unit-tested in win32drag_test.go.
func dragTarget(startWin Rect, startCur, nowCur point) (x, y int) {
	return startWin.X + int(nowCur.x-startCur.x), startWin.Y + int(nowCur.y-startCur.y)
}

// dragLoop moves the window with the physical cursor until the left button is
// released, then calls onDone. It deliberately does NOT use the classic
// ReleaseCapture + WM_NCLBUTTONDOWN/HTCAPTION trick: the mousedown that
// starts the drag happens inside the WebView2 browser, whose mouse capture
// lives in msedgewebview2.exe — a DIFFERENT PROCESS — so our ReleaseCapture
// is a no-op and DefWindowProc's modal move loop never receives the mouse
// (observed live: the window simply would not drag). Instead we poll the
// global cursor + button state, which no capture owner can hide from us, and
// reposition via SetWindowPos(SWP_ASYNCWINDOWPOS) so running on a plain
// goroutine cannot deadlock against the UI thread's message pump.
func dragLoop(hwnd uintptr, onDone func()) {
	startCur, ok := cursorPos()
	if !ok {
		dbgf("dragLoop: GetCursorPos failed")
		return
	}
	startWin, ok := getWindowRect(hwnd)
	if !ok {
		dbgf("dragLoop: GetWindowRect failed")
		return
	}
	dbgf("dragLoop: start cur=(%d,%d) win=%+v buttonDown=%v", startCur.x, startCur.y, startWin, leftButtonDown())
	const tick = 8 * time.Millisecond // ~125 Hz: smooth, negligible CPU
	moves := 0
	for leftButtonDown() {
		if cur, ok := cursorPos(); ok {
			x, y := dragTarget(startWin, startCur, cur)
			r, _, e := procSetWindowPos.Call(hwnd, 0,
				uintptr(x), uintptr(y), 0, 0,
				swpNoSize|swpNoZOrder|swpNoActivate|swpAsyncWindowPos)
			if moves == 0 {
				dbgf("dragLoop: first SetWindowPos(%d,%d) ret=%d err=%v", x, y, r, e)
			}
			moves++
		}
		time.Sleep(tick)
	}
	dbgf("dragLoop: end after %d moves", moves)
	if onDone != nil {
		onDone()
	}
}

// requestClose posts WM_CLOSE; go-webview2's wndproc turns that into
// DestroyWindow → WM_DESTROY → Terminate, unwinding Run().
func requestClose(hwnd uintptr) {
	_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
}

// minimizeWindow minimizes via ShowWindow(SW_MINIMIZE=6).
func minimizeWindow(hwnd uintptr) {
	_, _, _ = procShowWindow.Call(hwnd, swMinimize)
}

// alertBox shows a modal message box (hwnd 0 = no owner; fine before any
// window exists). scannerwin.exe is linked -H windowsgui, so stderr goes
// nowhere when launched from Explorer or a double-click — a bad command line
// or a missing WebView2 runtime must still fail with a message the user can
// actually see, not exit silently. icon is mbIconError or mbIconInfo.
func alertBox(title, text string, icon uintptr) {
	t, terr := windows.UTF16PtrFromString(title)
	x, xerr := windows.UTF16PtrFromString(text)
	if terr != nil || xerr != nil {
		return // interior NUL — nothing sane to show
	}
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(x)), uintptr(unsafe.Pointer(t)),
		mbOK|icon|mbSetForeground)
}

// isIconic reports whether the window is minimized (IsIconic, winuser.h:4399).
// A minimized window's GetWindowRect is the off-screen icon slot (-32000,
// -32000) — never persist that as a real position.
func isIconic(hwnd uintptr) bool {
	ret, _, _ := procIsIconic.Call(hwnd)
	return ret != 0
}

// dbgf appends drag/window diagnostics to %TEMP%\scannerwin.log when
// SCANNERWIN_DEBUG=1. Under -H windowsgui there is no console, so a file is
// the only way to see what the input path actually did.
func dbgf(format string, args ...any) {
	if os.Getenv("SCANNERWIN_DEBUG") != "1" {
		return
	}
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "scannerwin.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}
