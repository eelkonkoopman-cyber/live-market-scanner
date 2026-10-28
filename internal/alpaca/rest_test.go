package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const (
	testKeyID  = "PKTESTKEYID"
	testSecret = "TESTSECRETVALUE"
)

// fakeREST is an httptest Alpaca: checks auth headers on every call, fails
// the test if a key ever appears in a URL, and serves canned handlers.
type fakeREST struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	routes map[string]http.HandlerFunc
}

func (f *fakeREST) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.String())
	f.mu.Unlock()
	if strings.Contains(r.URL.String(), testKeyID) || strings.Contains(r.URL.String(), testSecret) {
		f.t.Errorf("key in URL: %s", r.URL)
	}
	if r.Header.Get("APCA-API-KEY-ID") != testKeyID || r.Header.Get("APCA-API-SECRET-KEY") != testSecret {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
		return
	}
	for prefix, h := range f.routes {
		if strings.HasPrefix(r.URL.Path, prefix) {
			h(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

func newFake(t *testing.T, routes map[string]http.HandlerFunc, now time.Time) (*Client, *fakeREST) {
	f := &fakeREST{t: t, routes: routes}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(Config{
		Keys:     Keys{KeyID: testKeyID, Secret: testSecret},
		DataBase: srv.URL, TradingBase: srv.URL,
		Now:           func() time.Time { return now },
		SnapshotChunk: 2, BarsChunk: 2,
	})
	return c, f
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

var testAssets = []map[string]any{
	{"symbol": "AAA", "name": "Aaa Inc. Common Stock", "exchange": "NASDAQ", "status": "active", "tradable": true},
	{"symbol": "BBB", "name": "Bbb Corp American Depositary Shares", "exchange": "NYSE", "status": "active", "tradable": true},
	{"symbol": "CCC", "name": "Ccc Ultra ProShares ETF", "exchange": "ARCA", "status": "active", "tradable": true},
	{"symbol": "DDDW", "name": "Ddd Acquisition Corp Warrants", "exchange": "NASDAQ", "status": "active", "tradable": true},
	{"symbol": "OTCX", "name": "Otc Thing", "exchange": "OTC", "status": "active", "tradable": false},
}

func assetsRoute(w http.ResponseWriter, r *http.Request) { writeJSON(w, testAssets) }

func TestReferenceTypes(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{"/v2/assets": assetsRoute}, time.Now())
	tks, err := c.Reference(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tk := range tks {
		got[tk.Symbol] = tk.Type
	}
	want := map[string]string{"AAA": "CS", "BBB": "ADRC", "CCC": "ETF", "DDDW": "WARRANT", "OTCX": "OTC"}
	for s, ty := range want {
		if got[s] != ty {
			t.Errorf("%s: type %q want %q", s, got[s], ty)
		}
	}
}

func TestSnapshotSeedsAndStaleDailyBar(t *testing.T) {
	// Tuesday 08:00 ET (pre-market).
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, etLoc)
	snaps := map[string]any{
		// Traded pre-market today: dailyBar is today's.
		"AAA": map[string]any{
			"latestTrade":  map[string]any{"t": "2026-10-27T11:59:58.5Z", "p": 2.5, "s": 100},
			"minuteBar":    map[string]any{"t": "2026-10-27T11:59:00Z", "o": 2.4, "h": 2.6, "l": 2.3, "c": 2.5, "v": 1000, "vw": 2.45},
			"dailyBar":     map[string]any{"t": "2026-10-27T04:00:00Z", "o": 2.0, "h": 2.6, "l": 1.9, "c": 2.5, "v": 50000, "vw": 2.3},
			"prevDailyBar": map[string]any{"t": "2026-10-26T04:00:00Z", "o": 1.5, "h": 2.1, "l": 1.5, "c": 2.0, "v": 9000, "vw": 1.8},
		},
		// Not traded yet today: dailyBar is YESTERDAY's -> it is the prev close.
		"BBB": map[string]any{
			"latestTrade":  map[string]any{"t": "2026-10-26T19:59:59Z", "p": 10.1, "s": 100},
			"dailyBar":     map[string]any{"t": "2026-10-26T04:00:00Z", "o": 9, "h": 10.5, "l": 8.9, "c": 10.0, "v": 70000, "vw": 9.8},
			"prevDailyBar": map[string]any{"t": "2026-10-23T04:00:00Z", "o": 8, "h": 9, "l": 8, "c": 8.5, "v": 1000, "vw": 8.4},
		},
	}
	var seen sync.Map
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"/v2/assets": assetsRoute,
		"/v2/stocks/snapshots": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("feed") != "sip" {
				t.Errorf("feed=%q", r.URL.Query().Get("feed"))
			}
			out := map[string]any{}
			for _, s := range strings.Split(r.URL.Query().Get("symbols"), ",") {
				seen.Store(s, true)
				if v, ok := snaps[s]; ok {
					out[s] = v
				}
			}
			writeJSON(w, out)
		},
	}, now)
	seeds, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen.Load("OTCX"); ok {
		t.Error("OTC symbol requested")
	}
	if _, ok := seen.Load("DDDW"); !ok {
		t.Error("chunking lost a symbol")
	}
	by := map[string]core.Seed{}
	for _, s := range seeds {
		by[s.Sym] = s
	}
	a := by["AAA"]
	if a.Close != 2.5 || a.Vol != 50000 || a.PrevClose != 2.0 || a.DayOpen != 2.0 || a.DayVWAP != 2.3 || a.High != 2.6 {
		t.Errorf("AAA seed %+v", a)
	}
	b := by["BBB"]
	if b.PrevClose != 10.0 || b.Vol != 0 || b.DayOpen != 0 || b.Close != 10.0 {
		t.Errorf("BBB (stale dailyBar) seed %+v", b)
	}
}

