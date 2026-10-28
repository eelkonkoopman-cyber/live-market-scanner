package dildopb

import (
	"bytes"
	"encoding/json"
	"math"
	"sync/atomic"
)

// The live parameter set: the Signal window's GREEN DILDO tuning panel is the
// single source of truth for the market-wide alert too. The scanner persists the
// panel's JSON (schema_meta dildo.params.v1), Parse validates it (the same rules
// as normalizeDildoParams in web/src/signalLayout.ts, field by field: a bad or
// missing field falls back to its default), and SetLive publishes it. The
// engine's per-bar judge reads Live() ONCE per bar - one atomic pointer load, a
// small struct copy, no allocation.

// MetaKey is the schema_meta key the tuning is persisted under.
const MetaKey = "dildo.params.v1"

var live atomic.Pointer[Params]

// Live returns the current parameters (DefaultParams until SetLive is called).
func Live() Params {
	if p := live.Load(); p != nil {
		return *p
	}
	return DefaultParams()
}

// SetLive publishes p as the live parameter set (callers pass a Parse result).
func SetLive(p Params) { live.Store(&p) }

// ResetLive returns to the defaults (tests).
func ResetLive() { live.Store(nil) }

// Parse reads the panel's JSON over the defaults. Unknown fields are ignored; a
// field of the wrong type, non-finite, negative, non-whole where a count is
// meant, below 1 where a length is meant, above 100 for maxTopPct, or an
// unknown floor keeps its default. Malformed JSON gives the defaults and false.
func Parse(raw []byte) (Params, bool) {
	p := DefaultParams()
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return p, true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return p, false
	}
	num := func(key string, dst *float64, whole bool, min, max float64) {
		r, ok := m[key]
		if !ok {
			return
		}
		var v float64
		if json.Unmarshal(r, &v) != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < min || v > max {
			return
		}
		if whole && v != math.Trunc(v) {
			return
		}
		*dst = v
	}
	count := func(key string, dst *int, min float64) {
		f := float64(*dst)
		num(key, &f, true, min, 1e6)
		*dst = int(f)
	}
	if r, ok := m["enabled"]; ok {
		var b bool
		if json.Unmarshal(r, &b) == nil {
			p.Enabled = b
		}
	}
	inf := math.Inf(1)
	num("minBodyPct", &p.MinBodyPct, false, 0, inf)
	num("minBodyX", &p.MinBodyX, false, 0, inf)
	num("maxTopPct", &p.MaxTopPct, false, 0, 100)
	num("minVolX", &p.MinVolX, false, 0, inf)
	count("priorBars", &p.PriorBars, 1)
	count("minPriorBars", &p.MinPriorBars, 0)
	num("minPrice", &p.MinPrice, false, 0, inf)
	num("minDollarVol", &p.MinDollarVol, false, 0, inf)
	count("pbWindowMin", &p.PbWindowMin, 1)
	num("touchPct", &p.TouchPct, false, 0, inf)
	num("holdPct", &p.HoldPct, false, 0, inf)
	count("emaPeriod", &p.EmaPeriod, 1)
	if r, ok := m["floor"]; ok {
		var s string
		if json.Unmarshal(r, &s) == nil && (s == FloorMid || s == FloorOpen) {
			p.Floor = s
		}
	}
	return p, true
}

// wire is Params with the panel's camelCase keys.
type wire struct {
	Enabled      bool    `json:"enabled"`
	MinBodyPct   float64 `json:"minBodyPct"`
	MinBodyX     float64 `json:"minBodyX"`
	MaxTopPct    float64 `json:"maxTopPct"`
	MinVolX      float64 `json:"minVolX"`
	PriorBars    int     `json:"priorBars"`
	MinPriorBars int     `json:"minPriorBars"`
	MinPrice     float64 `json:"minPrice"`
	MinDollarVol float64 `json:"minDollarVol"`
	PbWindowMin  int     `json:"pbWindowMin"`
	TouchPct     float64 `json:"touchPct"`
	HoldPct      float64 `json:"holdPct"`
	Floor        string  `json:"floor"`
	EmaPeriod    int     `json:"emaPeriod"`
}

// Marshal is the panel's JSON for p (the shape Parse reads back).
func Marshal(p Params) []byte {
	b, _ := json.Marshal(wire(p))
	return b
}
