package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// trendRepo stores the daily OHLCV history behind the dtrend classification and
// the flag-pattern pass. Mirrors the volume-curve tables: (ticker, trade_date)
// rows, nightly upsert, pruned past ~45 calendar days.
type trendRepo struct{ h *sql.DB }

// UpsertDailyBars writes one date's bars. tradeDate — not each bar's own Date —
// is what lands in the trade_date column: the whole map belongs to a single
// grouped-daily fetch, and the row key must agree with HasClosesForDate /
// PruneDailyCloses, which are called with that same date.
func (r *trendRepo) UpsertDailyBars(ctx context.Context, tradeDate string, bars map[string]core.DailyBar) error {
	if len(bars) == 0 {
		return nil
	}
	tx, err := r.h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Batched multi-row inserts: ~12k single-row Execs per night would hold
	// the write lock noticeably longer for zero benefit.
	const batch = 400
	type kv struct {
		sym string
		b   core.DailyBar
	}
	rows := make([]kv, 0, len(bars))
	for s, b := range bars {
		rows = append(rows, kv{s, b})
	}
	for i := 0; i < len(rows); i += batch {
		end := min(i+batch, len(rows))
		var sb strings.Builder
		sb.WriteString(`INSERT OR REPLACE INTO daily_close_history (ticker, trade_date, close, open, high, low, volume) VALUES `)
		args := make([]any, 0, (end-i)*7)
		for j := i; j < end; j++ {
			if j > i {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?,?)")
			b := rows[j].b
			args = append(args, rows[j].sym, tradeDate, b.C, b.O, b.H, b.L, b.V)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("upsert daily bars: %w", err)
		}
	}
	return tx.Commit()
}

func (r *trendRepo) HasClosesForDate(ctx context.Context, tradeDate string) (bool, error) {
	var one int
	err := r.h.QueryRowContext(ctx,
		`SELECT 1 FROM daily_close_history WHERE trade_date = ? LIMIT 1`, tradeDate).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LoadBars is the ONLY read path over the daily history: the trend classifier
// wants closes and the pattern pass wants full bars, but a second closes-only
// query over the same ~12k x 32 rows would just double the nightly read for a
// projection the caller can do in a loop.
func (r *trendRepo) LoadBars(ctx context.Context, sinceDate string) (map[string][]core.DailyBar, error) {
	rows, err := r.h.QueryContext(ctx, `
SELECT ticker, trade_date, open, high, low, close, volume FROM daily_close_history
WHERE trade_date >= ? ORDER BY ticker, trade_date`, sinceDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]core.DailyBar, 12000)
	for rows.Next() {
		var sym string
		var b core.DailyBar
		if err := rows.Scan(&sym, &b.Date, &b.O, &b.H, &b.L, &b.C, &b.V); err != nil {
			return nil, err
		}
		out[sym] = append(out[sym], b)
	}
	return out, rows.Err()
}

// BarsFor is the per-symbol read path: same columns, same trade-date ASCENDING
// guarantee as LoadBars, but for ONE ticker. LoadBars stays the nightly
// whole-market path — this exists for the opposite shape of consumer, a Claude
// setup read on a single shortlisted symbol, where loading ~12k tickers' bars
// to use one of them would be absurd. The (ticker, trade_date) primary key
// makes it an index range scan, so it stays cheap even called per entrant.
func (r *trendRepo) BarsFor(ctx context.Context, sym, sinceDate string) ([]core.DailyBar, error) {
	rows, err := r.h.QueryContext(ctx, `
SELECT trade_date, open, high, low, close, volume FROM daily_close_history
WHERE ticker = ? AND trade_date >= ? ORDER BY trade_date`, sym, sinceDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DailyBar
	for rows.Next() {
		var b core.DailyBar
		if err := rows.Scan(&b.Date, &b.O, &b.H, &b.L, &b.C, &b.V); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *trendRepo) MaxDate(ctx context.Context) (string, error) {
	var d sql.NullString
	if err := r.h.QueryRowContext(ctx, `SELECT MAX(trade_date) FROM daily_close_history`).Scan(&d); err != nil {
		return "", err
	}
	return d.String, nil
}

func (r *trendRepo) PruneDailyCloses(ctx context.Context, beforeDate string) (int, error) {
	res, err := r.h.ExecContext(ctx,
		`DELETE FROM daily_close_history WHERE trade_date < ?`, beforeDate)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AvgDailyVolume returns each ticker's average daily volume over the last
// `days` trading dates stored BEFORE beforeDate (ET "2006-01-02"; today is
// excluded), keeping only tickers that traded on at least minDays of them.
// The window is the most recent `days` DISTINCT dates in the table, i.e. the
// trading days the whole-market daily fetch recorded. Volumes are Massive's
// daily bars, which include pre-market and after-hours trading.
func (d *DB) AvgDailyVolume(ctx context.Context, beforeDate string, days, minDays int) (map[string]float64, error) {
	rows, err := d.sql.QueryContext(ctx, `
WITH win AS (
  SELECT DISTINCT trade_date FROM daily_close_history
  WHERE trade_date < ? ORDER BY trade_date DESC LIMIT ?
)
SELECT ticker, AVG(volume) FROM daily_close_history
WHERE trade_date IN (SELECT trade_date FROM win)
GROUP BY ticker HAVING COUNT(*) >= ?`, beforeDate, days, minDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64, 12000)
	for rows.Next() {
		var sym string
		var avg float64
		if err := rows.Scan(&sym, &avg); err != nil {
			return nil, err
		}
		if avg > 0 {
			out[sym] = avg
		}
	}
	return out, rows.Err()
}
