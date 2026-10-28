package feed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveKey loads the user's real Massive key the same way the app does
// (~/.marketscanner/apikey, BOM-tolerant), for the opt-in live tests below.
func liveKey(t *testing.T) string {
	t.Helper()
	if k := strings.TrimSpace(os.Getenv("MASSIVE_API_KEY")); k != "" {
		return k
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	b, err := os.ReadFile(filepath.Join(home, ".marketscanner", "apikey"))
	if err != nil {
		t.Skip("no apikey file")
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))
}

// TestLiveBenzinga verifies the real Benzinga expansion against the user's key:
// that it's entitled, and that our field mapping matches the live response.
// REST only — it does NOT open a WebSocket, so it can't disturb a running
// scanner instance (Massive allows ~1 WS connection per key).
//
//	SCANNER_LIVE_FEED_TEST=1 go test ./internal/feed -run TestLiveBenzinga -v
func TestLiveBenzinga(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live Benzinga test")
	}
	c := New(Config{APIKey: liveKey(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	since := time.Now().Add(-48 * time.Hour).UnixMilli()
	items, bodies, err := c.BenzingaNews(ctx, since)
	if err != nil {
		t.Fatalf("BenzingaNews failed (not entitled?): %v", err)
	}
	t.Logf("got %d articles, %d with full bodies", len(items), len(bodies))
	if len(items) == 0 {
		t.Fatal("no articles returned — expected some over the last 48h")
	}

	withTickers, withBody, withTeaser := 0, 0, 0
	for _, it := range items {
		if it.ID == "" || !strings.HasPrefix(it.ID, "bz") {
			t.Fatalf("bad ID mapping (expected bz<benzinga_id>): %q", it.ID)
		}
		if it.Headline == "" {
			t.Fatalf("headline missing on %s", it.ID)
		}
		if it.TS <= 0 {
			t.Fatalf("timestamp did not parse on %s (published field format?)", it.ID)
		}
		if len(it.Tickers) > 0 {
			withTickers++
		}
		if it.HasBody {
			withBody++
		}
		if it.Teaser != "" {
			withTeaser++
		}
	}
	newest := items[0]
	t.Logf("newest: %s | %s | tickers=%v | hasBody=%v",
		time.UnixMilli(newest.TS).Format(time.RFC3339), clipStr(newest.Headline, 70), newest.Tickers, newest.HasBody)
	t.Logf("coverage: %d/%d have tickers, %d have bodies, %d have teasers",
		withTickers, len(items), withBody, withTeaser)

	if withBody == 0 {
		t.Error("NO article carried a body — summaries would fall back to fetching URLs")
	}
	if withTickers == 0 {
		t.Error("NO article carried tickers — per-symbol news joins would never match")
	}
}

// TestLiveBenzingaForSymbol exercises the per-ticker fetch the auto-summarizer
// uses when a symbol enters a scan.
func TestLiveBenzingaForSymbol(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_FEED_TEST") == "" {
		t.Skip("set SCANNER_LIVE_FEED_TEST=1 to run the live Benzinga test")
	}
	sym := os.Getenv("SCANNER_LIVE_SYM")
	if sym == "" {
		sym = "AAPL"
	}
	c := New(Config{APIKey: liveKey(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	items, bodies, err := c.BenzingaNewsFor(ctx, sym, time.Now().Add(-7*24*time.Hour).UnixMilli())
	if err != nil {
		t.Fatalf("BenzingaNewsFor(%s) failed: %v", sym, err)
	}
	t.Logf("%s: %d articles over 7d, %d with bodies", sym, len(items), len(bodies))
	for i, it := range items {
		if i >= 3 {
			break
		}
		t.Logf("  %s | %s | %v", time.UnixMilli(it.TS).Format("01-02 15:04"), clipStr(it.Headline, 60), it.Tickers)
	}
	// The tickers= filter must actually constrain results.
	for _, it := range items {
		found := false
		for _, tk := range it.Tickers {
			if tk == sym {
				found = true
				break
			}
		}
		if !found && len(it.Tickers) > 0 {
			t.Errorf("tickers= filter leaked an unrelated article: %s tickers=%v", it.ID, it.Tickers)
		}
	}
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
