package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Geometry persistence: ~/.marketscanner/windows.json, one entry per PANE
// TYPE ("main"/"tape"/"charts"/"scan"), not per pane instance. Scan ids and
// tape symbols are ephemeral — keying on them would grow the file without
// bound and rarely hit; keying per type means "the next tape window opens
// where the last tape window was", which is the behavior a fixed multi-monitor
// day-trading layout actually wants. If two scan pop-outs are open at once the
// last one closed wins — acceptable for v1.
//
// Everything in this file is pure I/O-parameterized logic (explicit paths,
// rects passed in) so it is fully unit-testable without a window.

// Rect is an outer window rectangle in virtual-screen coordinates (x/y may be
// negative on monitors left of or above the primary).
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// WindowStates maps pane type → last saved geometry.
type WindowStates map[string]Rect

// StatePath returns ~/.marketscanner/windows.json.
func StatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".marketscanner", "windows.json"), nil
}

// LoadStates reads the state file. A missing, unreadable, or corrupt file is
// not an error — the shell must always open — it just yields an empty map.
func LoadStates(path string) WindowStates {
	b, err := os.ReadFile(path)
	if err != nil {
		return WindowStates{}
	}
	var s WindowStates
	if err := json.Unmarshal(b, &s); err != nil || s == nil {
		return WindowStates{}
	}
	return s
}

// Cross-process locking parameters for SaveState. Each pane window is its own
// scannerwin.exe process and each runs a periodic persist ticker, so writes to
// windows.json race ACROSS PROCESSES — an in-process mutex is not enough. A
// real holder keeps the lock for single-digit milliseconds, so anything older
// than lockStaleAfter is a leftover from a killed process and gets broken.
const (
	lockRetryEvery = 5 * time.Millisecond
	lockStaleAfter = 5 * time.Second
	lockTimeout    = 2 * time.Second
)

// acquireLockFile takes a cross-process exclusive lock by creating lockPath
// with O_CREATE|O_EXCL (atomic on NTFS: exactly one creator wins). It returns
// a release func on success. Stale locks (older than lockStaleAfter) are
// deleted and re-contended. On timeout it returns an error; the caller
// proceeds UNLOCKED — losing one rect to a rare race beats never persisting.
func acquireLockFile(lockPath string, timeout time.Duration) (release func(), err error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { os.Remove(lockPath) }, nil
		}
		if errors.Is(err, os.ErrExist) {
			if st, serr := os.Stat(lockPath); serr == nil && time.Since(st.ModTime()) > lockStaleAfter {
				os.Remove(lockPath) // stale: holder died; next loop re-contends the create
				continue
			}
		}
		// Non-ErrExist errors are retried too, NOT returned: on Windows a
		// competitor's just-released lock file can sit in the delete-pending
		// state for a moment, during which O_CREATE|O_EXCL fails with
		// ERROR_ACCESS_DENIED rather than ERROR_FILE_EXISTS. Bailing out here
		// would mean writing UNLOCKED while another writer holds the lock —
		// the exact clobber this lock exists to prevent (caught by
		// TestSaveStateConcurrent under -race).
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for " + lockPath)
		}
		time.Sleep(lockRetryEvery)
	}
}

// writeFileAtomic writes b to path via a same-directory temp file + rename, so
// a concurrent LoadStates in another pane process sees either the old or the
// new complete file — never a torn half-write (which would parse as corrupt,
// load as empty, and make the next merge wipe every pane's geometry). Go opens
// files with FILE_SHARE_DELETE on Windows, so the rename-over-open-reader is
// legal.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		// The replace-rename can fail transiently on Windows when an
		// antivirus scanner, the indexer, or a delete-pending handle briefly
		// holds the destination (ERROR_ACCESS_DENIED / ERROR_SHARING_
		// VIOLATION). A fixed retry COUNT proved too small under heavy system
		// load (observed: 5×10ms exhausted while a fullscreen game saturated
		// the machine, losing the write) — retry against a generous DEADLINE
		// instead; geometry saves are rare and never on a latency-sensitive
		// path.
		deadline := time.Now().Add(2 * time.Second)
		for {
			werr = os.Rename(tmpName, path)
			if werr == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if werr != nil {
		os.Remove(tmpName)
		return werr
	}
	return nil
}

// SaveState merges one pane's rect into the file. The read-modify-write is
// serialized across processes by a lock file (each pane window is a separate
// process — see acquireLockFile) and the final write is atomic (see
// writeFileAtomic), so concurrently open panes of other types can't clobber
// each other's entries; same-key writes are last-writer-wins by design.
func SaveState(path string, pane Pane, r Rect) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if release, err := acquireLockFile(path+".lock", lockTimeout); err == nil {
		defer release()
	}
	// else: proceed unlocked — a wedged/stale lock must never brick persistence.
	s := LoadStates(path)
	s[string(pane)] = r
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// Window sanity floor: never restore something too small to grab.
const (
	minW = 200
	minH = 120
)

// ClampToScreen fits a remembered rect into the current virtual screen
// bounds (the bounding box of all monitors — GetSystemMetrics
// SM_XVIRTUALSCREEN=76 / SM_YVIRTUALSCREEN=77 / SM_CXVIRTUALSCREEN=78 /
// SM_CYVIRTUALSCREEN=79, values verified against winuser.h). A window last
// seen on a since-disconnected monitor must not restore off-screen. With an
// L-shaped multi-monitor arrangement the bounding box can still contain dead
// zones; clamping into the box is the standard cheap answer — the drag pill
// remains reachable in every rectangular arrangement, and v2 could walk
// per-monitor work areas.
func ClampToScreen(r Rect, screen Rect) Rect {
	if r.W < minW {
		r.W = minW
	}
	if r.H < minH {
		r.H = minH
	}
	if r.W > screen.W {
		r.W = screen.W
	}
	if r.H > screen.H {
		r.H = screen.H
	}
	// Slide fully inside the bounding box (right/bottom first so the
	// top-left corner — where the drag pill lives — wins when oversized).
	if r.X+r.W > screen.X+screen.W {
		r.X = screen.X + screen.W - r.W
	}
	if r.Y+r.H > screen.Y+screen.H {
		r.Y = screen.Y + screen.H - r.H
	}
	if r.X < screen.X {
		r.X = screen.X
	}
	if r.Y < screen.Y {
		r.Y = screen.Y
	}
	return r
}
