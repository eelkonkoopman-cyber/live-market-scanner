package persist

import (
	"context"
	"database/sql"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type floatRepo struct{ db *sql.DB }

func (r *floatRepo) Floats(ctx context.Context) (map[string]core.FloatRef, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, shares_out, float_shares, as_of, source FROM float_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]core.FloatRef)
	for rows.Next() {
		var (
			ticker, source string
			so, fs         sql.NullInt64
			asOf           int64
		)
		if err := rows.Scan(&ticker, &so, &fs, &asOf, &source); err != nil {
			return nil, err
		}
		fr := core.FloatRef{Ticker: ticker, AsOf: asOf, Source: source}
		if so.Valid {
			v := so.Int64
			fr.SharesOut = &v
		}
		if fs.Valid {
			v := fs.Int64
			fr.FloatShares = &v
		}
		out[ticker] = fr
	}
	return out, rows.Err()
}

func (r *floatRepo) UpsertFloats(ctx context.Context, refs []core.FloatRef) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO float_ref(ticker,shares_out,float_shares,as_of,source)
		VALUES(?,?,?,?,?)
		ON CONFLICT(ticker) DO UPDATE SET
		  shares_out=excluded.shares_out, float_shares=excluded.float_shares,
		  as_of=excluded.as_of, source=excluded.source`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, fr := range refs {
		if _, err := stmt.ExecContext(ctx, fr.Ticker, nullInt(fr.SharesOut),
			nullInt(fr.FloatShares), fr.AsOf, fr.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StaleTickers returns universe members whose float row is missing or whose
// as_of is older than olderThanSec — i.e. those the weekly refresh should fetch.
func (r *floatRepo) StaleTickers(ctx context.Context, olderThanSec int64, universe []string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ticker, as_of FROM float_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	asOf := make(map[string]int64)
	for rows.Next() {
		var t string
		var a int64
		if err := rows.Scan(&t, &a); err != nil {
			return nil, err
		}
		asOf[t] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var stale []string
	for _, t := range universe {
		if a, ok := asOf[t]; !ok || a < olderThanSec {
			stale = append(stale, t)
		}
	}
	return stale, nil
}

// nullInt maps a *int64 to a driver value (NULL when nil).
func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// b2i converts a bool to 0/1 for INTEGER columns. Shared across repos.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
