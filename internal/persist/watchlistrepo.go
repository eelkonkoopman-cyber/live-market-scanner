package persist

import (
	"context"
	"database/sql"
)

type watchlistRepo struct{ db *sql.DB }

func (r *watchlistRepo) List(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT sym FROM watchlist ORDER BY pos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Replace swaps the whole list in one transaction (the scan upsert Save
// pattern's whole-value replace, applied list-wide): delete everything, then
// insert each symbol with its slice index as pos. All-or-nothing — a failed
// insert rolls back to the previous list rather than leaving it half-replaced.
func (r *watchlistRepo) Replace(ctx context.Context, syms []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM watchlist`); err != nil {
		return err
	}
	for i, s := range syms {
		if _, err := tx.ExecContext(ctx, `INSERT INTO watchlist(sym,pos) VALUES(?,?)`, s, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
