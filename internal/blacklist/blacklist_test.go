package blacklist

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPersistAndDayRoll(t *testing.T) {
	store := map[string]string{}
	now := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC) // 11:00 ET
	cfg := Config{
		GetMeta: func(_ context.Context, k string) (string, error) { return store[k], nil },
		SetMeta: func(_ context.Context, k, v string) error { store[k] = v; return nil },
		Now:     func() time.Time { return now },
	}
	l := New(cfg)
	if err := l.Add(" abc "); err != nil {
		t.Fatal(err)
	}
	if err := l.Add("bad sym!"); err == nil {
		t.Fatal("want error for a non-symbol")
	}
	if !l.Has("ABC") {
		t.Fatal("ABC should be listed")
	}
	if store[MetaKey] != `{"day":"2026-10-20","syms":["ABC"]}` {
		t.Fatalf("stored %q", store[MetaKey])
	}
	// A restart the same day keeps it.
	if got := New(cfg).View(); !reflect.DeepEqual(got, []string{"ABC"}) {
		t.Fatalf("reload: %v", got)
	}
	if err := l.Remove("ABC"); err != nil || l.Has("ABC") {
		t.Fatalf("remove: %v", err)
	}
	_ = l.Add("XYZ")
	// 00:00 ET clears it.
	now = time.Date(2026, 10, 21, 4, 1, 0, 0, time.UTC)
	if l.Has("XYZ") || len(l.View()) != 0 {
		t.Fatal("list should clear at the ET day roll")
	}
	if got := New(cfg).View(); len(got) != 0 {
		t.Fatalf("an earlier day's list must be ignored: %v", got)
	}
}
