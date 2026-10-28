package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// Speak is persisted with the scan (scan_defs.def_json) and travels on the
// scan.upsert wire message. Two properties matter: a scan stored before the
// field existed decodes as silent (false), and a silent scan's JSON is
// byte-identical to what it was before the field was added (omitempty), so
// nothing that compares or diffs stored definitions sees a spurious change.
func TestAlertConfigSpeakJSON(t *testing.T) {
	silent, err := json.Marshal(AlertConfig{Enabled: true, On: AlertOnEnter, Sound: "ping", CooldownSec: 300})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(silent), "speak") {
		t.Fatalf("a silent alert must not serialise the speak key (omitempty): %s", silent)
	}

	loud, err := json.Marshal(AlertConfig{Enabled: true, On: AlertOnEnter, Sound: "ping", Speak: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loud), `"speak":true`) {
		t.Fatalf("a speaking alert must serialise speak:true: %s", loud)
	}

	var back AlertConfig
	if err := json.Unmarshal(loud, &back); err != nil || !back.Speak {
		t.Fatalf("speak must round-trip: err=%v got %+v", err, back)
	}

	// A definition stored before Speak existed has no such key: it must decode
	// as false, never as an error.
	var old AlertConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"on":"enter","sound":"ping","cooldownSec":60,"requireReentry":false}`), &old); err != nil {
		t.Fatalf("pre-Speak JSON must still decode: %v", err)
	}
	if old.Speak {
		t.Fatal("a scan stored without the speak key must stay silent")
	}
}

// SendToTWS rides the same JSON: off stores nothing (existing scans keep their
// stored bytes), on round-trips under "sendToTws", and a definition from before
// it existed decodes as off.
func TestAlertConfigSendToTWSJSON(t *testing.T) {
	quiet, _ := json.Marshal(AlertConfig{Enabled: true, On: AlertOnEnter, Sound: "ping"})
	if strings.Contains(string(quiet), "sendToTws") {
		t.Fatalf("off must be omitted: %s", quiet)
	}
	on, _ := json.Marshal(AlertConfig{Enabled: true, On: AlertOnEnter, Sound: "ping", SendToTWS: true})
	if !strings.Contains(string(on), `"sendToTws":true`) {
		t.Fatalf("on must be written as sendToTws: %s", on)
	}
	var back AlertConfig
	if err := json.Unmarshal(on, &back); err != nil || !back.SendToTWS {
		t.Fatalf("sendToTws must round-trip: err=%v got %+v", err, back)
	}
	var old AlertConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"on":"enter","sound":"ping","cooldownSec":60,"requireReentry":false,"speak":true}`), &old); err != nil || old.SendToTWS {
		t.Fatalf("a pre-SendToTWS definition must decode as off: err=%v %+v", err, old)
	}
}