func TestGroupedDailyPaginatesAndChunks(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, etLoc)
	c, f := newFake(t, map[string]http.HandlerFunc{
		"/v2/assets": assetsRoute,
		"/v2/stocks/bars": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("timeframe") != "1Day" || q.Get("adjustment") != "split" || q.Get("feed") != "sip" {
				t.Errorf("bad params %v", q)
			}
			syms := strings.Split(q.Get("symbols"), ",")
			bar := func(c float64) map[string]any {
				return map[string]any{"t": "2026-10-27T04:00:00Z", "o": 1, "h": 2, "l": 0.5, "c": c, "v": 1234.0, "vw": 1.2, "n": 10}
			}
			// Page 1 has the first symbol, page 2 the rest.
			if q.Get("page_token") == "" {
				tok := "p2"
				writeJSON(w, map[string]any{"bars": map[string]any{syms[0]: []any{bar(1.5)}}, "next_page_token": tok})
				return
			}
			m := map[string]any{}
			for _, s := range syms[1:] {
				m[s] = []any{bar(1.7)}
			}
			writeJSON(w, map[string]any{"bars": m, "next_page_token": nil})
		},
	}, now)
	m, err := c.GroupedDaily(context.Background(), "2026-10-27")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"AAA", "BBB", "CCC", "DDDW"} {
		if d, ok := m[s]; !ok || d.Date != "2026-10-27" || d.V != 1234 {
			t.Errorf("%s: %+v ok=%v", s, d, ok)
		}
	}
	if _, ok := m["OTCX"]; ok {
		t.Error("OTC in grouped daily")
	}
	n := 0
	for _, u := range f.calls {
		if strings.Contains(u, "page_token=p2") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("want 2 second-page calls (2 chunks), got %d: %v", n, f.calls)
	}
}

func TestMinuteBarsPagingAndBounds(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, etLoc)
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"/v2/stocks/AAA/bars": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("timeframe") != "1Min" {
				t.Errorf("timeframe %q", q.Get("timeframe"))
			}
			end, _ := time.Parse(time.RFC3339, q.Get("end"))
			if end.After(now) {
				t.Errorf("end %v after now", end)
			}
			start, _ := time.Parse(time.RFC3339, q.Get("start"))
			if !start.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, etLoc)) {
				t.Errorf("start %v", start)
			}
			if q.Get("page_token") == "" {
				writeJSON(w, map[string]any{"bars": []any{map[string]any{"t": "2026-10-27T08:00:00Z", "o": 1, "h": 1, "l": 1, "c": 1, "v": 10.0}}, "next_page_token": "x"})
				return
			}
			writeJSON(w, map[string]any{"bars": []any{map[string]any{"t": "2026-10-28T08:00:00Z", "o": 2, "h": 2, "l": 2, "c": 2, "v": 20.0}}, "next_page_token": nil})
		},
	}, now)
	bars, err := c.IntradayWithPrevDay(context.Background(), "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[1].V != 20 || bars[0].T != time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("bars %+v", bars)
	}
}

func TestNewsMapsBenzingaIDsAndBodies(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"/v1beta1/news": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("include_content") != "true" || r.URL.Query().Get("symbols") != "AAA" {
				t.Errorf("query %v", r.URL.Query())
			}
			writeJSON(w, map[string]any{"news": []any{map[string]any{
				"id": 4242, "headline": "AAA soars", "summary": "s", "content": "<p>body</p>",
				"url": "https://x/1", "source": "benzinga", "created_at": "2026-10-27T12:00:00Z", "symbols": []string{"AAA"},
			}}, "next_page_token": nil})
		},
	}, time.Now())
	items, bodies, err := c.BenzingaNewsFor(context.Background(), "AAA", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	if items[0].ID != "bz4242" || items[0].Source != "Benzinga" || !items[0].HasBody || bodies["bz4242"] != "<p>body</p>" {
		t.Fatalf("item %+v bodies %v", items[0], bodies)
	}
}

func TestErrorsMapAndNeverLeakKeys(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"/v2/stocks/AAA/snapshot": func(w http.ResponseWriter, r *http.Request) {
			// A hostile body echoing the key must be scrubbed.
			http.Error(w, fmt.Sprintf(`{"message":"too many requests for %s"}`, testSecret), http.StatusTooManyRequests)
		},
	}, time.Now())
	_, err := c.PrevClose(context.Background(), "AAA")
	if !errors.Is(err, core.ErrRateLimited) {
		t.Fatalf("want rate-limited, got %v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("secret leaked: %v", err)
	}
	c.SetKey("bad:keys")
	_, err = c.PrevClose(context.Background(), "AAA")
	if !errors.Is(err, core.ErrNotEntitled) {
		t.Fatalf("want not-entitled for 403, got %v", err)
	}
	if _, err := c.Fundamentals(context.Background(), "AAA"); !errors.Is(err, core.ErrNotEntitled) {
		t.Fatalf("fundamentals: %v", err)
	}
}

// Intraday keeps each minute bar's vw: Engine.SeedScopeVwap rebuilds the
// session VWAP from it after a restart.
func TestIntradayKeepsBarVwap(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, etLoc)
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"/v2/stocks/AAA/bars": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"bars": []any{map[string]any{"t": "2026-10-28T14:00:00Z", "o": 2, "h": 3, "l": 1, "c": 2, "v": 20.0, "vw": 2.25}}, "next_page_token": nil})
		},
	}, now)
	bars, err := c.Intraday(context.Background(), "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].VW != 2.25 || bars[0].V != 20 {
		t.Fatalf("bars %+v", bars)
	}
}
