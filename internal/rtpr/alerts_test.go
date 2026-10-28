package rtpr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Real shape from RTPR's docs (Impact Score frame) — the rule-match variant
// (with a "rules" array, no alert_kind) must be ignored, since this app
// registers no rules and would otherwise misread one as a score.
const highImpactFrame = `{
  "type": "alert", "alert_kind": "high_impact", "ticker": "BMRA",
  "impact_score": 92, "impact_tier": "high", "event_type": "fda_approval",
  "impact_direction": "bullish",
  "article_published_at": "2026-06-29T18:30:00Z",
  "article_url": "https://rtpr.io/a/lseg_n456?exp=1777497152&sig=cd34"
}`

const ruleMatchFrame = `{
  "type": "alert", "ticker": "AAPL",
  "rules": [{"rule_name": "Apple guidance"}],
  "article_published_at": "2026-06-29T18:30:00Z",
  "article_url": "https://rtpr.io/a/lseg_n123"
}`

func TestParseScoreExtractsHighImpactOnly(t *testing.T) {
	ev, ok := parseScore([]byte(highImpactFrame))
	if !ok {
		t.Fatal("high_impact frame rejected")
	}
	if ev.ArticleID != "rtprlseg_n456" {
		t.Fatalf("id scheme: want rtprlseg_n456, got %s", ev.ArticleID)
	}
	if ev.Ticker != "BMRA" || ev.Score != 92 || ev.Tier != "high" ||
		ev.EventType != "fda_approval" || ev.Direction != "bullish" {
		t.Fatalf("fields: %+v", ev)
	}
	if ev.URL != "https://rtpr.io/a/lseg_n456?exp=1777497152&sig=cd34" {
		t.Fatalf("url must be kept exactly as sent (signed): %q", ev.URL)
	}
}

func TestAlertsSessionIgnoresRuleMatchesRepliesPongForwardsScores(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("apiKey")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(ruleMatchFrame))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`))
		// Must reply pong before the next frame is meaningfully tested.
		_, msg, err := conn.Read(ctx)
		if err != nil || string(msg) != `{"type":"pong"}` {
			t.Errorf("want a pong reply, got %q (err %v)", msg, err)
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(highImpactFrame))
		<-ctx.Done()
	}))
	defer srv.Close()

	a := NewAlerts(AlertsConfig{Token: "tok123", URL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	select {
	case ev := <-a.Out():
		if ev.Ticker != "BMRA" || ev.Score != 92 {
			t.Fatalf("want the high_impact event, got %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no score delivered")
	}
	if gotKey != "tok123" {
		t.Fatalf("token not sent as apiKey query param: %q", gotKey)
	}

	// The rule-match frame must NOT have produced an event too.
	select {
	case ev := <-a.Out():
		t.Fatalf("rule-match frame should not surface as a score event: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAlertsReconnectsAfterDrop(t *testing.T) {
	accepts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts++
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if accepts == 1 {
			conn.Close(websocket.StatusInternalError, "kick")
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(highImpactFrame))
		<-r.Context().Done()
	}))
	defer srv.Close()

	h := &fakeHealth{}
	a := NewAlerts(AlertsConfig{Token: "t", URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Health: h})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	select {
	case ev := <-a.Out():
		if ev.Score != 92 {
			t.Fatalf("unexpected event after reconnect: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("never recovered after the first connection was killed")
	}
	if accepts < 2 {
		t.Fatalf("expected a reconnect, saw %d accepts", accepts)
	}

	// The kicked first connection must have reported rtprAlerts failing, and
	// reaching the event above proves the reconnect reported it healthy again.
	okNames, failNames := h.snapshot()
	if len(okNames) == 0 || okNames[0] != "rtprAlerts" {
		t.Fatalf("expected an rtprAlerts SourceOK call, got %v", okNames)
	}
	if len(failNames) == 0 || failNames[0] != "rtprAlerts" {
		t.Fatalf("expected an rtprAlerts SourceFail call from the kicked connection, got %v", failNames)
	}
}
