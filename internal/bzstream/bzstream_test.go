package bzstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeHealth records SourceOK/SourceFail calls in order, safe for concurrent
// use since the stream's session goroutine is the caller.
type fakeHealth struct {
	mu    sync.Mutex
	ok    []string
	fails []string
}

func (f *fakeHealth) SourceOK(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok = append(f.ok, name)
}

func (f *fakeHealth) SourceFail(name, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, name)
}

func (f *fakeHealth) snapshot() (ok, fails []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ok...), append([]string(nil), f.fails...)
}

// fakeBz is a minimal Benzinga WS endpoint: verifies the token, waits for the
// client's "replay", then plays scripted frames.
func fakeBz(t *testing.T, frames []string, gotToken *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotToken = r.URL.Query().Get("token")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()

		// First client frame must be the replay request.
		_, msg, err := conn.Read(ctx)
		if err != nil || string(msg) != "replay" {
			t.Errorf("first client frame: want replay, got %q (err %v)", msg, err)
			return
		}
		for _, f := range frames {
			if err := conn.Write(ctx, websocket.MessageText, []byte(f)); err != nil {
				return
			}
		}
		// Answer pings until the client goes away.
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if string(msg) == "ping" {
				_ = conn.Write(ctx, websocket.MessageText, []byte("pong"))
			}
		}
	}))
}

const createdFrame = `{
  "id": "uuid-1", "api_version": "v1", "kind": "News/v1",
  "data": {
    "action": "created", "id": 999001, "timestamp": "2026-07-28T13:30:02Z",
    "content": {
      "id": 999001, "author": "BZ Wire",
      "created": "2026-07-28T13:30:01Z", "updated": "2026-07-28T13:30:01Z",
      "title": "Acme Pharma&#39;s Drug Gets FDA Nod &amp; Fast Track",
      "teaser": "S&amp;P watcher note",
      "body": "<p>full body</p>",
      "url": "https://www.benzinga.com/news/999001",
      "stocks": [{"name": "acme"}, {"name": " BETA "}],
      "channels": [{"name": "FDA"}]
    }
  }
}`

const deletedFrame = `{
  "id": "uuid-2", "api_version": "v1", "kind": "News/v1",
  "data": {"action": "deleted", "id": 999002, "timestamp": "2026-07-28T13:31:00Z",
           "content": {"id": 999002, "title": "gone"}}
}`

