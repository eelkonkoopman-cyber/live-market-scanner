package flagscan

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/emaflag"
)

// The 9 EMA SLAM through the service: emaflag.Result.LastSlamTs / LastSlamPct
// reach the published EmaFlagRef as SlamMs / SlamPct (the scanner's e9SlamAge /
// e9SlamPct), a slam alone is a change worth a publish, and the slam is only
// ever published once its bar has CLOSED (CloseGrace included). The reference
// is SPAI 2026-10-07: one slam, on the 08:15 ET bar (close 4.566, 4.3% under
// its 9 EMA); the 08:16 bar opened under the EMA and is not one.

const spaiFixture = "spai-2026-10-07"

// spaiSlamTs is the 08:15 ET bar's start, the slam's timestamp.
var spaiSlamTs = time.Date(2026, 10, 7, 8, 15, 0, 0, etLocation()).UnixMilli()

// spaiSlamPct is the golden TS engine's lastSlamPct for that bar.
const spaiSlamPct = -4.3252149810545255

func spaiBars(t *testing.T) []core.Bar {
	t.Helper()
	bars := loadFixtures(t)[spaiFixture]
	if len(bars) == 0 {
		t.Fatalf("fixture %s missing or empty", spaiFixture)
	}
	return bars
}

// barsThrough is the fixture's bars up to and including the bar starting at ts.
func barsThrough(bars []core.Bar, ts int64) []core.Bar {
	n := 0
	for n < len(bars) && bars[n].T <= ts {
		n++
	}
	return bars[:n]
}

