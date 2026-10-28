package persist

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// DefaultSessionStatsKeepDates is how many distinct trading dates of boundary
// session snapshots PruneBefore retains when the caller passes <= 0. Ten dates
// is two full trading weeks of morning reviews at three sessions a day —
// roughly 60-120k rows, trivial for SQLite and far more history than the
// "what gapped after hours yesterday" workflow ever pages back through.
const DefaultSessionStatsKeepDates = 10

// sessionStatsRepo persists the engine's boundary session snapshots (the
// morning-review source). One RAW row per traded symbol per completed
// sub-session, keyed (date, session, sym); percentages are never stored — the
// consumer derives them at read time from these values.
type sessionStatsRepo struct{ db *sql.DB }

// sessionStatsBatch is the multi-row insert width: 9 columns keeps 400 rows
// at 3600 bound variables, the same headroom trendRepo's 400x7 batches use
// under SQLite's variable limit.
const sessionStatsBatch = 400

// SaveSnapshot replaces the (Date, Session) key's rows wholesale in one
// transaction: delete the boundary's previous rows (a re-freeze with FEWER
// traded symbols must not leave stale extras behind), then bulk-insert the
// new set through ONE prepared multi-row statement reused for every full
// batch (the trendRepo batching shape — one row per Exec would spend ~11k
// driver round-trips and blow the well-under-a-second budget the boundary
// writer runs on). One write-lock acquisition; INSERT OR REPLACE keeps even
// a duplicate symbol harmless. All-or-nothing — a failure rolls back to the
// previous snapshot rather than leaving the boundary half-written.
func (r *sessionStatsRepo) SaveSnapshot(ctx context.Context, snap core.SessionSnapshot) error {
	if snap.Date == "" {
		return fmt.Errorf("session stats save: empty date")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	sess := snap.Session.String()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM session_stats WHERE date = ? AND session = ?`, snap.Date, sess); err != nil {
		return err
	}
	var full *sql.Stmt // prepared lazily: a small snapshot may have no full batch
	defer func() {
		if full != nil {
			_ = full.Close()
		}
	}()
	args := make([]any, 0, sessionStatsBatch*9)
	for i := 0; i < len(snap.Rows); i += sessionStatsBatch {
		end := min(i+sessionStatsBatch, len(snap.Rows))
		args = args[:0]
		for _, row := range snap.Rows[i:end] {
			args = append(args, snap.Date, sess, row.Sym,
				row.Hi, row.Lo, row.Close, row.Vwap, row.Vol, row.PrevClose)
		}
		if end-i == sessionStatsBatch {
			if full == nil {
				if full, err = tx.PrepareContext(ctx, sessionStatsInsertSQL(sessionStatsBatch)); err != nil {
					return err
				}
			}
			if _, err := full.ExecContext(ctx, args...); err != nil {
				return fmt.Errorf("insert session stats batch: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, sessionStatsInsertSQL(end-i), args...); err != nil {
			return fmt.Errorf("insert session stats tail: %w", err)
		}
	}
	return tx.Commit()
}

// sessionStatsInsertSQL builds the n-row INSERT OR REPLACE statement text.
func sessionStatsInsertSQL(n int) string {
	var sb strings.Builder
	sb.WriteString(`INSERT OR REPLACE INTO session_stats (date, session, sym, hi, lo, close, vwap, vol, prev_close) VALUES `)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?,?,?,?,?)")
	}
	return sb.String()
}

// Dates returns the distinct trading dates on hand, newest first — the review
// window's date-picker list. Lexical DESC on "2006-01-02" strings is
// chronological DESC.
func (r *sessionStatsRepo) Dates(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT date FROM session_stats ORDER BY date DESC`)
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

// Load returns one (date, session)'s rows ordered by symbol. The (date,
// session, sym) primary key makes it an index range scan. An unknown pair is
// nil slice, nil error — "no data" is a normal answer, not a failure.
func (r *sessionStatsRepo) Load(ctx context.Context, date string, session core.Session) ([]core.SessionSnapshotRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT sym, hi, lo, close, vwap, vol, prev_close FROM session_stats
WHERE date = ? AND session = ? ORDER BY sym`, date, session.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.SessionSnapshotRow
	for rows.Next() {
		var row core.SessionSnapshotRow
		if err := rows.Scan(&row.Sym, &row.Hi, &row.Lo, &row.Close, &row.Vwap, &row.Vol, &row.PrevClose); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PruneBefore keeps only the newest keepDates distinct dates (<= 0 uses
// DefaultSessionStatsKeepDates), deleting every row of every older date, and
// reports the deleted row count (the PruneDailyCloses shape).
func (r *sessionStatsRepo) PruneBefore(ctx context.Context, keepDates int) (int, error) {
	if keepDates <= 0 {
		keepDates = DefaultSessionStatsKeepDates
	}
	res, err := r.db.ExecContext(ctx, `
DELETE FROM session_stats WHERE date NOT IN (
  SELECT DISTINCT date FROM session_stats ORDER BY date DESC LIMIT ?
)`, keepDates)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