func TestStreamParsesCreatedSkipsDeletedToleratesPong(t *testing.T) {
	var gotToken string
	srv := fakeBz(t, []string{"pong", createdFrame, deletedFrame}, &gotToken)
	defer srv.Close()

	s := New(Config{Token: "bz.production.test", URL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	select {
	case b := <-s.Out():
		it := b.Items[0]
		// ID must match the REST client's "bz<id>" scheme or cross-source dedupe breaks.
		if it.ID != "bz999001" {
			t.Fatalf("ID scheme: want bz999001 got %q", it.ID)
		}
		if it.Headline != "Acme Pharma's Drug Gets FDA Nod & Fast Track" {
			t.Fatalf("entities not decoded: %q", it.Headline)
		}
		if it.Teaser != "S&P watcher note" {
			t.Fatalf("teaser entities: %q", it.Teaser)
		}
		if len(it.Tickers) != 2 || it.Tickers[0] != "ACME" || it.Tickers[1] != "BETA" {
			t.Fatalf("tickers must be upper/trimmed: %v", it.Tickers)
		}
		if b.Bodies["bz999001"] != "<p>full body</p>" {
			t.Fatalf("body must stay raw HTML: %q", b.Bodies["bz999001"])
		}
		if !it.HasBody {
			t.Fatal("HasBody should be set")
		}
		if it.TS != time.Date(2026, 7, 28, 13, 30, 1, 0, time.UTC).UnixMilli() {
			t.Fatalf("TS from content.created: got %d", it.TS)
		}
	case <-ctx.Done():
		t.Fatal("no batch delivered")
	}

	// The deleted frame must NOT produce a second batch.
	select {
	case b := <-s.Out():
		t.Fatalf("deleted action should be ignored, got %+v", b)
	case <-time.After(300 * time.Millisecond):
	}

	if gotToken != "bz.production.test" {
		t.Fatalf("token not sent in query: %q", gotToken)
	}
}

// Benzinga's native socket does not match its docs everywhere; these are the
// observed/known variants the parser must survive: RFC1123 timestamps, string
// ids, plain-string stocks, and "securities" instead of "stocks". Every case
// must still mint the REST-compatible "bz<id>" so cross-source dedupe holds.
func TestParseToleratesNativeSchemaVariants(t *testing.T) {
	s := New(Config{Token: "t"})

	cases := []struct {
		name    string
		frame   string
		wantID  string
		wantTk  []string
		wantTS  int64
		tsExact bool
	}{
		{
			name: "rfc1123-created and string id",
			frame: `{"kind":"News/v1","data":{"action":"created","content":{
				"id":"777001","title":"T","created":"Mon, 28 Jul 2026 04:27:51 -0400",
				"stocks":[{"name":"AAPL"}]}}}`,
			wantID: "bz777001", wantTk: []string{"AAPL"},
			wantTS: time.Date(2026, 7, 28, 8, 27, 51, 0, time.UTC).UnixMilli(), tsExact: true,
		},
		{
			name: "plain string stocks",
			frame: `{"kind":"News/v1","data":{"action":"created","content":{
				"id":777002,"title":"T","created":"2026-07-28T08:30:00Z",
				"stocks":["msft"," tsla "]}}}`,
			wantID: "bz777002", wantTk: []string{"MSFT", "TSLA"},
			wantTS: time.Date(2026, 7, 28, 8, 30, 0, 0, time.UTC).UnixMilli(), tsExact: true,
		},
		{
			name: "securities field with symbol key",
			frame: `{"kind":"News/v1","data":{"action":"updated","content":{
				"id":777003,"title":"T","created":"2026-07-28T08:31:00Z",
				"securities":[{"symbol":"NVDA"}]}}}`,
			wantID: "bz777003", wantTk: []string{"NVDA"},
			wantTS: time.Date(2026, 7, 28, 8, 31, 0, 0, time.UTC).UnixMilli(), tsExact: true,
		},
	}
	for _, tc := range cases {
		b, ok := s.parse([]byte(tc.frame))
		if !ok {
			t.Fatalf("%s: frame rejected", tc.name)
		}
		it := b.Items[0]
		if it.ID != tc.wantID {
			t.Fatalf("%s: id want %s got %s", tc.name, tc.wantID, it.ID)
		}
		if len(it.Tickers) != len(tc.wantTk) {
			t.Fatalf("%s: tickers want %v got %v", tc.name, tc.wantTk, it.Tickers)
		}
		for i := range tc.wantTk {
			if it.Tickers[i] != tc.wantTk[i] {
				t.Fatalf("%s: tickers want %v got %v", tc.name, tc.wantTk, it.Tickers)
			}
		}
		if tc.tsExact && it.TS != tc.wantTS {
			t.Fatalf("%s: ts want %d got %d", tc.name, tc.wantTS, it.TS)
		}
	}
	if s.parseFails != 0 || s.badTimes != 0 {
		t.Fatalf("variant frames must parse cleanly: fails=%d badTimes=%d", s.parseFails, s.badTimes)
	}

	// A genuinely broken timestamp must be counted, not silently now-stamped.
	if _, ok := s.parse([]byte(`{"kind":"News/v1","data":{"action":"created","content":{
		"id":777004,"title":"T","created":"whenever"}}}`)); !ok {
		t.Fatal("bad-timestamp frame should still deliver the article")
	}
	if s.badTimes != 1 {
		t.Fatalf("bad timestamp must be counted, got %d", s.badTimes)
	}
}

// TestParseUsesCreatedAtFromRealWireCapture pins the exact shape captured live
// (SCANNER_BZ_WS_DUMP=1) from the real Benzinga socket: the docs describe a
// "created" field, but the wire sends "created_at"/"updated_at". Getting this
// wrong doesn't drop the article — parse still succeeds — it silently stamps
// published_ms as the receive instant instead of the true publish time, which
// is exactly what surfaced the bug: every push-sourced archive row showed a
// suspicious ~0ms publish-to-receive latency. This is a trimmed real capture,
// not a hand-built fixture.
func TestParseUsesCreatedAtFromRealWireCapture(t *testing.T) {
	s := New(Config{Token: "t"})
	const realFrame = `{
	  "api_version":"websocket/v1","kind":"News/v1",
	  "data":{"action":"Created","id":108600068,
	    "content":{
	      "id":60719081,"revision_id":70729509,"type":"benzinga_partner",
	      "title":"Bitcoin, Ethereum, XRP, Dogecoin Plummet",
	      "body":"<p>...</p>","authors":["Aniket Verma"],"teaser":"t",
	      "url":"https://www.benzinga.com/x/60719081",
	      "created_at":"2026-07-28T11:00:28.820Z",
	      "updated_at":"2026-07-28T11:00:28.841Z",
	      "tags":[{"name":"Crypto"}],
	      "securities":[
	        {"symbol":"$BTC","exchange":"","primary":false},
	        {"symbol":"MSTR","exchange":"NASDAQ","primary":false}
	      ]
	    }}}`

	b, ok := s.parse([]byte(realFrame))
	if !ok {
		t.Fatal("real capture frame rejected")
	}
	it := b.Items[0]
	if it.ID != "bz60719081" {
		t.Fatalf("id: want bz60719081 got %s", it.ID)
	}
	wantTS := time.Date(2026, 7, 28, 11, 0, 28, 820_000_000, time.UTC).UnixMilli()
	if it.TS != wantTS {
		t.Fatalf("TS must come from created_at (docs' \"created\" isn't on the wire): want %d got %d", wantTS, it.TS)
	}
	if s.badTimes != 0 {
		t.Fatalf("a real, correctly-shaped frame must not count as a bad timestamp, got %d", s.badTimes)
	}
	if len(it.Tickers) != 2 || it.Tickers[0] != "$BTC" || it.Tickers[1] != "MSTR" {
		t.Fatalf("tickers from securities.symbol: got %v", it.Tickers)
	}
}

// TestWireTypeDroppedUnknownTypeKept pins the language belt on content.type.
// The Korean frame's metadata is a REAL CAPTURE from the live socket
// (research 2026-08-03): Benzinga's translated editions arrive with
// content.type "benzinga_wire_<country>" — the only per-frame language
// marker that exists (no server-side filter in any Benzinga/Massive API).
// The decisive half is the SECOND case: the English type list is not
// enumerable from a single connection, so an UNKNOWN type must be KEPT —
// a whitelist here would silently lose real news the day Benzinga ships a
// new English type, which is the catastrophic failure mode.
func TestWireTypeDroppedUnknownTypeKept(t *testing.T) {
	s := New(Config{Token: "t"})

	// REAL CAPTURE metadata (id/type/title verbatim from the live stream).
	const koreanWireFrame = `{
	  "api_version":"websocket/v1","kind":"News/v1",
	  "data":{"action":"Created","id":108600300,
	    "content":{
	      "id":60719378,"type":"benzinga_wire_korea",
	      "title":"국장 한 달 새 28% 피바다...",
	      "body":"<p>...</p>",
	      "url":"https://www.benzinga.com/kr/x/60719378",
	      "created_at":"2026-07-28T11:02:11.000Z",
	      "securities":[{"symbol":"SSNLF"}]
	    }}}`
	if _, ok := s.parse([]byte(koreanWireFrame)); ok {
		t.Fatal("benzinga_wire_* frame must be dropped before body handling")
	}
	if s.wireDrops != 1 {
		t.Fatalf("wire drop must be counted: got %d", s.wireDrops)
	}
	if s.parseFails != 0 {
		t.Fatalf("a deliberate wire drop is not a parse failure: got %d", s.parseFails)
	}

	// Unknown type: KEPT, and not counted as a wire drop.
	const unknownTypeFrame = `{"kind":"News/v1","data":{"action":"created","content":{
		"id":888001,"type":"story_v2","title":"Acme Wins Defense Contract",
		"created_at":"2026-07-28T11:03:00Z","securities":[{"symbol":"ACME"}]}}}`
	b, ok := s.parse([]byte(unknownTypeFrame))
	if !ok {
		t.Fatal("unknown content.type must be KEPT — prefix blacklist, never a whitelist")
	}
	if b.Items[0].ID != "bz888001" || b.Items[0].Headline != "Acme Wins Defense Contract" {
		t.Fatalf("unknown-type frame mangled: %+v", b.Items[0])
	}
	if s.wireDrops != 1 {
		t.Fatalf("unknown type must not count as a wire drop: got %d", s.wireDrops)
	}

	// The known English capture shapes still parse with the type field
	// decoded: "benzinga_partner" (real capture above) and a frame with NO
	// type at all (the docs-shaped createdFrame).
	if _, ok := s.parse([]byte(createdFrame)); !ok {
		t.Fatal("typeless docs-shaped frame must still parse")
	}
}

func TestStreamReconnects(t *testing.T) {
	var tok string
	accepts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts++
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if accepts == 1 {
			conn.Close(websocket.StatusInternalError, "kick") // force a reconnect
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		if _, msg, err := conn.Read(ctx); err != nil || string(msg) != "replay" {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(createdFrame))
		_, _, _ = conn.Read(ctx) // hold open
	}))
	defer srv.Close()
	_ = tok

	h := &fakeHealth{}
	s := New(Config{Token: "t", URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Health: h})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	select {
	case b := <-s.Out():
		if b.Items[0].ID != "bz999001" {
			t.Fatalf("post-reconnect batch mismatch: %+v", b.Items[0])
		}
	case <-ctx.Done():
		t.Fatal("never recovered after the first connection was killed")
	}
	if accepts < 2 {
		t.Fatalf("expected a reconnect, saw %d accepts", accepts)
	}

	// The kicked first connection must have reported bzpush failing, and
	// reaching the batch above proves a later connect reported it healthy
	// again — this pairing is what makes a dead source visible on the status
	// bar instead of invisible.
	okNames, failNames := h.snapshot()
	if len(okNames) == 0 || okNames[0] != "bzpush" {
		t.Fatalf("expected a bzpush SourceOK call, got %v", okNames)
	}
	if len(failNames) == 0 || failNames[0] != "bzpush" {
		t.Fatalf("expected a bzpush SourceFail call from the kicked connection, got %v", failNames)
	}
}
