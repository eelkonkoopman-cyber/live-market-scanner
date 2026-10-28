//go:build windows

package main

import "testing"

// The drag loop itself needs a live window and a physical mouse; its math is
// pure and pinned here. The offset must track the cursor delta exactly —
// including negative coordinates (a monitor left of primary) and drags that
// cross the origin.
func TestDragTarget(t *testing.T) {
	cases := []struct {
		name  string
		win   Rect
		start point
		now   point
		wantX int
		wantY int
	}{
		{"no movement", Rect{X: 100, Y: 50, W: 800, H: 600}, point{500, 60}, point{500, 60}, 100, 50},
		{"right and down", Rect{X: 100, Y: 50, W: 800, H: 600}, point{500, 60}, point{560, 200}, 160, 190},
		{"left and up", Rect{X: 100, Y: 50, W: 800, H: 600}, point{500, 60}, point{450, 10}, 50, 0},
		{"onto negative-x monitor", Rect{X: 100, Y: 50, W: 800, H: 600}, point{500, 60}, point{-1400, 60}, -1800, 50},
		{"window already negative", Rect{X: -1920, Y: 0, W: 800, H: 600}, point{-1500, 30}, point{-1400, 130}, -1820, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y := dragTarget(c.win, c.start, c.now)
			if x != c.wantX || y != c.wantY {
				t.Fatalf("dragTarget = (%d,%d), want (%d,%d)", x, y, c.wantX, c.wantY)
			}
		})
	}
}
