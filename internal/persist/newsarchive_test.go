package persist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func newArchive(t *testing.T) core.NewsArchive {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.NewsArchive()
}

func TestArchiveFirstSightingWins(t *testing.T) {
	ctx := context.Background()
	r := newArchive(t)

	it := core.NewsItem{
		ID: "bz1", Headline: "Acme FDA nod", Source: "Benzinga",
		URL: "https://b.test/1", Teaser: "t", Tickers: []string{"ACME", "BETA"}, TS: 1000,
	}
	if err := r.SaveArticles(ctx, []core.NewsItem{it}, map[string]string{"bz1": "<p>body</p>"}, "push", 1025); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Re-arrival via a different source must NOT overwrite the first-sighting
	// latency record — received_ms/via are the measurement.
	if err := r.SaveArticles(ctx, []core.NewsItem{it}, nil, "poll", 9999); err != nil {
		t.Fatalf("resave: %v", err)
	}

	got, body, ok, err := r.Article(ctx, "bz1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if body != "<p>body</p>" {
		t.Fatalf("body lost on re-arrival: %q", body)
	}
	if got.Headline != "Acme FDA nod" || len(got.Tickers) != 2 || got.Tickers[1] != "BETA" || got.TS != 1000 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if !got.HasBody {
		t.Fatal("HasBody should be derived from the stored body")
	}
}

func TestArchiveArticlesForAndEntryHitsMax(t *testing.T) {
	ctx := context.Background()
	r := newArchive(t)

	items := []core.NewsItem{
		{ID: "n1", Headline: "old ACME story", Source: "W", URL: "u1", Tickers: []string{"ACME"}, TS: 1000},
		{ID: "n2", Headline: "new ACME story", Source: "W", URL: "u2", Tickers: []string{"ACME", "BETA"}, TS: 2000},
		{ID: "n3", Headline: "unrelated", Source: "W", URL: "u3", Tickers: []string{"OTHER"}, TS: 3000},
	}
	if err := r.SaveArticles(ctx, items, map[string]string{"n2": "<p>b</p>"}, "poll", 3001); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := r.ArticlesFor(ctx, "ACME", 10)
	if err != nil {
		t.Fatalf("ArticlesFor: %v", err)
	}
	// Newest first, only ACME's, HasBody derived from the stored body.
	if len(got) != 2 || got[0].ID != "n2" || got[1].ID != "n1" {
		t.Fatalf("want [n2 n1], got %+v", got)
	}
	if !got[0].HasBody || got[1].HasBody {
		t.Fatalf("HasBody: n2 should have one, n1 not: %+v", got)
	}
	if len(got[0].Tickers) != 2 || got[0].Tickers[1] != "BETA" {
		t.Fatalf("tickers roundtrip: %+v", got[0].Tickers)
	}

	// EntryHitsMax takes the MAX archived Hits per scan/sym, NOT a count of
	// entries — a scan gated by its own hits filter only archives an entry
	// once the threshold is met (e.g. entries at base-hits 2 and 3, never 1),
	// so counting rows would undercount the true recurrence; the max archived
	// value is exactly the last known counter, since hits only rises in a day.
	for _, e := range []core.ScanEntry{
		{ScanID: "s1", Sym: "ACME", At: 500, Hits: 9}, // before the cutoff -> excluded
		{ScanID: "s1", Sym: "ACME", At: 1500, Hits: 2},
		{ScanID: "s1", Sym: "ACME", At: 2500, Hits: 3},
		{ScanID: "s2", Sym: "ACME", At: 2600, Hits: 1},
	} {
		if err := r.SaveEntry(ctx, e); err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	counts, err := r.EntryHitsMax(ctx, 1000)
	if err != nil {
		t.Fatalf("EntryHitsMax: %v", err)
	}
	if counts["s1"]["ACME"] != 3 || counts["s2"]["ACME"] != 1 {
		t.Fatalf("want s1/ACME=3 (max of 2,3; the Hits=9@500 excluded by the since bound), s2/ACME=1; got %+v", counts)
	}
}

func TestArchiveArticleScore(t *testing.T) {
	ctx := context.Background()
	r := newArchive(t)

	it := core.NewsItem{ID: "rtpr1", Headline: "h", Tickers: []string{"BMRA"}, TS: 1000}
	if err := r.SaveArticles(ctx, []core.NewsItem{it}, nil, "rtpr", 1200); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Before scoring: Score must be nil, not a zero-value int (0 is a real score).
	got, _, ok, err := r.Article(ctx, "rtpr1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Score != nil {
		t.Fatalf("unscored article must have a nil Score, got %v", *got.Score)
	}

	if ok, err := r.SetArticleScore(ctx, "rtpr1", 92, "high", "fda_approval", "bullish"); err != nil || !ok {
		t.Fatalf("SetArticleScore: ok=%v err=%v", ok, err)
	}
	got, _, ok, err = r.Article(ctx, "rtpr1")
	if err != nil || !ok {
		t.Fatalf("get after score: ok=%v err=%v", ok, err)
	}
	if got.Score == nil || *got.Score != 92 || got.ScoreTier != "high" ||
		got.EventType != "fda_approval" || got.Direction != "bullish" {
		t.Fatalf("scored fields mismatch: %+v", got)
	}

	// ArticlesFor (the history path used by news.for) must carry the score too.
	list, err := r.ArticlesFor(ctx, "BMRA", 10)
	if err != nil || len(list) != 1 || list[0].Score == nil || *list[0].Score != 92 {
		t.Fatalf("ArticlesFor must include the score: list=%+v err=%v", list, err)
	}

	// Scoring an article that was never archived must report ok=false, not
	// silently succeed — the caller uses this to decide whether to fetch it.
	if ok, err := r.SetArticleScore(ctx, "never-seen", 50, "low", "x", "neutral"); err != nil || ok {
		t.Fatalf("scoring an unarchived id must report ok=false, got ok=%v err=%v", ok, err)
	}
}

func TestArchivePrune(t *testing.T) {
	ctx := context.Background()
	r := newArchive(t)

	old := core.NewsItem{ID: "old", Headline: "h", Tickers: []string{"A"}, TS: 1000}
	fresh := core.NewsItem{ID: "new", Headline: "h", Tickers: []string{"A"}, TS: 5000}
	if err := r.SaveArticles(ctx, []core.NewsItem{old, fresh}, nil, "poll", 5001); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := r.SaveEntry(ctx, core.ScanEntry{ScanID: "s1", Sym: "A", At: 1500, Last: 2}); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if err := r.SaveEntry(ctx, core.ScanEntry{ScanID: "s1", Sym: "A", At: 6000, Last: 3}); err != nil {
		t.Fatalf("entry: %v", err)
	}

	if err := r.PruneNews(ctx, 3000); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, _, ok, _ := r.Article(ctx, "old"); ok {
		t.Fatal("old article should be pruned")
	}
	if _, _, ok, _ := r.Article(ctx, "new"); !ok {
		t.Fatal("new article must survive the prune")
	}
}
