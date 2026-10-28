package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// twsRig is a server with two scans -- "dildo" (SendToTWS on) and "plain" (off)
// -- a recording AlertToTWS hook and a hand-driven clock.
type twsRig struct {
	fs    *fakeScanner
	conn  *websocket.Conn
	ctx   context.Context
	clock atomic.Int64 // unix ms

	mu    sync.Mutex
	calls []string
}

func newTWSRig(t *testing.T, withHook bool) *twsRig {
	t.Helper()
	r := &twsRig{fs: newFake()}
	r.clock.Store(time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC).UnixMilli())
	r.fs.defs = []core.ScanDef{
		{ID: "dildo", Name: "Green Dildo", Enabled: true, Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter, SendToTWS: true}},
		{ID: "plain", Name: "Plain", Enabled: true, Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter}},
	}
	cfg := Config{Scanner: r.fs}
	if withHook {
		cfg.AlertToTWS = func(sym string) {
			r.mu.Lock()
			r.calls = append(r.calls, sym)
			r.mu.Unlock()
		}
	}
	srv := New(cfg)
	srv.now = func() time.Time { return time.UnixMilli(r.clock.Load()) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	r.ctx = ctx
	r.conn = dialWS(t, ctx, ts)
	t.Cleanup(func() { r.conn.CloseNow() })
	r.sync(t)
	return r
}

// sync round-trips a hello: the server handles one connection's frames in
// order, so once scan.list is back every earlier frame has been processed.
func (r *twsRig) sync(t *testing.T) {
	t.Helper()
	writeFrame(t, r.ctx, r.conn, core.THello, nil)
	readUntil(t, r.ctx, r.conn, core.TScanList)
}

func (r *twsRig) advance(d time.Duration) { r.clock.Add(d.Milliseconds()) }

func (r *twsRig) alert(scanID, sym string) {
	r.fs.alerts <- core.AlertMsg{ScanID: scanID, Sym: sym, Kind: "enter", At: r.clock.Load(), Sound: "ping"}
}

func (r *twsRig) auto(t *testing.T) core.TwsAutoMsg {
	t.Helper()
	f := readUntil(t, r.ctx, r.conn, core.TTwsAuto)
	var m core.TwsAutoMsg
	if err := json.Unmarshal(f.D, &m); err != nil {
		t.Fatalf("tws.auto: %v", err)
	}
	return m
}

func (r *twsRig) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *twsRig) pick(t *testing.T, sym string) {
	t.Helper()
	writeFrame(t, r.ctx, r.conn, core.TTapeWatch, core.TapeWatchMsg{Sym: sym})
	r.sync(t)
}

func TestTwsAutoSendsAFlaggedScansAlert(t *testing.T) {
	r := newTWSRig(t, true)
	r.alert("dildo", "ABCD")
	m := r.auto(t)
	if !m.Sent || m.Sym != "ABCD" || m.ScanID != "dildo" || m.ScanName != "Green Dildo" || m.Reason != "" || m.At != r.clock.Load() {
		t.Fatalf("tws.auto %+v", m)
	}
	if got := strings.Join(r.sent(), ","); got != "ABCD" {
		t.Fatalf("hook calls %q", got)
	}
}

func TestTwsAutoIgnoresAScanWithoutTheFlag(t *testing.T) {
	r := newTWSRig(t, true)
	r.alert("plain", "NOPE")
	r.alert("dildo", "YES")
	// Alerts are handled in order: the first tws.auto is the flagged scan's, so
	// the plain scan's alert produced none.
	if m := r.auto(t); m.Sym != "YES" || !m.Sent {
		t.Fatalf("first tws.auto %+v", m)
	}
	if got := strings.Join(r.sent(), ","); got != "YES" {
		t.Fatalf("hook calls %q", got)
	}
}

func TestTwsAutoSkipsWhenTheUserJustPickedAnotherSymbol(t *testing.T) {
	r := newTWSRig(t, true)
	r.pick(t, "mine")
	r.advance(10 * time.Second)
	r.alert("dildo", "ABCD")
	m := r.auto(t)
	if m.Sent || m.Sym != "ABCD" || m.Reason != "you picked MINE 10s ago" {
		t.Fatalf("tws.auto %+v", m)
	}
	if len(r.sent()) != 0 {
		t.Fatalf("hook called: %v", r.sent())
	}

	// 20 s after the pick the quiet period is over.
	r.advance(10 * time.Second)
	r.alert("dildo", "ABCD")
	if m := r.auto(t); !m.Sent {
		t.Fatalf("after the quiet period: %+v", m)
	}
}

