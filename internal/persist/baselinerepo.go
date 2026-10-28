package persist

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type baselineRepo struct{ db *sql.DB }

// LoadBaseline reads rvol_baseline into the RAM-resident table the hot path uses.
func (r *baselineRepo) LoadBaseline(ctx context.Context) (*core.BaselineTable, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, bucket, baseline_cum, sample_count FROM rvol_baseline`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBaseline(rows)
}

func scanBaseline(rows *sql.Rows) (*core.BaselineTable, error) {
	cum := make(map[string][core.BucketCount]float64)
	cnt := make(map[string][core.BucketCount]uint16)
	for rows.Next() {
		var (
			ticker string
			bucket int
			bc     float64
			sc     int
		)
		if err := rows.Scan(&ticker, &bucket, &bc, &sc); err != nil {
			return nil, err
		}
		if bucket < 0 || bucket >= core.BucketCount {
			continue
		}
		a := cum[ticker]
		a[bucket] = bc
		cum[ticker] = a
		c := cnt[ticker]
		c[bucket] = uint16(sc)
		cnt[ticker] = c
	}
	return &core.BaselineTable{Cum: cum, Count: cnt}, rows.Err()
}

// UpsertVolumeCurves MERGES a day's per-ticker cumulative 5-min curves (ticker
// -> cumulative volume by bucket index 0..len-1) into history: per (ticker,
// date, bucket) the larger value wins. Cumulative volume only grows within a
// day, so a periodic save, a restarted process (which never saw the morning),
// the nightly flush and a minute-bar backfill can all write the same day
// without one wiping another's buckets. Non-positive values mean "not
// observed" and are skipped.
func (r *baselineRepo) UpsertVolumeCurves(ctx context.Context, tradeDate string, curves map[string][]int64) error {
	if len(curves) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := mergeCurvesTx(ctx, tx, tradeDate, curves); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeCurvesTx is UpsertVolumeCurves' body, shared with SaveFilledCurves.
func mergeCurvesTx(ctx context.Context, tx *sql.Tx, tradeDate string, curves map[string][]int64) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO volume_curve_history(ticker,trade_date,bucket,cum_vol)
		VALUES(?,?,?,?)
		ON CONFLICT(ticker,trade_date,bucket) DO UPDATE SET cum_vol=MAX(cum_vol, excluded.cum_vol)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for ticker, cum := range curves {
		for b, v := range cum {
			// <= 0 is "not observed" (before the engine first saw the ticker, after
			// the current bucket, or a bucket a save already covered) — storing it
			// would average zeros into the baseline and inflate RVOL.
			if v <= 0 {
				continue
			}
			if _, err := stmt.ExecContext(ctx, ticker, tradeDate, b, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *baselineRepo) HasCurveForDate(ctx context.Context, tradeDate string) (bool, error) {
	var x int
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM volume_curve_history WHERE trade_date=?)`, tradeDate).Scan(&x)
	if err != nil {
		return false, err
	}
	return x == 1, nil
}

// TrailingTradingDays returns up to n trading days <= asOf, most recent first.
func (r *baselineRepo) TrailingTradingDays(ctx context.Context, asOf string, n int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT trade_date FROM market_calendar
		WHERE is_trading=1 AND trade_date<=? ORDER BY trade_date DESC LIMIT ?`, asOf, n)
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

// RecomputeBaseline rebuilds rvol_baseline as the per-(ticker,bucket) mean of
// cumulative volume across the given window of trade dates. The refresh is
// DELETE + INSERT…SELECT inside one tx, preserving the table DDL (no DROP/RENAME)
// and leaving the old baseline intact if anything fails before commit.
func (r *baselineRepo) RecomputeBaseline(ctx context.Context, asOf string, window []string) (int, error) {
	if len(window) == 0 {
		return 0, nil
	}
	ph := make([]string, len(window))
	dateArgs := make([]any, len(window))
	for i, d := range window {
		ph[i] = "?"
		dateArgs[i] = d
	}
	now := time.Now().Unix()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM rvol_baseline`); err != nil {
		return 0, err
	}
	q := `INSERT INTO rvol_baseline(ticker,bucket,baseline_cum,sample_count,computed_for,updated_at)
	      SELECT ticker, bucket, AVG(CAST(cum_vol AS REAL)), COUNT(*), ?, ?
	      FROM volume_curve_history
	      WHERE trade_date IN (` + strings.Join(ph, ",") + `) AND cum_vol > 0
	      GROUP BY ticker, bucket`
	args := append([]any{asOf, now}, dateArgs...)
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// RecomputeBaselineFor is RecomputeBaseline restricted to the given tickers:
// their rows are deleted and rebuilt in one tx (a primary-key range per
// ticker, so it stays fast however much history the window holds), then read
// back as a partial table.
func (r *baselineRepo) RecomputeBaselineFor(ctx context.Context, asOf string, window, tickers []string) (*core.BaselineTable, error) {
	if len(window) == 0 || len(tickers) == 0 {
		return &core.BaselineTable{}, nil
	}
	tph, targs := inList(tickers)
	dph, dargs := inList(window)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rvol_baseline WHERE ticker IN (`+tph+`)`, targs...); err != nil {
		return nil, err
	}
	args := append([]any{asOf, time.Now().Unix()}, targs...)
	args = append(args, dargs...)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rvol_baseline(ticker,bucket,baseline_cum,sample_count,computed_for,updated_at)
		SELECT ticker, bucket, AVG(CAST(cum_vol AS REAL)), COUNT(*), ?, ?
		FROM volume_curve_history
		WHERE ticker IN (`+tph+`) AND trade_date IN (`+dph+`) AND cum_vol > 0
		GROUP BY ticker, bucket`, args...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, bucket, baseline_cum, sample_count FROM rvol_baseline WHERE ticker IN (`+tph+`)`, targs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBaseline(rows)
}

func (r *baselineRepo) PruneVolumeCurves(ctx context.Context, beforeDate string) (int, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM volume_curve_history WHERE trade_date<?`, beforeDate)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// The backfill markers go with the curves they describe.
	if _, err := r.db.ExecContext(ctx, `DELETE FROM curve_filled WHERE trade_date<?`, beforeDate); err != nil {
		return int(n), err
	}
	return int(n), nil
}

func (r *baselineRepo) SetBaselineStatus(ctx context.Context, status, computedFor string) error {
	if err := r.setMeta(ctx, "baseline_status", status); err != nil {
		return err
	}
	return r.setMeta(ctx, "baseline_computed_for", computedFor)
}

func (r *baselineRepo) GetBaselineStatus(ctx context.Context) (status, computedFor string, err error) {
	if status, err = r.getMeta(ctx, "baseline_status"); err != nil {
		return "", "", err
	}
	if computedFor, err = r.getMeta(ctx, "baseline_computed_for"); err != nil {
		return "", "", err
	}
	if status == "" {
		status = "building" // never recomputed yet
	}
	return status, computedFor, nil
}

func (r *baselineRepo) setMeta(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO schema_meta(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (r *baselineRepo) getMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
