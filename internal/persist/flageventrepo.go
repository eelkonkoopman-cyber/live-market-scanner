package persist

import (
	"context"
	"database/sql"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type flagEventRepo struct{ db *sql.DB }

// Insert records evs under day in one transaction. INSERT OR IGNORE: the
// (sym, ts, kind) key makes a repeated signal a no-op, so the first recording
// (and its At, Note and Judged) wins.
func (r *flagEventRepo) Insert(ctx context.Context, day string, evs []core.FlagEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	st, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO flag_events(day,sym,ts,kind,label,price,at,note,judged) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, e := range evs {
		var price any // nil -> NULL
		if e.Price != nil {
			price = *e.Price
		}
		if _, err := st.ExecContext(ctx, day, e.Sym, e.Ts, e.Kind, e.Label, price, e.At, e.Note, e.Judged); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *flagEventRepo) Day(ctx context.Context, day string) ([]core.FlagEvent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT sym,ts,kind,label,price,at,note,judged FROM flag_events WHERE day=? ORDER BY ts, rowid`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.FlagEvent
	for rows.Next() {
		var (
			e core.FlagEvent
			p sql.NullFloat64
		)
		if err := rows.Scan(&e.Sym, &e.Ts, &e.Kind, &e.Label, &p, &e.At, &e.Note, &e.Judged); err != nil {
			return nil, err
		}
		if p.Valid {
			v := p.Float64
			e.Price = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *flagEventRepo) Days(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT day FROM flag_events ORDER BY day DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Prune deletes every day strictly older than today minus keepDays. The cutoff
// is computed here as a calendar date (YYYY-MM-DD sorts as text).
func (r *flagEventRepo) Prune(ctx context.Context, keepDays int, today string) error {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return err
	}
	cutoff := t.AddDate(0, 0, -keepDays).Format("2006-01-02")
	_, err = r.db.ExecContext(ctx, `DELETE FROM flag_events WHERE day < ?`, cutoff)
	return err
}
