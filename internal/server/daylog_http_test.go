package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestDayLogHTTP(t *testing.T) {
	var gotSnap bool
	srv := New(Config{Scanner: newFake(), DayLog: func(_ context.Context, date string, snap bool) (core.DayLogMsg, error) {
		gotSnap = snap
		if date == "" {
			date = "2026-10-15"
		}
		return core.DayLogMsg{Date: date, Dates: []string{"2026-10-15", "2026-10-14"},
			Alerts: []core.DayAlert{{Sym: "AAA"}},
			Movers: []core.DayMover{{Side: "gain", Sym: "G"}, {Side: "loss", Sym: "L"}}}, nil
	}})
	h := srv.Handler()
	do := func(method, url, remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := do("GET", "/api/daylog?date=2026-10-14", "127.0.0.1:5000")
	var got struct {
		Date   string
		Alerts []core.DayAlert
		Movers struct{ Gainers, Losers []core.DayMover }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.Date != "2026-10-14" ||
		len(got.Alerts) != 1 || len(got.Movers.Gainers) != 1 || got.Movers.Losers[0].Sym != "L" || gotSnap {
		t.Fatalf("daylog: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/daylog?date=nope", "127.0.0.1:5000"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", w.Code)
	}
	if w := do("POST", "/api/daylog", "127.0.0.1:5000"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", w.Code)
	}
	if w := do("GET", "/api/daylog", "192.168.1.5:5000"); w.Code != http.StatusNotFound {
		t.Fatalf("remote: %d", w.Code)
	}
	w = do("GET", "/api/daylog/dates", "[::1]:5000")
	var d struct{ Dates []string }
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || len(d.Dates) != 2 {
		t.Fatalf("dates: %d %s", w.Code, w.Body)
	}
}
