package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// iflagSessionBars builds a session series shaped like engine.SessionBars
// output: 30 five-minute buckets — a 22-bucket ramp 10.00 → 11.20 on a million
// shares each (a 12% pole, which clears the intraday 10% gate but NOT the
// daily 20% gate, so a pass under the wrong preset fails loudly), then an
// 8-bucket coil 10.90-11.20 on 400k. Dates carry the engine's real
// "2006-01-02 15:04" bucket stamps so the AsOf assertion is end-to-end.
func iflagSessionBars() []core.DailyBar {
	closes := make([]float64, 0, 30)
	for i := 0; i < 22; i++ {
		closes = append(closes, 10+1.2*float64(i)/21)
	}
	closes = append(closes, 11.2, 10.9, 11.2, 10.9, 11.2, 10.9, 11.2, 11.05)
	out := make([]core.DailyBar, len(closes))
	base := time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)
	prev := closes[0]
	for i, c := range closes {
		v := int64(1_000_000)
		if i >= 22 {
			v = 400_000
		}
		hi, lo := c, c
		if prev > hi {
			hi = prev
		}
		if prev < lo {
			lo = prev
		}
		out[i] = core.DailyBar{
			Date: base.Add(time.Duration(i) * 5 * time.Minute).Format("2006-01-02 15:04"),
			O:    prev, H: hi, L: lo, C: c, V: v,
		}
		prev = c
	}
	return out
}

// iflagChopBars is too short a history for any verdict (19 buckets < the
// 20-bucket MinBars gate) — a symbol the pass must silently drop, not zero.
func iflagChopBars() []core.DailyBar {
	out := make([]core.DailyBar, 19)
	base := time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)
	for i := range out {
		out[i] = core.DailyBar{
			Date: base.Add(time.Duration(i) * 5 * time.Minute).Format("2006-01-02 15:04"),
			O:    10, H: 10, L: 10, C: 10, V: 1_000_000,
		}
	}
	return out
}

// startIFlags runs runIntradayFlags on a fast ticker with a channel-backed
// publish, cancelled at cleanup; done closes when the loop returns.
func startIFlags(t *testing.T, bars func() map[string][]core.DailyBar) (pubs chan map[string]core.FlagRef, cancel context.CancelFunc, done chan error) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	t.Cleanup(c)
	pubs = make(chan map[string]core.FlagRef, 16)
	done = make(chan error, 1)
	go func() {
		done <- runIntradayFlags(ctx, 10*time.Millisecond, bars, func(m map[string]core.FlagRef) {
			select {
			case pubs <- m:
			default: // test behind; later cycles republish the same picture
			}
		})
	}()
	return pubs, c, done
}

func recvPublish(t *testing.T, pubs chan map[string]core.FlagRef) map[string]core.FlagRef {
	t.Helper()
	select {
	case m := <-pubs:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no publish within 2s")
		return nil
	}
}

// TestIntradayFlagsPublishesOnlyOK proves a cycle keeps exactly the symbols
// pattern accepted under IntradayParams: the coiling 12% pole is published
// (with the geometry the overlay needs — State 1, BaseHigh, AsOf from the
// last bucket's stamp) and the too-short symbol is absent, not zeroed.
func TestIntradayFlagsPublishesOnlyOK(t *testing.T) {
	coil := iflagSessionBars()
	pubs, _, _ := startIFlags(t, func() map[string][]core.DailyBar {
		return map[string][]core.DailyBar{"COIL": coil, "CHOP": iflagChopBars()}
	})

	m := recvPublish(t, pubs)
	if len(m) != 1 {
		t.Fatalf("want exactly the ok symbol, got %v", m)
	}
	ref, ok := m["COIL"]
	if !ok {
		t.Fatalf("COIL missing from publish: %v", m)
	}
	if ref.State != 1 {
		t.Fatalf("a coiling setup publishes State 1, got %v (%+v)", ref.State, ref)
	}
	if ref.BaseHigh != 11.2 {
		t.Fatalf("BaseHigh must be the coil's high for the live-breakout compare, got %v", ref.BaseHigh)
	}
	if want := coil[len(coil)-1].Date; ref.AsOf != want {
		t.Fatalf("AsOf must be the last bucket's stamp %q, got %q", want, ref.AsOf)
	}
	if _, ok := m["CHOP"]; ok {
		t.Fatal("a rejected symbol must be ABSENT from the publish, never present with zeroes")
	}
}

// TestIntradayFlagsAlwaysPublishesEmpty is the reset-wipe guarantee: when the
// engine has nothing (empty session after reset), the cycle must still publish
// a NON-NIL empty map — SetIntradayFlags drops nil, and only a published empty
// map makes the overlay stamp Null over yesterday's stale intraday flags.
func TestIntradayFlagsAlwaysPublishesEmpty(t *testing.T) {
	pubs, _, _ := startIFlags(t, func() map[string][]core.DailyBar {
		return map[string][]core.DailyBar{}
	})
	m := recvPublish(t, pubs)
	if m == nil {
		t.Fatal("empty cycle must publish a non-nil map (nil is a no-op in SetIntradayFlags)")
	}
	if len(m) != 0 {
		t.Fatalf("want an empty publish, got %v", m)
	}
}

// TestIntradayFlagsSymbolDisappears proves each cycle is built fresh from the
// current bars: a symbol the engine stops returning (gated out, or the session
// reset) vanishes from the very next publish rather than lingering.
func TestIntradayFlagsSymbolDisappears(t *testing.T) {
	var calls atomic.Int64
	pubs, _, _ := startIFlags(t, func() map[string][]core.DailyBar {
		if calls.Add(1) == 1 {
			return map[string][]core.DailyBar{"COIL": iflagSessionBars()}
		}
		return map[string][]core.DailyBar{}
	})

	first := recvPublish(t, pubs)
	if _, ok := first["COIL"]; !ok {
		t.Fatalf("first cycle must publish COIL, got %v", first)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		m := recvPublish(t, pubs)
		if _, ok := m["COIL"]; !ok {
			if m == nil || len(m) != 0 {
				t.Fatalf("post-disappearance publish must be the non-nil empty map, got %#v", m)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("COIL never disappeared from subsequent publishes")
		}
	}
}

// TestIntradayFlagsStopsOnCancel proves the loop exits promptly and cleanly
// (nil error, so start() doesn't log a component failure) on ctx cancel.
func TestIntradayFlagsStopsOnCancel(t *testing.T) {
	pubs, cancel, done := startIFlags(t, func() map[string][]core.DailyBar {
		return map[string][]core.DailyBar{}
	})
	recvPublish(t, pubs) // loop is demonstrably running
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runIntradayFlags must return nil on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runIntradayFlags did not stop on ctx cancel")
	}
}
