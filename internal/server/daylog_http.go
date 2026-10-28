package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Read-only HTTP view of the Day Log for other local tools (AfterBell's Market
// Log page). Loopback callers only, GET only; it never takes the movers
// snapshot, it only reads what is recorded.

type dayLogHTTP struct {
	Date   string          `json:"date"`
	Alerts []core.DayAlert `json:"alerts"`
	Movers struct {
		Gainers []core.DayMover `json:"gainers"`
		Losers  []core.DayMover `json:"losers"`
	} `json:"movers"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) daylogGuard(w http.ResponseWriter, r *http.Request) bool {
	if !loopbackHost(r.RemoteAddr) {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return false
	}
	if s.cfg.DayLog == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the Day Log is not running"})
		return false
	}
	return true
}

func (s *Server) serveDayLog(w http.ResponseWriter, r *http.Request) {
	if !s.daylogGuard(w, r) {
		return
	}
	date := r.URL.Query().Get("date")
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad date " + strconv.Quote(date) + ": want YYYY-MM-DD"})
			return
		}
	}
	msg, err := s.cfg.DayLog(r.Context(), date, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := dayLogHTTP{Date: msg.Date, Alerts: msg.Alerts}
	if out.Alerts == nil {
		out.Alerts = []core.DayAlert{}
	}
	out.Movers.Gainers, out.Movers.Losers = []core.DayMover{}, []core.DayMover{}
	for _, m := range msg.Movers {
		if m.Side == "gain" {
			out.Movers.Gainers = append(out.Movers.Gainers, m)
		} else {
			out.Movers.Losers = append(out.Movers.Losers, m)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) serveDayLogDates(w http.ResponseWriter, r *http.Request) {
	if !s.daylogGuard(w, r) {
		return
	}
	msg, err := s.cfg.DayLog(r.Context(), "", false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	d := msg.Dates
	if d == nil {
		d = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dates": d})
}
