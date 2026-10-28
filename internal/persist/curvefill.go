package persist

import (
	"context"
	"strings"
)

// FilledCurveDates returns, per ticker, which of the given dates already had
// their curve rebuilt from minute bars (see SaveFilledCurves).
func (d *DB) FilledCurveDates(ctx context.Context, dates []string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(dates) == 0 {
		return out, nil
	}
	ph, args := inList(dates)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ticker, trade_date FROM curve_filled WHERE trade_date IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sym, day string
		if err := rows.Scan(&sym, &day); err != nil {
			return nil, err
		}
		if out[sym] == nil {
			out[sym] = map[string]bool{}
		}
		out[sym][day] = true
	}
	return out, rows.Err()
}

// DailyVolumes returns one ticker's daily volume (daily_close_history, which
// includes the closing auction and late prints) on each of the given dates it
// has a row for.
func (d *DB) DailyVolumes(ctx context.Context, ticker string, dates []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(dates) == 0 {
		return out, nil
	}
	ph, args := inList(dates)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT trade_date, volume FROM daily_close_history WHERE ticker=? AND trade_date IN (`+ph+`)`,
		append([]any{ticker}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var vol float64
		if err := rows.Scan(&day, &vol); err != nil {
			return nil, err
		}
		out[day] = int64(vol)
	}
	return out, rows.Err()
}

// SaveFilledCurves merges one ticker's rebuilt curves (date -> cumulative
// volume by bucket) into volume_curve_history with UpsertVolumeCurves' rules
// (larger value wins, <= 0 skipped) and marks EVERY date in dates as filled,
// including dates with no curve (the ticker didn't trade), so none is fetched
// again. One transaction: a date is never marked without its data.
func (d *DB) SaveFilledCurves(ctx context.Context, ticker string, curves map[string][]int64, dates []string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for day, cv := range curves {
		if err := mergeCurvesTx(ctx, tx, day, map[string][]int64{ticker: cv}); err != nil {
			return err
		}
	}
	for _, day := range dates {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO curve_filled(ticker, trade_date) VALUES(?, ?)`, ticker, day); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func inList(vals []string) (string, []any) {
	ph := make([]string, len(vals))
	args := make([]any, len(vals))
	for i, v := range vals {
		ph[i], args[i] = "?", v
	}
	return strings.Join(ph, ","), args
}
