//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procLoadIconW                = user32.NewProc("LoadIconW")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
)

// Win32 constants (winuser.h / shellapi.h values).
const (
	wmNull            = 0x0000
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmContextMenu     = 0x007B
	wmLButtonUp       = 0x0202
	wmLButtonDblClk   = 0x0203
	wmRButtonUp       = 0x0205
	wmApp             = 0x8000

	wmTrayCallback = wmApp + 1 // uCallbackMessage for the icon
	wmTrayQuit     = wmApp + 2 // private: ctx cancelled, tear down

	nimAdd    = 0
	nimDelete = 2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0
	mfSeparator = 0x800

	tpmRightButton = 0x2
	tpmNoNotify    = 0x80
	tpmReturnCmd   = 0x100

	smCxSmIcon = 49

	idiApplication = 32512

	wsExToolWindow = 0x80
	wsOverlapped   = 0

	iconVersion = 0x00030000 // CreateIconFromResourceEx dwVer

	errClassExists = 1410 // ERROR_CLASS_ALREADY_EXISTS

	windowClassName = "LiveMarketScannerTray"
	debounceWindow  = 500 * time.Millisecond
)

// wndClassEx mirrors WNDCLASSEXW.
type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

// winMsg mirrors MSG (Go's natural alignment reproduces the 64-bit layout:
// hwnd@0, message@8, wParam@16, lParam@24, time@32, pt@36, size 48).
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// notifyIconData mirrors NOTIFYICONDATAW (shellapi.h, version 6+ layout). Go
// aligns fields exactly as MSVC does on both 32- and 64-bit, so cbSize is just
// unsafe.Sizeof (976 on amd64: hWnd@8, uID@16, uFlags@20, uCallbackMessage@24,
// hIcon@32, szTip@40, ...).
type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeout         uint32 // union with uVersion
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

// The window procedure callback must stay referenced for the process lifetime
// (windows.NewCallback slots are never freed).
var wndProcCB = windows.NewCallback(wndProc)

// state is the single active tray. It is set by run before the window exists
// and only read from the locked message-loop thread.
type state struct {
	opts        Options
	hwnd        uintptr
	hicon       uintptr
	ownsIcon    bool
	nid         notifyIconData
	taskbarMsg  uint32
	added       bool
	defaultDeb  debouncer
	sessionOnce func() // OnSessionEnd, fired at most once
}

var cur *state

func run(ctx context.Context, o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	s := &state{opts: o, defaultDeb: debouncer{window: debounceWindow}}
	if o.OnSessionEnd != nil {
		fired := false
		s.sessionOnce = func() {
			if fired {
				return
			}
			fired = true
			go o.OnSessionEnd()
		}
	}
	cur = s
	defer func() { cur = nil }()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString(windowClassName)
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProcCB,
		hInstance:     hInst,
		lpszClassName: className,
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 && !errors.Is(err, windows.Errno(errClassExists)) {
		return fmt.Errorf("tray: RegisterClassExW: %w", err)
	}

	s.taskbarMsg = registerMessage("TaskbarCreated")
	s.hicon, s.ownsIcon = loadIcon(o.Icon)

	// A hidden top-level (NOT HWND_MESSAGE) window: message-only windows do not
	// receive the TaskbarCreated broadcast.
	title, _ := windows.UTF16PtrFromString("Live Market Scanner tray")
	hwnd, _, err := procCreateWindowExW.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlapped,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		if s.ownsIcon {
			procDestroyIcon.Call(s.hicon)
		}
		return fmt.Errorf("tray: CreateWindowExW: %w", err)
	}
	s.hwnd = hwnd

	s.nid = notifyIconData{
		hWnd:             hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCallback,
		hIcon:            s.hicon,
	}
	s.nid.cbSize = uint32(unsafe.Sizeof(s.nid))
	copy(s.nid.szTip[:], tipUTF16(o.Tooltip)) // rest stays zero => NUL terminated
	s.addIcon()
	// Every exit path removes the icon so no ghost is left behind.
	defer func() {
		s.removeIcon()
		if s.ownsIcon {
			procDestroyIcon.Call(s.hicon)
		}
	}()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			procPostMessageW.Call(hwnd, wmTrayQuit, 0, 0)
		case <-done:
		}
	}()

	var msg winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func (s *state) addIcon() {
	r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&s.nid)))
	s.added = r != 0
	if !s.added {
		// Explorer may not be up yet (early login); TaskbarCreated re-adds.
		logx.L().Warn("tray: Shell_NotifyIcon(NIM_ADD) failed; will retry when the taskbar appears", "err", err)
	}
}

