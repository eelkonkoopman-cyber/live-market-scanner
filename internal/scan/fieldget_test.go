package scan

import (
	"math"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// numGetter is fieldNum's switch resolved ahead of time: for every field name
// (and some that are not fields) it must return exactly fieldNum's value, bit
// for bit, on rows with the engine's Null patterns and the overlay pointers
// both set and unset.
func TestNumGetterMatchesFieldNum(t *testing.T) {
	g := newGoldGen(goldGenConfig{Rows: 600, Seed: 7, Start: goldAt(20, 8, 0, 0)})
	fields := append(append([]string{}, goldFields...), "", "sym", "chart", "bars", "no-such-field")
	avail, zero := int64(12_000), int64(0)
	checked := 0
	for step := 0; step < 30; step++ {
		snap := g.Next(goldAt(20, 8, 0, 0) + int64(step)*90_000)
		for i := range snap.Rows {
			r := &snap.Rows[i]
			switch i % 4 {
			case 1:
				r.ShortAvail, r.ShortFee = &avail, 3.5
			case 2:
				r.ShortAvail, r.ShortFee = &zero, core.Null
			case 3:
				r.NewsAge = i % 900
			}
		}
		for _, f := range fields {
			get := numGetter(f)
			for i := range snap.Rows {
				r := &snap.Rows[i]
				if a, b := get(r), fieldNum(r, f); math.Float64bits(a) != math.Float64bits(b) {
					t.Fatalf("step %d %s field %q: numGetter %v, fieldNum %v", step, r.Sym, f, a, b)
				}
				checked++
			}
		}
	}
	t.Logf("%d field reads compared", checked)
}
