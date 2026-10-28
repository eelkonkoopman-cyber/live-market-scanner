package persist

import (
	"context"
	"database/sql"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type calendarRepo struct{ db *sql.DB }

func (r *calendarRepo) Sessions(ctx context.Context) ([]core.MarketDay, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT trade_date, is_trading, is_half_day, sess_open_min, reg_open_min,
		       reg_close_min, sess_close_min, last_bucket
		FROM market_calendar ORDER BY trade_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MarketDay
	for rows.Next() {
		var (
			m              core.MarketDay
			trading, halfd int
		)
		if err := rows.Scan(&m.Date, &trading, &halfd, &m.SessOpenMin, &m.RegOpenMin,
			&m.RegCloseMin, &m.SessCloseMin, &m.LastBucket); err != nil {
			return nil, err
		}
		m.IsTrading = trading == 1
		m.IsHalfDay = halfd == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *calendarRepo) Upsert(ctx context.Context, days []core.MarketDay) error {
	if len(days) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO market_calendar(trade_date,is_trading,is_half_day,sess_open_min,
		    reg_open_min,reg_close_min,sess_close_min,last_bucket,source,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
		  is_trading=excluded.is_trading, is_half_day=excluded.is_half_day,
		  sess_open_min=excluded.sess_open_min, reg_open_min=excluded.reg_open_min,
		  reg_close_min=excluded.reg_close_min, sess_close_min=excluded.sess_close_min,
		  last_bucket=excluded.last_bucket, source=excluded.source,
		  updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range days {
		if _, err := stmt.ExecContext(ctx, m.Date, b2i(m.IsTrading), b2i(m.IsHalfDay),
			m.SessOpenMin, m.RegOpenMin, m.RegCloseMin, m.SessCloseMin, m.LastBucket,
			"import", now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
