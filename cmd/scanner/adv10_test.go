package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRunAdv10LoadsBeforeTodayETAndApplies(t *testing.T) {
	adv10First, adv10Every = time.Millisecond, time.Millisecond
	t.Cleanup(func() { adv10First, adv10Every = 15*time.Second, 5*time.Minute })

	ny, _ := time.LoadLocation("America/New_York")
	// 02:30 on Oct 6 in the UK is still Oct 5 in New York: "before" must be the ET date.
	now := func() time.Time { return time.Date(2026, 10, 5, 22, 30, 0, 0, ny) }
	var mu sync.Mutex
	var gotBefore string
	var gotDays, gotMin int
	applied := make(chan map[string]float64, 4)
	load := func(_ context.Context, before string, days, minDays int) (map[string]float64, error) {
		mu.Lock()
		gotBefore, gotDays, gotMin = before, days, minDays
		mu.Unlock()
		return map[string]float64{"AAA": 1e6}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runAdv10(ctx, load, func(m map[string]float64) { applied <- m }, now) }()
	select {
	case m := <-applied:
		if m["AAA"] != 1e6 {
			t.Fatalf("applied %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loader never applied")
	}
	select { // and it keeps re-applying (reaches tickers created later)
	case <-applied:
	case <-time.After(2 * time.Second):
		t.Fatal("loader must re-apply periodically")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotBefore != "2026-10-05" || gotDays != 10 || gotMin != 5 {
		t.Fatalf("load(%q, %d, %d), want (2026-10-05, 10, 5)", gotBefore, gotDays, gotMin)
	}
}