func (s *state) removeIcon() {
	if s.added {
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&s.nid)))
		s.added = false
	}
}

func registerMessage(name string) uint32 {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

// loadIcon builds an HICON of the small-icon size from the .ico bytes. The
// second result reports whether the caller owns (must DestroyIcon) the handle;
// the stock IDI_APPLICATION fallback is shared and not owned.
//
// Note: LookupIconIdFromDirectoryEx expects the GRPICONDIR *resource* layout
// (2-byte ids), which a raw .ico file does not have, so the file is parsed here
// instead (pickIconImage) and the chosen DIB goes straight to
// CreateIconFromResourceEx.
func loadIcon(ico []byte) (uintptr, bool) {
	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if size == 0 {
		size = 16
	}
	if img, ok := pickIconImage(ico, int(size)); ok {
		h, _, _ := procCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)),
			1, iconVersion, size, size, 0,
		)
		if h != 0 {
			return h, true
		}
	}
	h, _, _ := procLoadIconW.Call(0, idiApplication)
	return h, false
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	s := cur
	if s == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	m := uint32(msg)
	switch {
	case s.taskbarMsg != 0 && m == s.taskbarMsg:
		s.added = false
		s.addIcon() // Explorer restarted
		return 0
	case m == wmTrayCallback:
		switch uint32(lParam & 0xFFFF) {
		case wmLButtonUp, wmLButtonDblClk:
			if s.opts.OnDefault != nil && s.defaultDeb.allow(time.Now()) {
				go s.opts.OnDefault()
			}
		case wmRButtonUp, wmContextMenu:
			s.showMenu()
		}
		return 0
	case m == wmQueryEndSession:
		return 1
	case m == wmEndSession:
		if wParam != 0 && s.sessionOnce != nil {
			s.sessionOnce()
		}
		return 0
	case m == wmClose:
		// Do not DestroyWindow here: OnSessionEnd cancels the context and the
		// wmTrayQuit path below tears down.
		if s.sessionOnce != nil {
			s.sessionOnce()
		}
		return 0
	case m == wmTrayQuit:
		s.removeIcon()
		procDestroyWindow.Call(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// showMenu runs the popup menu modally on the message-loop thread, then hands
// the chosen item's OnClick to a fresh goroutine.
func (s *state) showMenu() {
	if len(s.opts.Items) == 0 {
		return
	}
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	defaultPos := -1
	for i, it := range s.opts.Items {
		id := uintptr(i + 1) // 0 means "cancelled" from TPM_RETURNCMD
		if it.Label == "" {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		label, err := windows.UTF16PtrFromString(it.Label)
		if err != nil {
			continue
		}
		procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(label)))
		if it.Default && defaultPos < 0 {
			defaultPos = i
		}
	}
	if defaultPos >= 0 {
		procSetMenuDefaultItem.Call(menu, uintptr(defaultPos), 1) // fByPos = TRUE
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required so the menu dismisses when the user clicks elsewhere.
	procSetForegroundWindow.Call(s.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu,
		tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.x), uintptr(pt.y), 0, s.hwnd, 0)
	procPostMessageW.Call(s.hwnd, wmNull, 0, 0) // documented fix (KB135788)

	if i := int(cmd) - 1; cmd != 0 && i >= 0 && i < len(s.opts.Items) {
		if fn := s.opts.Items[i].OnClick; fn != nil {
			go fn()
		}
	}
}
