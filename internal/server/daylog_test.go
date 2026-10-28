package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestDayLogGetAndAlertHook(t *testing.T) {
	fs := newFake()
	var mu sync.Mutex
	var asked []string
	var snaps []bool
	hooked := make(chan core.AlertMsg, 4)
	srv := New(Config{
		Scanner:   fs,
		AlertHook: func(a core.AlertMsg) { hooked <- a },
		DayLog: func(_ context.Context, date string, snap bool) (core.DayLogMsg, error) {
			mu.Lock()
			asked, snaps = append(asked, date), append(snaps, snap)
			mu.Unlock()
			if date == "2026-01-02" {
				return core.DayLogMsg{}, errors.New("boom")
			}
			return core.DayLogMsg{Date: "2026-10-15", Dates: []string{"2026-10-15"}, Alerts: []core.DayAlert{{Sym: "AAA", ScanID: "s"}}, CSVDir: `C:\x`}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := dialWS(t, ctx, ts)
	defer c.CloseNow()

	writeFrame(t, ctx, c, core.TDayLogGet, core.DayLogGetMsg{Snapshot: true})
	var got core.DayLogMsg
	if err := json.Unmarshal(readUntil(t, ctx, c, core.TDayLogData).D, &got); err != nil || got.Date != "2026-10-15" || len(got.Alerts) != 1 || got.CSVDir != `C:\x` {
		t.Fatalf("daylog.data: %+v err=%v", got, err)
	}
	for _, bad := range []string{"yesterday", "2026-1-2"} {
		writeFrame(t, ctx, c, core.TDayLogGet, core.DayLogGetMsg{Date: bad})
		var em core.ErrorMsg
		if err := json.Unmarshal(readUntil(t, ctx, c, core.TError).D, &em); err != nil || em.Code != "daylog" {
			t.Fatalf("bad date %q: %+v err=%v", bad, em, err)
		}
	}
	writeFrame(t, ctx, c, core.TDayLogGet, core.DayLogGetMsg{Date: "2026-01-02"})
	var em core.ErrorMsg
	if err := json.Unmarshal(readUntil(t, ctx, c, core.TError).D, &em); err != nil || em.Code != "daylog" || em.Msg != "boom" {
		t.Fatalf("callback error: %+v err=%v", em, err)
	}
	mu.Lock()
	if !reflect.DeepEqual(asked, []string{"", "2026-01-02"}) || !snaps[0] || snaps[1] {
		t.Fatalf("asked %q snaps %v", asked, snaps)
	}
	mu.Unlock()

	// The hook sees every alert the scanner fires, whatever clients do with it.
	fs.alerts <- core.AlertMsg{ScanID: "s", Sym: "ZZZ", Kind: "enter"}
	select {
	case a := <-hooked:
		if a.Sym != "ZZZ" {
			t.Fatalf("hook got %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("alert hook not called")
	}
}

func TestDayLogOffAnswersWithError(t *testing.T) {
	srv := New(Config{Scanner: newFake()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.Start(ctx)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := dialWS(t, ctx, ts)
	defer c.CloseNow()
	writeFrame(t, ctx, c, core.TDayLogGet, core.DayLogGetMsg{})
	var em core.ErrorMsg
	if err := json.Unmarshal(readUntil(t, ctx, c, core.TError).D, &em); err != nil || em.Code != "daylog" {
		t.Fatalf("want a daylog error: %+v err=%v", em, err)
	}
}
