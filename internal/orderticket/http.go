package orderticket

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Handler serves the pane's API under /api/order/: GET state, POST actions.
// Loopback only; every POST needs the X-Order-Ticket header and a JSON body
// (both force a CORS preflight, which is never answered, so another web page
// in a browser cannot drive it) and a same-host Origin when one is sent.
func (t *Ticket) Handler() http.Handler {
	return http.HandlerFunc(t.serve)
}

func loopback(remote string) bool {
	h, _, err := net.SplitHostPort(remote)
	if err != nil {
		h = remote
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type actionReq struct {
	Side            string   `json:"side"`
	PositionUSD     float64  `json:"positionUSD"`
	Shares          int64    `json:"shares"`
	Sym             string   `json:"sym"`
	Token           string   `json:"token"`
	ID              int64    `json:"id"`
	Settings        Settings `json:"settings"`
	Max             float64  `json:"max"`
	TPPct           float64  `json:"tpPct"`
	On              bool     `json:"on"`
	StopOn          *bool    `json:"stopOn"` // prepare: soft stop on (default) / off
	RiskUSD         float64  `json:"riskUsd"`
	Preset          string   `json:"preset"`
	Trail5m         bool     `json:"trail5m"`
	EntryTimeoutMin int      `json:"entryTimeoutMin"`
	RiskLow         float64  `json:"riskLow"`
	RiskMid         float64  `json:"riskMid"`
	RiskHigh        float64  `json:"riskHigh"`
	RiskPctLow      float64  `json:"riskPctLow"`
	RiskPctMid      float64  `json:"riskPctMid"`
	RiskPctHigh     float64  `json:"riskPctHigh"`
	RiskMode        string   `json:"riskMode"`
	StopPct         float64  `json:"stopPct"`
	StopFollow      bool     `json:"stopFollow"`
	StaleMin        int      `json:"staleMin"`
	FollowSec       int      `json:"followSec"`
	DropPct         float64  `json:"dropPct"`
	DropSec         int      `json:"dropSec"`
	NearPct         float64  `json:"nearPct"`
	BEPct           float64  `json:"bePct"`
	BEOffsetPct     float64  `json:"beOffsetPct"`
	TPLow           float64  `json:"tpLowPct"`
	TPHigh          float64  `json:"tpHighPct"`
}

func (t *Ticket) serve(w http.ResponseWriter, r *http.Request) {
	if !loopback(r.RemoteAddr) {
		http.NotFound(w, r)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/order/")
	if action == "state" && r.Method == http.MethodGet {
		reply(w, 200, t.State())
		return
	}
	if r.Method != http.MethodPost {
		reply(w, 405, map[string]string{"error": "POST only"})
		return
	}
	if r.Header.Get("X-Order-Ticket") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, 403, map[string]string{"error": "forbidden"})
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			reply(w, 403, map[string]string{"error": "cross-origin"})
			return
		}
	}
	var q actionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&q); err != nil {
		reply(w, 400, map[string]string{"error": "bad json"})
		return
	}
	var err error
	var out any
	switch action {
	case "connect":
		t.Connect()
	case "disconnect":
		t.Disconnect()
	case "prepare":
		out, err = t.PrepareOrder(q.Side, q.PositionUSD, q.Shares, q.Sym, q.TPPct, q.StopOn != nil && !*q.StopOn, q.RiskUSD)
	case "previewsize":
		t.SetPreviewSize(q.PositionUSD, q.Shares, q.RiskUSD)
	case "close":
		out, err = t.PrepareClose(q.Sym)
	case "preset":
		err = t.SetPreset(r.Context(), q.Preset, q.Trail5m, q.EntryTimeoutMin)
	case "stopbe":
		err = t.MoveStopToEntry(q.ID)
	case "stopbesym":
		err = t.MoveStopToEntrySym(q.Sym)
	case "cancelsym":
		out, err = t.CancelSymbol(q.Sym)
	case "confirm":
		err = t.Confirm(q.Token, q.Sym)
	case "abort":
		t.Abort()
	case "cancel":
		err = t.Cancel(q.ID)
	case "cancelall":
		err = t.CancelAll()
	case "settings":
		err = t.SetSettings(r.Context(), q.Settings)
	case "stop":
		err = t.SetStopEnabled(q.ID, q.On)
	case "stopcfg":
		err = t.SetStopSettings(r.Context(), q.StopPct, q.StopFollow)
	case "risk":
		err = t.SetRiskPresets(r.Context(), q.RiskLow, q.RiskMid, q.RiskHigh)
	case "riskpct":
		err = t.SetRiskPctPresets(r.Context(), q.RiskPctLow, q.RiskPctMid, q.RiskPctHigh)
	case "riskmode":
		err = t.SetRiskMode(r.Context(), q.RiskMode)
	case "breakeven":
		err = t.SetBreakeven(r.Context(), q.On, q.BEPct, q.BEOffsetPct)
	case "dropcancel":
		err = t.SetDropCancel(r.Context(), q.On, q.DropPct, q.DropSec, q.NearPct)
	case "followsec":
		err = t.SetFollowSec(r.Context(), q.FollowSec)
	case "emalive":
		err = t.SetEMALive(r.Context(), q.On)
	case "stalemin":
		err = t.SetStaleMin(r.Context(), q.StaleMin)
	case "tp":
		err = t.SetTPPresets(r.Context(), q.TPLow, q.TPHigh)
	case "max":
		err = t.SetMaxPosition(r.Context(), q.Max)
	default:
		reply(w, 404, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		reply(w, 400, map[string]any{"error": err.Error(), "state": t.State()})
		return
	}
	reply(w, 200, map[string]any{"ok": true, "result": out, "state": t.State()})
}
