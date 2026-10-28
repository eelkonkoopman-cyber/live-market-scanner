package persist

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// fundamentalsRepo caches on-demand SEC-filed fundamentals per ticker (one row,
// whole struct as JSON — unlike float_ref this isn't queried in bulk, so there's
// no need to explode it into columns).
type fundamentalsRepo struct{ db *sql.DB }

func (r *fundamentalsRepo) GetFundamentals(ctx context.Context, sym string) (core.Fundamentals, bool, error) {
	var asOf int64
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT as_of, data_json FROM fundamentals_cache WHERE ticker = ?`, sym).Scan(&asOf, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Fundamentals{}, false, nil
	}
	if err != nil {
		return core.Fundamentals{}, false, err
	}
	var f core.Fundamentals
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return core.Fundamentals{}, false, err
	}
	return f, true, nil
}

func (r *fundamentalsRepo) SaveFundamentals(ctx context.Context, f core.Fundamentals) error {
	if f.AsOf == 0 {
		f.AsOf = time.Now().Unix()
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO fundamentals_cache (ticker, as_of, data_json)
VALUES (?, ?, ?)
ON CONFLICT(ticker) DO UPDATE SET as_of = excluded.as_of, data_json = excluded.data_json`,
		f.Sym, f.AsOf, string(raw))
	return err
}
