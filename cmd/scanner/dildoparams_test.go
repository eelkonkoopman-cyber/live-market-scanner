package main

import (
	"context"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
)

func TestDildoTuningPersistAndLive(t *testing.T) {
	t.Cleanup(dildopb.ResetLive)
	store := map[string]string{}
	d := dildoTuning{
		get: func(_ context.Context, k string) (string, error) { return store[k], nil },
		set: func(_ context.Context, k, v string) error { store[k] = v; return nil },
	}
	ctx := context.Background()
	d.Load(ctx) // key absent -> defaults
	if dildopb.Live() != dildopb.DefaultParams() {
		t.Fatal("absent key must give the defaults")
	}
	out, err := d.Set(ctx, []byte(`{"minBodyPct":11,"floor":"open","minVolX":-3,"junk":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want := dildopb.DefaultParams()
	want.MinBodyPct, want.Floor = 11, dildopb.FloorOpen
	if dildopb.Live() != want {
		t.Fatalf("live %+v want %+v", dildopb.Live(), want)
	}
	if store[dildopb.MetaKey] != string(out) {
		t.Fatal("not persisted")
	}
	if o, _ := d.Set(ctx, []byte("{bad")); o != nil || dildopb.Live() != want {
		t.Fatal("malformed set must be ignored")
	}
	dildopb.ResetLive() // a restart
	d.Load(ctx)
	if dildopb.Live() != want {
		t.Fatalf("restart lost the tuning: %+v", dildopb.Live())
	}
	store[dildopb.MetaKey] = "garbage"
	d.Load(ctx)
	if dildopb.Live() != dildopb.DefaultParams() {
		t.Fatal("garbage must fall back to the defaults")
	}
}
