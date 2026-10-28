package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "windows.json") // parent dir must be auto-created

	// Missing file → empty map, no error.
	if s := LoadStates(path); len(s) != 0 {
		t.Fatalf("LoadStates(missing) = %v, want empty", s)
	}

	r1 := Rect{X: 100, Y: 50, W: 920, H: 820}
	if err := SaveState(path, PaneTape, r1); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	r2 := Rect{X: -1920, Y: 0, W: 1600, H: 900} // secondary monitor left of primary
	if err := SaveState(path, PaneCharts, r2); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	s := LoadStates(path)
	if got := s["tape"]; got != r1 {
		t.Errorf("tape = %+v, want %+v (second save must not clobber other keys)", got, r1)
	}
	if got := s["charts"]; got != r2 {
		t.Errorf("charts = %+v, want %+v", got, r2)
	}

	// Overwrite same key.
	r3 := Rect{X: 5, Y: 6, W: 921, H: 821}
	if err := SaveState(path, PaneTape, r3); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if got := LoadStates(path)["tape"]; got != r3 {
		t.Errorf("tape after overwrite = %+v, want %+v", got, r3)
	}
}

// TestSaveStateConcurrent models the real deployment: every pane window is a
// separate PROCESS whose persist ticker calls SaveState against the same file
// with no shared in-process mutex. The lock file must serialize the
// read-modify-writes: after all writers finish, every pane's final rect must
// survive (an unserialized merge drops other panes' entries; a torn write
// wipes the whole file).
func TestSaveStateConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "windows.json")
	panes := []Pane{PaneMain, PaneTape, PaneCharts, PaneScan}
	const iters = 25

	var wg sync.WaitGroup
	for i, p := range panes {
		wg.Add(1)
		go func(i int, p Pane) {
			defer wg.Done()
			for k := 0; k <= iters; k++ {
				if err := SaveState(path, p, Rect{X: i * 1000, Y: k, W: 800, H: 600}); err != nil {
					t.Errorf("SaveState(%s, iter %d): %v", p, k, err)
					return
				}
			}
		}(i, p)
	}
	wg.Wait()

	s := LoadStates(path)
	for i, p := range panes {
		want := Rect{X: i * 1000, Y: iters, W: 800, H: 600}
		if got := s[string(p)]; got != want {
			t.Errorf("%s = %+v, want %+v (concurrent SaveState clobbered it)", p, got, want)
		}
	}

	// No droppings: the lock file and every atomic-write temp must be cleaned up.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("leftover file %q after saves (want only windows.json)", e.Name())
		}
	}
}

// TestSaveStateStaleLock: a lock file abandoned by a killed process must be
// broken, not honored forever — SaveState still persists and cleans it up.
func TestSaveStateStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "windows.json")
	lock := path + ".lock"
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	r := Rect{X: 1, Y: 2, W: 300, H: 200}
	start := time.Now()
	if err := SaveState(path, PaneMain, r); err != nil {
		t.Fatalf("SaveState with stale lock: %v", err)
	}
	if d := time.Since(start); d > lockTimeout {
		t.Errorf("SaveState took %v — waited out the timeout instead of breaking the stale lock", d)
	}
	if got := LoadStates(path)["main"]; got != r {
		t.Errorf("main = %+v, want %+v", got, r)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("stale lock %q not cleaned up (stat err=%v)", lock, err)
	}
}

// TestWriteFileAtomicCleansUpTemp: a failed atomic write must not leave temp
// droppings next to the state file.
func TestWriteFileAtomicCleansUpTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "windows.json")
	if err := writeFileAtomic(path, []byte("{}")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "{}" {
		t.Errorf("content = %q, err = %v", b, err)
	}
}

func TestLoadStatesCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "windows.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := LoadStates(path); len(s) != 0 {
		t.Errorf("LoadStates(corrupt) = %v, want empty (shell must still open)", s)
	}
	if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := LoadStates(path); s == nil || len(s) != 0 {
		t.Errorf("LoadStates(null) = %v, want non-nil empty map", s)
	}
}

func TestClampToScreen(t *testing.T) {
	// Single 1920x1080 primary at origin.
	single := Rect{X: 0, Y: 0, W: 1920, H: 1080}
	// Dual: 1920x1080 primary + 1920x1080 secondary to the LEFT (negative x).
	dual := Rect{X: -1920, Y: 0, W: 3840, H: 1080}

	cases := []struct {
		name   string
		in     Rect
		screen Rect
		want   Rect
	}{
		{"fits untouched", Rect{100, 100, 800, 600}, single, Rect{100, 100, 800, 600}},
		{"disconnected monitor right of primary", Rect{2200, 100, 800, 600}, single, Rect{1120, 100, 800, 600}},
		{"disconnected monitor left (negative x now invalid)", Rect{-1500, 100, 800, 600}, single, Rect{0, 100, 800, 600}},
		{"negative x valid on dual layout", Rect{-1500, 100, 800, 600}, dual, Rect{-1500, 100, 800, 600}},
		{"below the screen", Rect{100, 2000, 800, 600}, single, Rect{100, 480, 800, 600}},
		{"above the screen", Rect{100, -700, 800, 600}, single, Rect{100, 0, 800, 600}},
		{"oversized shrinks and pins top-left", Rect{0, 0, 5000, 3000}, single, Rect{0, 0, 1920, 1080}},
		{"degenerate zero rect gets sanity floor", Rect{300, 300, 0, 0}, single, Rect{300, 300, 200, 120}},
		{"partly off right edge slides back", Rect{1800, 900, 800, 600}, single, Rect{1120, 480, 800, 600}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampToScreen(c.in, c.screen); got != c.want {
				t.Errorf("ClampToScreen(%+v, %+v) = %+v, want %+v", c.in, c.screen, got, c.want)
			}
		})
	}
}
