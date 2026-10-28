package persist

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// newsArchive persists every article on first arrival plus every scan entry.
// See core.NewsArchive for the contract; the important invariant here is
// INSERT OR IGNORE — received_ms/via must describe the FIRST sighting, so a
// re-arrival (seen-map reset, overlap window, chaser double-cover) never
// overwrites the latency datum.
type newsArchive struct{ h *sql.DB }

func (r *newsArchive) SaveArticles(ctx context.Context, items []core.NewsItem, bodies map[string]string, via string, receivedMs int64) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO news_articles
  (id, headline, source, url, teaser, body, tickers, published_ms, received_ms, via)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.ID, it.Headline, it.Source, it.URL, it.Teaser, bodies[it.ID],
			strings.Join(it.Tickers, ","), it.TS, receivedMs, via)
		if err != nil {
			return err
		}
		// Only index tickers for rows we actually inserted.
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		for _, t := range it.Tickers {
			if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO news_article_tickers (ticker, id, published_ms) VALUES (?, ?, ?)`,
				t, it.ID, it.TS); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *newsArchive) Article(ctx context.Context, id string) (core.NewsItem, string, bool, error) {
	var it core.NewsItem
	var body, tickers string
	var score sql.NullInt64
	var tier, eventType, direction sql.NullString
	err := r.h.QueryRowContext(ctx, `
SELECT id, headline, source, url, teaser, body, tickers, published_ms,
       impact_score, impact_tier, event_type, impact_direction
FROM news_articles WHERE id = ?`, id).
		Scan(&it.ID, &it.Headline, &it.Source, &it.URL, &it.Teaser, &body, &tickers, &it.TS,
			&score, &tier, &eventType, &direction)
	if errors.Is(err, sql.ErrNoRows) {
		return core.NewsItem{}, "", false, nil
	}
	if err != nil {
		return core.NewsItem{}, "", false, err
	}
	if tickers != "" {
		it.Tickers = strings.Split(tickers, ",")
	}
	it.HasBody = body != ""
	applyScore(&it, score, tier, eventType, direction)
	return it, body, true, nil
}

func (r *newsArchive) ArticlesFor(ctx context.Context, sym string, limit int) ([]core.NewsItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.h.QueryContext(ctx, `
SELECT a.id, a.headline, a.source, a.url, a.teaser, a.tickers, a.published_ms, a.body != '',
       a.impact_score, a.impact_tier, a.event_type, a.impact_direction
FROM news_article_tickers t JOIN news_articles a ON a.id = t.id
WHERE t.ticker = ? ORDER BY t.published_ms DESC LIMIT ?`, sym, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.NewsItem
	for rows.Next() {
		var it core.NewsItem
		var tickers string
		var score sql.NullInt64
		var tier, eventType, direction sql.NullString
		if err := rows.Scan(&it.ID, &it.Headline, &it.Source, &it.URL, &it.Teaser, &tickers, &it.TS, &it.HasBody,
			&score, &tier, &eventType, &direction); err != nil {
			return nil, err
		}
		if tickers != "" {
			it.Tickers = strings.Split(tickers, ",")
		}
		applyScore(&it, score, tier, eventType, direction)
		out = append(out, it)
	}
	return out, rows.Err()
}

func applyScore(it *core.NewsItem, score sql.NullInt64, tier, eventType, direction sql.NullString) {
	if !score.Valid {
		return
	}
	n := int(score.Int64)
	it.Score = &n
	it.ScoreTier, it.EventType, it.Direction = tier.String, eventType.String, direction.String
}

// SetArticleScore attaches an RTPR Impact Score to an already-archived
// article. ok=false (zero rows affected) means the article isn't archived
// yet — the caller fetches it via the score event's signed permalink first.
func (r *newsArchive) SetArticleScore(ctx context.Context, id string, score int, tier, eventType, direction string) (bool, error) {
	res, err := r.h.ExecContext(ctx, `
UPDATE news_articles SET impact_score = ?, impact_tier = ?, event_type = ?, impact_direction = ?
WHERE id = ?`, score, tier, eventType, direction, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *newsArchive) EntryHitsMax(ctx context.Context, sinceMs int64) (map[string]map[string]int, error) {
	rows, err := r.h.QueryContext(ctx, `
SELECT scan_id, sym, MAX(hits) FROM scan_entries WHERE ts >= ? GROUP BY scan_id, sym`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]int)
	for rows.Next() {
		var scanID, sym string
		var n int
		if err := rows.Scan(&scanID, &sym, &n); err != nil {
			return nil, err
		}
		if out[scanID] == nil {
			out[scanID] = make(map[string]int)
		}
		out[scanID][sym] = n
	}
	return out, rows.Err()
}

func (r *newsArchive) SaveEntry(ctx context.Context, e core.ScanEntry) error {
	_, err := r.h.ExecContext(ctx, `
INSERT INTO scan_entries (ts, sym, scan_id, last, has_news, hits) VALUES (?, ?, ?, ?, ?, ?)`,
		e.At, e.Sym, e.ScanID, e.Last, b2i(e.HasNews), e.Hits)
	return err
}

func (r *newsArchive) PruneNews(ctx context.Context, beforeMs int64) error {
	for _, q := range []string{
		`DELETE FROM news_article_tickers WHERE published_ms < ?`,
		`DELETE FROM news_articles WHERE published_ms < ?`,
		`DELETE FROM scan_entries WHERE ts < ?`,
	} {
		if _, err := r.h.ExecContext(ctx, q, beforeMs); err != nil {
			return err
		}
	}
	return nil
}
