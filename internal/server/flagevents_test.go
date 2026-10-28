package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestFlagEventsGetPublishAndBadDate(t *testing.T) {
	px := 4.5
	var mu sync.Mutex
	var asked []string
	srv := New(Config{Scanner: newFake(), FlagEvents: func(_ context.Context, date string) (core.FlagEventsMsg, error) {
		mu.Lock()
		asked = append(asked, date)
		mu.Unlock()
		if date == "2026-01-02" {
			return core.FlagEventsMsg{}, errors.New("boom")
		}
		return core.FlagEventsMsg{
			Date: "2026-10-06", Dates: []string{"2026-10-06", "2026-10-05"},
			Events: []core.FlagEvent{{Ts: 1, At: 2, Sym: "AAA", Kind: "entry", Label: "Entry", Price: &px}},
		}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := dialWS(t, ctx, ts)
	defer c.CloseNow()

	// get -> data frame carrying the stubbed message ("" date passes through).
	writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{})
	f := readUntil(t, ctx, c, core.TFlagEventsData)
	var got core.FlagEventsMsg
	if err := json.Unmarshal(f.D, &got); err != nil || got.Date != "2026-10-06" ||
		!reflect.DeepEqual(got.Dates, []string{"2026-10-06", "2026-10-05"}) ||
		len(got.Events) != 1 || got.Events[0].Sym != "AAA" || got.Events[0].Price == nil || *got.Events[0].Price != 4.5 {
		t.Fatalf("flagevents.data mismatch: %+v err=%v", got, err)
	}
	writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{Date: "2026-10-05"})
	readUntil(t, ctx, c, core.TFlagEventsData)
	mu.Lock()
	if !reflect.DeepEqual(asked, []string{"", "2026-10-05"}) {
		t.Fatalf("FlagEvents called with %q", asked)
	}
	mu.Unlock()

	// bad date -> error frame, callback not reached.
	for _, bad := range []string{"yesterday", "2026-13-40", "2026-1-2"} {
		writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{Date: bad})
		var em core.ErrorMsg
		if err := json.Unmarshal(readUntil(t, ctx, c, core.TError).D, &em); err != nil || em.Code != "flagevents" {
			t.Fatalf("bad date %q: error frame %+v err=%v", bad, em, err)
		}
	}
	// callback error -> error frame.
	writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{Date: "2026-01-02"})
	var em core.ErrorMsg
	if err := json.Unmarshal(readUntil(t, ctx, c, core.TError).D, &em); err != nil || em.Code != "flagevents" || em.Msg != "boom" {
		t.Fatalf("callback error frame: %+v err=%v", em, err)
	}
	mu.Lock()
	if len(asked) != 3 {
		t.Fatalf("bad dates must not reach FlagEvents: %q", asked)
	}
	mu.Unlock()

	// PublishFlagEvents reaches every connected client as flagevents.add.
	c2 := dialWS(t, ctx, ts)
	defer c2.CloseNow()
	writeFrame(t, ctx, c2, core.TFlagEventsGet, core.FlagEventsGetMsg{})
	readUntil(t, ctx, c2, core.TFlagEventsData) // c2 is registered once it is answered
	srv.PublishFlagEvents(core.FlagEventsMsg{Date: "2026-10-06", Events: []core.FlagEvent{{Ts: 9, Sym: "BBB", Kind: "flat"}}})
	for _, conn := range []*websocket.Conn{c, c2} {
		var add core.FlagEventsMsg
		if err := json.Unmarshal(readUntil(t, ctx, conn, core.TFlagEventsAdd).D, &add); err != nil ||
			add.Date != "2026-10-06" || len(add.Events) != 1 || add.Events[0].Sym != "BBB" || add.Events[0].Price != nil {
			t.Fatalf("flagevents.add mismatch: %+v err=%v", add, err)
		}
	}
}

func TestFlagEventsOffIsIgnored(t *testing.T) {
	srv := New(Config{Scanner: newFake()}) // FlagEvents nil: feature off
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := dialWS(t, ctx, ts)
	defer c.CloseNow()
	writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{Date: "garbage"})
	srv.PublishFlagEvents(core.FlagEventsMsg{Date: "2026-10-06"}) // nothing to assert beyond not blocking/panicking
}

// The dildo / climax rows ride the same frames: their kind and one-line note
// reach the client in flagevents.data and flagevents.add; a flag row without a
// note carries no "note" key at all.
func TestFlagEventsCarryTheSignalNote(t *testing.T) {
	px := 1.09
	dildo := core.FlagEvent{Ts: 1, At: 2, Sym: "SBFM", Kind: "dildo", Label: "GREEN DILDO", Price: &px,
		Note: "+9.0% body (4.5x prior), 200x volume, $218k; floor 1.04"}
	srv := New(Config{Scanner: newFake(), FlagEvents: func(context.Context, string) (core.FlagEventsMsg, error) {
		return core.FlagEventsMsg{Date: "2026-10-08", Dates: []string{"2026-10-08"},
			Events: []core.FlagEvent{dildo, {Ts: 3, Sym: "BEAT", Kind: "entry", Label: "LONG (FLAG)"}}}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := dialWS(t, ctx, ts)
	defer c.CloseNow()

	writeFrame(t, ctx, c, core.TFlagEventsGet, core.FlagEventsGetMsg{})
	f := readUntil(t, ctx, c, core.TFlagEventsData)
	var raw struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(f.D, &raw); err != nil || len(raw.Events) != 2 {
		t.Fatalf("data frame %s err=%v", f.D, err)
	}
	if raw.Events[0]["note"] != dildo.Note || raw.Events[0]["kind"] != "dildo" {
		t.Fatalf("dildo row on the wire: %v", raw.Events[0])
	}
	if _, has := raw.Events[1]["note"]; has {
		t.Fatalf("a flag row must carry no note key: %v", raw.Events[1])
	}

	bottom := core.FlagEvent{Ts: 4, Sym: "BIYA", Kind: "bottom", Label: "BOTTOM CONFIRMED", Note: "closed 1.88 above the climax midpoint 1.84"}
	srv.PublishFlagEvents(core.FlagEventsMsg{Date: "2026-10-08", Events: []core.FlagEvent{bottom}})
	var add core.FlagEventsMsg
	if err := json.Unmarshal(readUntil(t, ctx, c, core.TFlagEventsAdd).D, &add); err != nil ||
		len(add.Events) != 1 || add.Events[0] != bottom {
		t.Fatalf("flagevents.add: %+v err=%v", add, err)
	}
}
