package main

import (
	"context"

	"github.com/eelkonkoopman/marketscanner/internal/dildopb"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// dildoTuning is the persisted, market-wide GREEN DILDO tuning (schema_meta
// dildopb.MetaKey): the Signal window's panel is its only editor. Load runs
// once at startup, before the engine judges a bar; Set is the hub's
// dildo.params.set. Only alerts, the chart and HOT admission follow it.
type dildoTuning struct {
	get func(ctx context.Context, key string) (string, error)
	set func(ctx context.Context, key, v string) error
}

// Load publishes the stored tuning (defaults when the key is absent or
// unreadable; bad fields fall back one by one) and returns it as JSON.
func (d dildoTuning) Load(ctx context.Context) []byte {
	p := dildopb.DefaultParams()
	if v, err := d.get(ctx, dildopb.MetaKey); err != nil {
		logx.L().Warn("dildo params: load failed; using the defaults", "err", err)
	} else if v != "" {
		var ok bool
		if p, ok = dildopb.Parse([]byte(v)); !ok {
			logx.L().Warn("dildo params: stored value is not valid JSON; using the defaults")
		}
	}
	dildopb.SetLive(p)
	return dildopb.Marshal(p)
}

// Get is the live set as JSON.
func (d dildoTuning) Get() []byte { return dildopb.Marshal(dildopb.Live()) }

// Set validates raw over the defaults, applies it live (even if persisting then
// fails, which is returned) and returns the set that took effect.
func (d dildoTuning) Set(ctx context.Context, raw []byte) ([]byte, error) {
	p, ok := dildopb.Parse(raw)
	if !ok {
		return nil, nil // malformed frame: ignore, keep what is live
	}
	dildopb.SetLive(p)
	out := dildopb.Marshal(p)
	return out, d.set(ctx, dildopb.MetaKey, string(out))
}
