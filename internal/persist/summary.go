package persist

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// summaryRepo caches Claude news summaries (one row per article ID) so a
// repeated click or a restart never re-spends tokens on the same article.
type summaryRepo struct{ h *sql.DB }

func (r *summaryRepo) GetSummary(ctx context.Context, id string) (string, string, bool, error) {
	var s, model string
	err := r.h.QueryRowContext(ctx, `SELECT summary, model FROM news_summary WHERE id = ?`, id).Scan(&s, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return s, model, true, nil
}

func (r *summaryRepo) SaveSummary(ctx context.Context, id, sym, model, summary string) error {
	_, err := r.h.ExecContext(ctx, `
INSERT INTO news_summary (id, sym, model, summary, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET summary = excluded.summary, model = excluded.model`,
		id, sym, model, summary, time.Now().UnixMilli())
	return err
}