func TestTwsAutoSendsWhenTheUserPickedTheSameSymbol(t *testing.T) {
	r := newTWSRig(t, true)
	r.pick(t, "abcd")
	r.advance(3 * time.Second)
	r.alert("dildo", "ABCD")
	if m := r.auto(t); !m.Sent || m.Sym != "ABCD" {
		t.Fatalf("tws.auto %+v", m)
	}
	if got := strings.Join(r.sent(), ","); got != "ABCD" {
		t.Fatalf("hook calls %q", got)
	}
}

func TestTwsAutoMinimumGapBetweenSends(t *testing.T) {
	r := newTWSRig(t, true)
	r.alert("dildo", "AAA")
	if m := r.auto(t); !m.Sent {
		t.Fatalf("first: %+v", m)
	}
	r.advance(3 * time.Second)
	r.alert("dildo", "BBB")
	m := r.auto(t)
	if m.Sent || m.Sym != "BBB" || m.Reason != "AAA was sent 3s ago" {
		t.Fatalf("3 s later: %+v", m)
	}
	r.advance(3 * time.Second) // 6 s after the send (a skipped alert does not restart the gap)
	r.alert("dildo", "CCC")
	if m := r.auto(t); !m.Sent || m.Sym != "CCC" {
		t.Fatalf("6 s later: %+v", m)
	}
	if got := strings.Join(r.sent(), ","); got != "AAA,CCC" {
		t.Fatalf("hook calls %q", got)
	}
}

func TestTwsAutoNilHookDoesNothing(t *testing.T) {
	r := newTWSRig(t, false)
	r.alert("dildo", "ABCD")
	readUntil(t, r.ctx, r.conn, core.TAlert) // the alert itself still goes out
	r.fs.ticks <- core.ScanTick{ID: "dildo", Seq: 1, Order: []string{"ABCD"}}
	// Everything up to the tick that follows the alert: no tws.auto among it.
	for i := 0; i < 30; i++ {
		rctx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
		_, data, err := r.conn.Read(rctx)
		cancel()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f core.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.T == core.TTwsAuto {
			t.Fatalf("tws.auto with no hook: %s", data)
		}
		if f.T == core.TScanTick {
			return
		}
	}
	t.Fatal("no scan.tick")
}

func TestTwsFollowOffSkipsAlertToTWSAndPersists(t *testing.T) {
	var mu sync.Mutex
	stored := ""
	r := &twsRig{fs: newFake()}
	r.clock.Store(time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC).UnixMilli())
	r.fs.defs = []core.ScanDef{{ID: "dildo", Name: "Green Dildo", Enabled: true, Alert: core.AlertConfig{Enabled: true, On: core.AlertOnEnter, SendToTWS: true}}}
	cfg := Config{Scanner: r.fs,
		AlertToTWS:   func(sym string) { r.mu.Lock(); r.calls = append(r.calls, sym); r.mu.Unlock() },
		AlertTWSLoad: func(context.Context) (string, error) { mu.Lock(); defer mu.Unlock(); return stored, nil },
		AlertTWSSave: func(_ context.Context, v string) error { mu.Lock(); stored = v; mu.Unlock(); return nil },
	}
	srv := New(cfg)
	srv.now = func() time.Time { return time.UnixMilli(r.clock.Load()) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	r.ctx = ctx
	r.conn = dialWS(t, ctx, ts)
	t.Cleanup(func() { r.conn.CloseNow() })
	r.sync(t)

	writeFrame(t, ctx, r.conn, core.TAlertTWSGet, nil)
	var m core.AlertTWSMsg
	if err := json.Unmarshal(readUntil(t, ctx, r.conn, core.TAlertTWSData).D, &m); err != nil || !m.Enabled {
		t.Fatalf("default must be on: %+v %v", m, err)
	}
	writeFrame(t, ctx, r.conn, core.TAlertTWSSet, core.AlertTWSMsg{Enabled: false})
	if err := json.Unmarshal(readUntil(t, ctx, r.conn, core.TAlertTWSData).D, &m); err != nil || m.Enabled {
		t.Fatalf("broadcast after off: %+v %v", m, err)
	}
	mu.Lock()
	if stored != "0" {
		t.Fatalf("persisted %q", stored)
	}
	mu.Unlock()
	r.alert("dildo", "ABCD")
	time.Sleep(300 * time.Millisecond)
	if got := r.sent(); len(got) != 0 {
		t.Fatalf("hook called while off: %v", got)
	}
	writeFrame(t, ctx, r.conn, core.TAlertTWSSet, core.AlertTWSMsg{Enabled: true})
	readUntil(t, ctx, r.conn, core.TAlertTWSData)
	r.alert("dildo", "WXYZ")
	r.auto(t)
	if got := strings.Join(r.sent(), ","); got != "WXYZ" {
		t.Fatalf("hook calls %q", got)
	}

	// A fresh server over the same store starts OFF when "0" was persisted.
	stored = "0"
	s2 := New(cfg)
	if s2.twsFollow() {
		t.Fatal("persisted off not honoured")
	}
}