func TestSlamPublishedForSPAI(t *testing.T) {
	bars := spaiBars(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := emaflag.DefaultParams()
	if !p.Slam {
		t.Fatal("the scan runs the defaults, and the defaults must have the SLAM on")
	}
	const minute = int64(60_000)

	for _, c := range []struct {
		name     string
		bars     []core.Bar
		nowMs    int64 // what the engine sees (the service is handed nowMs + CloseGrace)
		wantSlam bool
	}{
		{"whole morning, all closed", bars, bars[len(bars)-1].T + 2*minute, true},
		{"08:15 still forming (1 ms before its close)", barsThrough(bars, spaiSlamTs), spaiSlamTs + minute - 1, false},
		{"08:15 just closed", barsThrough(bars, spaiSlamTs), spaiSlamTs + minute, true},
		{"through 08:14 only", barsThrough(bars, spaiSlamTs-minute), spaiSlamTs + minute, false},
		{"08:16 closed: still the 08:15 slam", barsThrough(bars, spaiSlamTs+minute), spaiSlamTs + 2*minute, true},
	} {
		ref, ok := verdictViaService(t, ctx, p, "SPAI", c.bars, c.nowMs)
		if !ok {
			t.Fatalf("%s: no verdict published", c.name)
		}
		if !c.wantSlam {
			if ref.SlamMs != 0 {
				t.Errorf("%s: SlamMs %d, want 0 (no slam yet)", c.name, ref.SlamMs)
			}
			continue
		}
		if ref.SlamMs != spaiSlamTs {
			t.Errorf("%s: SlamMs %d, want the 08:15 ET bar %d", c.name, ref.SlamMs, spaiSlamTs)
		}
		if !near(ref.SlamPct, spaiSlamPct) || !(ref.SlamPct < -3) {
			t.Errorf("%s: SlamPct %v, want %v (negative: under the EMA)", c.name, ref.SlamPct, spaiSlamPct)
		}
	}

	// With the SLAM switched off nothing else changes: same state, same levels,
	// and no slam on the ref.
	off := p
	off.Slam = false
	on, _ := verdictViaService(t, ctx, p, "SPAI", bars, bars[len(bars)-1].T+2*minute)
	got, ok := verdictViaService(t, ctx, off, "SPAI", bars, bars[len(bars)-1].T+2*minute)
	if !ok || got.SlamMs != 0 {
		t.Fatalf("slam off: verdict %v ok %v, want no slam", got, ok)
	}
	got.SlamMs, got.SlamPct = on.SlamMs, on.SlamPct
	if !sameRef(got, on) {
		t.Fatalf("switching the SLAM off changed the flag verdict: %+v vs %+v", got, on)
	}
}

// refOf copies the slam as is; sameRef sees a change in either slam field (so a
// slam that changes nothing else is still published) and, like the levels,
// treats NaN == NaN.
func TestRefOfAndSameRefCarryTheSlam(t *testing.T) {
	res := emaflag.Result{
		State: emaflag.StateIdle, EmaDistPct: -2, Trigger: math.NaN(), Stop: math.NaN(),
		LastSlamTs: spaiSlamTs, LastSlamClose: 4.566, LastSlamPct: spaiSlamPct,
	}
	ref := refOf(res)
	if ref.SlamMs != spaiSlamTs || ref.SlamPct != spaiSlamPct {
		t.Fatalf("refOf: SlamMs %d SlamPct %v", ref.SlamMs, ref.SlamPct)
	}
	if !sameRef(ref, refOf(res)) {
		t.Fatal("a ref must equal itself")
	}
	ts := ref
	ts.SlamMs += 60_000
	pct := ref
	pct.SlamPct = -5
	none := ref
	none.SlamMs, none.SlamPct = 0, math.NaN()
	for name, other := range map[string]core.EmaFlagRef{"slam time": ts, "slam pct": pct, "no slam": none} {
		if sameRef(ref, other) || sameRef(other, ref) {
			t.Errorf("%s differs: sameRef must say so (a slam must be published)", name)
		}
	}
	if a, b := none, none; !sameRef(a, b) {
		t.Error("NaN SlamPct on both sides is the same ref")
	}
}

// Live, second by second: REST serves SPAI through its 08:15 bar at 08:15:30
// (the bar still forming), and the clock then ticks on with no newer bar. The
// SLAM must appear in the published map - and on the OnEval update that
// produced it - exactly when the engine's clock (now - CloseGrace) reaches the
// bar's close: 08:16:03 wall time, never before.
func TestSlamPublishedOnlyOnceItsBarClosed(t *testing.T) {
	bars := spaiBars(t)
	e := newEnv(t)
	e.setCands(core.PmVolEntry{Sym: "SPAI", PmVolX: 2})
	e.setBars(func(context.Context, string) ([]core.Bar, error) { return barsThrough(bars, spaiSlamTs), nil })
	ups := &collector{}
	cfg := e.config()
	cfg.OnEval = ups.add
	s := e.serviceCfg(cfg, defaultTuning())

	start := time.UnixMilli(spaiSlamTs).Add(30 * time.Second) // 08:15:30 ET
	closeAt := time.UnixMilli(spaiSlamTs + minuteMs).Add(CloseGrace)
	var firstSlam time.Time
	for now := start; !now.After(closeAt.Add(3 * time.Second)); now = now.Add(time.Second) {
		e.step(s, now)
		pub, ok := e.lastPub()["SPAI"]
		if !ok {
			t.Fatalf("%s: no verdict for SPAI", now.In(etLocation()).Format("15:04:05"))
		}
		for _, u := range ups.take() {
			if u.Result.LastSlamTs != pub.SlamMs {
				t.Fatalf("%s: OnEval LastSlamTs %d, published SlamMs %d", now.In(etLocation()).Format("15:04:05"), u.Result.LastSlamTs, pub.SlamMs)
			}
		}
		if pub.SlamMs != 0 && firstSlam.IsZero() {
			firstSlam = now
			if pub.SlamMs != spaiSlamTs || !near(pub.SlamPct, spaiSlamPct) {
				t.Fatalf("published slam %d / %v, want %d / %v", pub.SlamMs, pub.SlamPct, spaiSlamTs, spaiSlamPct)
			}
		}
	}
	if !firstSlam.Equal(closeAt) {
		t.Fatalf("the SLAM was first published at %s, want %s (the 08:15 bar's close + CloseGrace)",
			firstSlam.In(etLocation()).Format("15:04:05"), closeAt.In(etLocation()).Format("15:04:05"))
	}
}
