package orderticket

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func entryMods(r *rig, id int64) []placed {
	var out []placed
	for i, x := range r.f.placesCopy() {
		if i > 1 && x.id == id { // [0],[1] are the original parent + TP
			out = append(out, x)
		}
	}
	return out
}

func echo(r *rig, id int64, lim float64) {
	r.f.w.OpenOrder(id, contractFor("SGRX"), &Order{Action: "BUY", OrderType: "LMT", TotalQuantity: 666, LmtPrice: lim,
		TIF: "DAY", OrderRef: refPrefix + "SGRX", ClientID: 80}, &OrderState{Status: "Submitted"})
}

func TestFollowEvery5sLiveOnlyOnTickChange(t *testing.T) {
	r := newRig(t, nil)
	r.ready("SGRX", "111", 1.50)
	pid, cid, _ := placeOrder(t, r, "BUY")
	if v := view(t, r, pid); v.FollowLabel != "follows 9EMA live · every 5 s" {
		t.Fatalf("%q", v.FollowLabel)
	}
	ctx := context.Background()
	price(r, 1.70) // forming candle: live EMA 1.5 x 0.8 + 1.7 x 0.2 = 1.54
	r.advance(2 * time.Second)
	r.tk.Tick(ctx)
	if len(entryMods(r, pid)) != 0 {
		t.Fatal("re-priced before followSec")
	}
	r.advance(3 * time.Second)
	r.tk.Tick(ctx)
	m := entryMods(r, pid)
	if len(m) != 1 || !near(m[0].o.LmtPrice, 1.54) {
		t.Fatalf("mods %+v", m)
	}
	var tp []placed
	for _, x := range r.f.placesCopy()[2:] {
		if x.id == cid {
			tp = append(tp, x)
		}
	}
	if len(tp) != 1 || !near(tp[0].o.LmtPrice, 1.70) { // 1.54 x 1.10 = 1.694 -> up
		t.Fatalf("TP did not follow %+v", tp)
	}
	echo(r, pid, 1.54)
	price(r, 1.705) // EMA 1.541: same tick-rounded limit
	r.advance(5 * time.Second)
	r.tk.Tick(ctx)
	if len(entryMods(r, pid)) != 1 {
		t.Fatal("modified for a sub-tick change")
	}
	// filled: no more modifies
	r.f.w.OrderStatus(pid, "Filled", 666, 0, 1.54, 9100, 0, 1.54, 80, "", 0)
	price(r, 1.95)
	r.advance(5 * time.Second)
	r.tk.Tick(ctx)
	if len(entryMods(r, pid)) != 1 {
		t.Fatal("modified after the fill")
	}
}

func TestFollowNoOverlappingModifies(t *testing.T) {
	r := newRig(t, nil)
	if err := r.tk.SetFollowSec(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	r.ready("SGRX", "111", 1.50)
	pid, _, _ := placeOrder(t, r, "BUY")
	ctx := context.Background()
	price(r, 1.70)
	r.advance(2 * time.Second)
	r.tk.Tick(ctx) // modify #1, not acknowledged
	price(r, 1.90)
	r.advance(2 * time.Second)
	r.tk.Tick(ctx) // 2 s later: previous modify still in flight -> wait
	if n := len(entryMods(r, pid)); n != 1 {
		t.Fatalf("overlapping modifies: %d", n)
	}
	r.advance(2 * time.Second)
	r.tk.Tick(ctx) // 4 s after #1: the 3 s wait is over
	if n := len(entryMods(r, pid)); n != 2 {
		t.Fatalf("after 3 s: %d", n)
	}
	echo(r, pid, entryMods(r, pid)[1].o.LmtPrice)
	price(r, 2.20)
	r.advance(2 * time.Second)
	r.tk.Tick(ctx) // acknowledged: next interval goes ahead
	if n := len(entryMods(r, pid)); n != 3 {
		t.Fatalf("after echo: %d", n)
	}
}

func TestFollowClosedModeIgnoresFormingCandle(t *testing.T) {
	r := newRig(t, nil)
	r.tk.settings.EMALive = false
	r.ready("SGRX", "111", 1.50)
	pid, _, _ := placeOrder(t, r, "BUY")
	if v := view(t, r, pid); v.FollowLabel != "follows 9EMA closed · every 5 s" {
		t.Fatalf("%q", v.FollowLabel)
	}
	price(r, 1.70)
	r.advance(5 * time.Second)
	r.tk.Tick(context.Background())
	if len(entryMods(r, pid)) != 0 {
		t.Fatal("closed-mode order followed the forming candle")
	}
	// the order keeps ITS mode even if the setting changes
	_ = r.tk.SetEMALive(context.Background(), true)
	r.advance(5 * time.Second)
	r.tk.Tick(context.Background())
	if len(entryMods(r, pid)) != 0 {
		t.Fatal("order switched mode with the setting")
	}
}

func TestSettingsMigrationAndFollowSec(t *testing.T) {
	load := func(saved Settings) (Settings, map[string]string) {
		b, _ := json.Marshal(saved)
		meta := map[string]string{MetaKey: string(b)}
		tk, err := New(Config{Audit: &MemAudit{},
			GetMeta: func(_ context.Context, k string) (string, error) { return meta[k], nil },
			SetMeta: func(_ context.Context, k, v string) error { meta[k] = v; return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return tk.settings, meta
	}
	old := Settings{Host: "127.0.0.1", Port: 7496, ClientID: 80, MaxPositionUSD: 5000} // pre-rev install, closed mode
	s, meta := load(old)
	if !s.EMALive || s.Rev != settingsRev || s.FollowSec != 5 {
		t.Fatalf("not migrated %+v", s)
	}
	var saved Settings
	_ = json.Unmarshal([]byte(meta[MetaKey]), &saved)
	if !saved.EMALive || saved.Rev != settingsRev {
		t.Fatalf("migration not persisted %s", meta[MetaKey])
	}
	// once: a user who then picks closed keeps it
	saved.EMALive, saved.EMALiveSet = false, true
	if s, _ = load(saved); s.EMALive {
		t.Fatal("explicit closed choice overridden")
	}
	// an explicit pre-rev choice is respected too
	old.EMALiveSet = true
	if s, _ = load(old); s.EMALive {
		t.Fatal("explicit pre-rev choice overridden")
	}
	if d := DefaultSettings(); !d.EMALive || d.FollowSec != 5 {
		t.Fatalf("defaults %+v", d)
	}
	r := newRig(t, nil)
	for _, bad := range []int{1, 61} {
		if err := r.tk.SetFollowSec(context.Background(), bad); err == nil {
			t.Fatalf("%d accepted", bad)
		}
	}
	if err := r.tk.SetFollowSec(context.Background(), 60); err != nil {
		t.Fatal(err)
	}
}
