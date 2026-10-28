// alerts.go consumes RTPR's alerts WebSocket (wss://ws.rtpr.io/ws-alerts) —
// the delivery channel for Impact Scores (Beta): RTPR analyzes each press
// release after it hits the wire and pushes a 0-100 score for releases
// clearing the account's configured threshold, typically ~45s after
// publication. Scores are an ENRICHMENT of articles we already carry (the
// 3s ref poll beats the scorer), never a speed path.
//
// Requires the user to enable "Alert me on high-impact PRs" + WebSocket
// delivery in RTPR's Connect tab; until then the socket simply stays quiet.
package rtpr

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	defaultAlertsURL = "wss://ws.rtpr.io/ws-alerts"

	alertsBackoffStart = 2 * time.Second // docs: ~2s, capped at one minute
	alertsBackoffMax   = time.Minute
	alertsReadWait     = 90 * time.Second // server pings every 30s; silence past 90s = dead
)

// ScoreEvent is one Impact Score delivery, normalized to this app's article
// id scheme so it joins the archive/panel by id.
type ScoreEvent struct {
	ArticleID string // "rtpr" + permalink id — matches the ref-poll's ids
	Ticker    string
	Score     int
	Tier      string // e.g. "high"
	EventType string // e.g. "fda_approval"
	Direction string // bullish | bearish | neutral
	URL       string // signed permalink, follow EXACTLY as sent (hot window)
	PubAt     string
}

type AlertsConfig struct {
	Token string
	URL   string // override for tests

	// Health optionally reports this socket's connection state under the name
	// "rtprAlerts" for the status bar; nil is a no-op (guarded at every call
	// site).
	Health core.HealthReporter
}

type Alerts struct {
	cfg AlertsConfig
	out chan ScoreEvent
}

func NewAlerts(cfg AlertsConfig) *Alerts {
	if cfg.URL == "" {
		cfg.URL = defaultAlertsURL
	}
	return &Alerts{cfg: cfg, out: make(chan ScoreEvent, 64)}
}

func (a *Alerts) Out() <-chan ScoreEvent { return a.out }

// Run connects and reconnects until ctx is done; never returns a fatal error
// (a dead score socket degrades to "no scores", nothing else).
func (a *Alerts) Run(ctx context.Context) error {
	backoff := alertsBackoffStart
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if a.cfg.Health != nil {
			a.cfg.Health.SourceFail("rtprAlerts", err.Error())
		}
		// 4001 = invalid key, 4004 = subscription inactive: still retry (the
		// user may fix the account mid-run), but at the slow cap, not a hammer.
		if s := websocket.CloseStatus(err); s == 4001 || s == 4004 {
			backoff = alertsBackoffMax
			logx.L().Warn("rtpr alerts ws refused (key or subscription); retrying slowly", "close", int(s))
		} else {
			logx.L().Warn("rtpr alerts ws dropped; reconnecting", "err", err, "in", backoff)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, alertsBackoffMax)
	}
}

func (a *Alerts) session(ctx context.Context) error {
	u := a.cfg.URL + "?apiKey=" + url.QueryEscape(a.cfg.Token)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	logx.L().Info("rtpr alerts ws connected (impact scores active if enabled in Connect)")
	if a.cfg.Health != nil {
		a.cfg.Health.SourceOK("rtprAlerts")
	}

	for {
		rctx, rcancel := context.WithTimeout(ctx, alertsReadWait)
		_, payload, err := conn.Read(rctx)
		rcancel()
		if err != nil {
			return err
		}
		var probe struct {
			Type      string `json:"type"`
			AlertKind string `json:"alert_kind"`
		}
		if json.Unmarshal(payload, &probe) != nil {
			continue
		}
		switch {
		case probe.Type == "ping":
			// Server-initiated heartbeat: reply or be disconnected at 90s.
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`)); err != nil {
				return err
			}
		case probe.Type == "alert" && probe.AlertKind == "high_impact":
			if ev, ok := parseScore(payload); ok {
				select {
				case a.out <- ev:
				default: // consumer behind; a lost score is cosmetic
				}
			}
		default:
			// Rule-match alerts (no rules configured by this app) and unknown
			// frames: ignore quietly.
		}
	}
}

func parseScore(payload []byte) (ScoreEvent, bool) {
	var m struct {
		Ticker    string `json:"ticker"`
		Score     int    `json:"impact_score"`
		Tier      string `json:"impact_tier"`
		EventType string `json:"event_type"`
		Direction string `json:"impact_direction"`
		URL       string `json:"article_url"`
		PubAt     string `json:"article_published_at"`
	}
	if err := json.Unmarshal(payload, &m); err != nil || m.URL == "" {
		return ScoreEvent{}, false
	}
	return ScoreEvent{
		ArticleID: "rtpr" + idFromURL(m.URL),
		Ticker:    strings.ToUpper(strings.TrimSpace(m.Ticker)),
		Score:     m.Score,
		Tier:      m.Tier,
		EventType: m.EventType,
		Direction: m.Direction,
		URL:       m.URL,
		PubAt:     m.PubAt,
	}, true
}

// FetchOne fetches and parses a single article by its (possibly signed)
// permalink — used when a score arrives for an article the ref poll hasn't
// delivered yet (the signed hot-window URL is authorized for exactly this).
func (p *Poller) FetchOne(ctx context.Context, articleURL string) (Batch, bool) {
	art, ok := p.fetchArticle(ctx, articleURL)
	if !ok {
		return Batch{}, false
	}
	id := "rtpr" + idFromURL(articleURL)
	b := Batch{Items: []core.NewsItem{art.toNewsItem(id, articleURL)}}
	if art.BodyHTML != "" {
		b.Bodies = map[string]string{id: art.BodyHTML}
	}
	return b, true
}
