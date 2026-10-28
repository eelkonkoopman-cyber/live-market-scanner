package core

import "testing"

func TestBaselineWithReplacesOnlyListedTickers(t *testing.T) {
	row := func(v float64, n uint16) ([BucketCount]float64, [BucketCount]uint16) {
		var c [BucketCount]float64
		var k [BucketCount]uint16
		c[1], k[1] = v, n
		return c, k
	}
	b := &BaselineTable{Cum: map[string][BucketCount]float64{}, Count: map[string][BucketCount]uint16{}}
	b.Cum["KEEP"], b.Count["KEEP"] = row(100, 5)
	b.Cum["OLD"], b.Count["OLD"] = row(200, 5)
	b.Cum["GONE"], b.Count["GONE"] = row(300, 5)
	part := &BaselineTable{Cum: map[string][BucketCount]float64{}, Count: map[string][BucketCount]uint16{}}
	part.Cum["OLD"], part.Count["OLD"] = row(250, 7)
	part.Cum["NEW"], part.Count["NEW"] = row(400, 6)

	got := b.With(part, []string{"OLD", "NEW", "GONE"})
	if v, _ := got.Get("KEEP", 1); v != 100 {
		t.Errorf("unlisted ticker untouched: %v", v)
	}
	if v, _ := got.Get("OLD", 1); v != 250 || got.CountAt("OLD", 1) != 7 {
		t.Errorf("listed ticker replaced: %v n=%d", v, got.CountAt("OLD", 1))
	}
	if v, _ := got.Get("NEW", 1); v != 400 {
		t.Errorf("listed ticker added: %v", v)
	}
	if _, ok := got.Get("GONE", 1); ok {
		t.Error("a listed ticker with no rows left is dropped")
	}
	if v, _ := b.Get("OLD", 1); v != 200 {
		t.Error("the original table must not change: the engine reads it concurrently")
	}
	if v, _ := (*BaselineTable)(nil).With(part, []string{"NEW"}).Get("NEW", 1); v != 400 {
		t.Error("a nil table counts as empty")
	}
}
