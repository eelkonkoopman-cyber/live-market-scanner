package main

import (
	"context"
	"errors"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// twsHarness is an in-memory scan store + schema_meta for the one-time
// "Send to TWS" migration of the Green Dildo scan.
type twsHarness struct {
	meta      map[string]string
	metaErr   error
	listErr   error
	saveErr   error
	defs      []core.ScanDef
	saves     int
	installed []core.ScanDef
}

func (h *twsHarness) run() {
	migrateDildoSendToTWS(context.Background(),
		func(_ context.Context, k string) (string, error) { return h.meta[k], h.metaErr },
		func(_ context.Context, k, v string) error { h.meta[k] = v; return nil },
		func(context.Context) ([]core.ScanDef, error) { return h.defs, h.listErr },
		func(_ context.Context, d core.ScanDef) error {
			if h.saveErr != nil {
				return h.saveErr
			}
			h.saves++
			for i := range h.defs {
				if h.defs[i].ID == d.ID {
					h.defs[i] = d
				}
			}
			return nil
		},
		func(d core.ScanDef) { h.installed = append(h.installed, d) })
}

func (h *twsHarness) dildo() (core.ScanDef, bool) {
	for _, d := range h.defs {
		if d.ID == dildo1mID {
			return d, true
		}
	}
	return core.ScanDef{}, false
}

func TestDildoScanSeedsWithSendToTWSOn(t *testing.T) {
	for _, d := range dildoScans() {
		if want := d.ID == dildo1mID; d.Alert.SendToTWS != want {
			t.Fatalf("%s SendToTWS = %v, want %v", d.ID, d.Alert.SendToTWS, want)
		}
	}
	if dildoTWSKey != "seeded.dildo.tws.v1" || dildoTWSKey == dildoSeedKey {
		t.Fatalf("migration key %q", dildoTWSKey)
	}
}

func TestMigrateDildoSendToTWSOnceKeepingUserEdits(t *testing.T) {
	seeded := dildoScans()[0]
	// The user's own tuning since it was seeded.
	seeded.Alert.SendToTWS = false
	seeded.Alert.Sound = "ping"
	seeded.Alert.Speak = false
	seeded.Alert.CooldownSec = 90
	seeded.Filter.Conds[1].Value = 1.5
	seeded.Name = "My Dildo"
	h := &twsHarness{meta: map[string]string{}, defs: []core.ScanDef{seeded, {ID: "other", Name: "other"}}}
	h.run()

	got, _ := h.dildo()
	want := seeded
	want.Alert.SendToTWS = true
	if got.Alert != want.Alert || got.Name != "My Dildo" || got.Filter.Conds[1].Value != 1.5 {
		t.Fatalf("migrated scan %+v, want only SendToTWS changed from %+v", got, seeded)
	}
	if h.saves != 1 || len(h.installed) != 1 || !h.installed[0].Alert.SendToTWS || h.meta[dildoTWSKey] != "1" {
		t.Fatalf("saves %d installed %v meta %v", h.saves, h.installed, h.meta)
	}
	if o := h.defs[1]; o.Alert.SendToTWS {
		t.Fatal("another scan was touched")
	}

	// The user turns it off again; a later boot must not switch it back on.
	h.defs[0].Alert.SendToTWS = false
	h.saves, h.installed = 0, nil
	h.run()
	if d, _ := h.dildo(); d.Alert.SendToTWS || h.saves != 0 || len(h.installed) != 0 {
		t.Fatalf("re-ran after its key was set: %+v saves %d", d, h.saves)
	}
}

func TestMigrateDildoSendToTWSSkipsADeletedScanButSetsTheKey(t *testing.T) {
	h := &twsHarness{meta: map[string]string{}, defs: []core.ScanDef{{ID: "other"}}}
	h.run()
	if _, ok := h.dildo(); ok || h.saves != 0 || len(h.installed) != 0 {
		t.Fatalf("resurrected a deleted scan: %+v", h.defs)
	}
	if h.meta[dildoTWSKey] != "1" {
		t.Fatal("key must be set so a scan the user adds back later is left alone")
	}
}

func TestMigrateDildoSendToTWSAlreadyOnJustSetsTheKey(t *testing.T) {
	h := &twsHarness{meta: map[string]string{}, defs: dildoScans()} // fresh install: seeded with it on
	h.run()
	if h.saves != 0 || len(h.installed) != 0 || h.meta[dildoTWSKey] != "1" {
		t.Fatalf("saves %d installed %v meta %v", h.saves, h.installed, h.meta)
	}
}

func TestMigrateDildoSendToTWSRetriesAfterFailures(t *testing.T) {
	seeded := dildoScans()[0]
	seeded.Alert.SendToTWS = false
	h := &twsHarness{meta: map[string]string{}, defs: []core.ScanDef{seeded}, saveErr: errors.New("disk full")}
	h.run()
	if h.meta[dildoTWSKey] != "" || len(h.installed) != 0 {
		t.Fatalf("a failed save must leave the key unset and install nothing: %v %v", h.meta, h.installed)
	}
	h.saveErr = nil
	h.run() // next boot
	if d, _ := h.dildo(); !d.Alert.SendToTWS || h.meta[dildoTWSKey] != "1" {
		t.Fatalf("retry did not migrate: %+v %v", d, h.meta)
	}

	h = &twsHarness{meta: map[string]string{}, defs: []core.ScanDef{seeded}, listErr: errors.New("db locked")}
	h.run()
	if h.meta[dildoTWSKey] != "" || h.saves != 0 {
		t.Fatalf("an unreadable scan list must change nothing: %v", h.meta)
	}
	h = &twsHarness{meta: map[string]string{}, metaErr: errors.New("db locked"), defs: []core.ScanDef{seeded}}
	h.run()
	if h.saves != 0 {
		t.Fatal("an unreadable meta store must change nothing")
	}
}
