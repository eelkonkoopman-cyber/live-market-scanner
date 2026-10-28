// Package tray puts a Live Market Scanner icon in the Windows notification
// area (system tray) with a context menu, using nothing but Win32 calls
// through golang.org/x/sys/windows (no cgo). On other platforms Run only blocks
// until its context is cancelled, so callers need no build tags.
//
// This file holds the platform-independent API and the pure helpers (unit
// tested); the Win32 message loop lives in tray_windows.go.
package tray

import (
	"context"
	"encoding/binary"
	"time"
	"unicode/utf16"
)

// Item is one context-menu entry.
type Item struct {
	Label   string // "" = separator
	OnClick func()
	Default bool // rendered bold (the double-click action)
}

// Options configures Run.
type Options struct {
	Tooltip      string
	Icon         []byte // an .ico file (assets.Icon)
	Items        []Item
	OnDefault    func() // left double-click AND left single-click on the icon
	OnSessionEnd func() // Windows logoff/shutdown, or WM_CLOSE sent to the tray window (e.g. `taskkill /IM scanner.exe` without /F)
}

// Run shows the tray icon and blocks until ctx is done; the icon is removed
// before it returns. Callbacks are always invoked on their own goroutines, never
// on the tray's message loop. Only one Run may be active per process.
func Run(ctx context.Context, o Options) error { return run(ctx, o) }

// maxTipUnits is the capacity of NOTIFYICONDATAW.szTip minus its NUL.
const maxTipUnits = 127

// tipUTF16 converts s to UTF-16, truncated to at most maxTipUnits code units
// (never splitting a surrogate pair) and without a terminating NUL.
func tipUTF16(s string) []uint16 {
	u := utf16.Encode([]rune(s))
	if len(u) <= maxTipUnits {
		return u
	}
	u = u[:maxTipUnits]
	if last := u[len(u)-1]; last >= 0xD800 && last < 0xDC00 { // dangling high surrogate
		u = u[:len(u)-1]
	}
	return u
}

// debouncer suppresses repeat triggers inside a window: a left double-click
// arrives as WM_LBUTTONUP then WM_LBUTTONDBLCLK and must fire the action once.
type debouncer struct {
	window time.Duration
	last   time.Time
}

// allow reports whether an event at now should fire, recording it if so.
func (d *debouncer) allow(now time.Time) bool {
	if !d.last.IsZero() && now.Sub(d.last) < d.window {
		return false
	}
	d.last = now
	return true
}

// pickIconImage parses an .ico file and returns the raw image bytes (the DIB
// or PNG payload CreateIconFromResourceEx accepts) of the entry best matching
// want pixels: the smallest entry >= want, else the largest available.
func pickIconImage(ico []byte, want int) ([]byte, bool) {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	bestSize := 0
	var best []byte
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		if e+16 > len(ico) {
			break
		}
		sz := int(ico[e])
		if sz == 0 {
			sz = 256
		}
		length := int(binary.LittleEndian.Uint32(ico[e+8:]))
		off := int(binary.LittleEndian.Uint32(ico[e+12:]))
		if off < 0 || length <= 0 || off+length > len(ico) {
			continue
		}
		better := false
		switch {
		case best == nil:
			better = true
		case bestSize < want && sz > bestSize: // grow towards want
			better = true
		case bestSize >= want && sz >= want && sz < bestSize: // tighter fit
			better = true
		}
		if better {
			bestSize, best = sz, ico[off:off+length]
		}
	}
	return best, best != nil
}
