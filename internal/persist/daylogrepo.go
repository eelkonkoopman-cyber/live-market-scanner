package persist

import (
	"context"
	"database/sql"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type dayLogRepo struct{ db *sql.DB }

func nf(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func ni(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func fp(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}

func ip(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

// InsertAlert stores one alert and returns its id. Alerts are never deduplicated:
// two fires of the same scan on the same name are two rows, as they were two alerts.
func (r *dayLogRepo) InsertAlert(ctx context.Context, a core.DayAlert) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO day_alerts(day,at,sym,scan_id,scan_name,kind,price,chg_day,vol,rvol,news_hl,news_age,note,blacklisted,out5,out15,out30,max_up,max_down,out_done)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Day, a.At, a.Sym, a.ScanID, a.ScanName, a.Kind, nf(a.Price), nf(a.ChgDay), ni(a.Vol), nf(a.Rvol),
		a.NewsHL, ni(a.NewsAge), a.Note, b2i(a.Blacklisted), nf(a.Out5), nf(a.Out15), nf(a.Out30), nf(a.MaxUp), nf(a.MaxDown), b2i(a.Done))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const dayAlertCols = `id,day,at,sym,scan_id,scan_name,kind,price,chg_day,vol,rvol,news_hl,news_age,note,blacklisted,out5,out15,out30,max_up,max_down,out_done`

func (r *dayLogRepo) query(ctx context.Context, where string, args ...any) ([]core.DayAlert, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+dayAlertCols+` FROM day_alerts WHERE `+where+` ORDER BY at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DayAlert
	for rows.Next() {
		var (
			a                                    core.DayAlert
			price, chg, rvol, o5, o15, o30, u, d sql.NullFloat64
			vol, age                             sql.NullInt64
			bl, done                             int
		)
		if err := rows.Scan(&a.ID, &a.Day, &a.At, &a.Sym, &a.ScanID, &a.ScanName, &a.Kind, &price, &chg, &vol, &rvol,
			&a.NewsHL, &age, &a.Note, &bl, &o5, &o15, &o30, &u, &d, &done); err != nil {
			return nil, err
		}
		a.Price, a.ChgDay, a.Vol, a.Rvol, a.NewsAge = fp(price), fp(chg), ip(vol), fp(rvol), ip(age)
		a.Out5, a.Out15, a.Out30, a.MaxUp, a.MaxDown = fp(o5), fp(o15), fp(o30), fp(u), fp(d)
		a.Blacklisted, a.Done = bl != 0, done != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *dayLogRepo) Alerts(ctx context.Context, day string) ([]core.DayAlert, error) {
	return r.query(ctx, `day=?`, day)
}

func (r *dayLogRepo) PendingAlerts(ctx context.Context, day string) ([]core.DayAlert, error) {
	return r.query(ctx, `day=? AND out_done=0`, day)
}

func (r *dayLogRepo) SetOutcome(ctx context.Context, id int64, o core.DayOutcome) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE day_alerts SET out5=?,out15=?,out30=?,max_up=?,max_down=?,out_done=? WHERE id=?`,
		nf(o.Out5), nf(o.Out15), nf(o.Out30), nf(o.MaxUp), nf(o.MaxDown), b2i(o.Done), id)
	return err
}

// ReplaceMovers swaps the day's gainers / losers snapshot for ms in one transaction.
func (r *dayLogRepo) ReplaceMovers(ctx context.Context, day string, ms []core.DayMover) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM day_movers WHERE day=?`, day); err != nil {
		return err
	}
	for _, m := range ms {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO day_movers(day,side,rank,sym,price,chg_day,vol,headline,taken_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			day, m.Side, m.Rank, m.Sym, nf(m.Price), nf(m.ChgDay), ni(m.Vol), m.Headline, m.TakenAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Movers returns the day's snapshot: gainers first (rank order), then losers.
func (r *dayLogRepo) Movers(ctx context.Context, day string) ([]core.DayMover, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT side,rank,sym,price,chg_day,vol,headline,taken_at FROM day_movers WHERE day=? ORDER BY side='loss', rank`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DayMover
	for rows.Next() {
		var (
			m    core.DayMover
			p, c sql.NullFloat64
			v    sql.NullInt64
		)
		if err := rows.Scan(&m.Side, &m.Rank, &m.Sym, &p, &c, &v, &m.Headline, &m.TakenAt); err != nil {
			return nil, err
		}
		m.Day, m.Price, m.ChgDay, m.Vol = day, fp(p), fp(c), ip(v)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Days lists the days that have alerts or movers, newest first.
func (r *dayLogRepo) Days(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT day FROM (SELECT day FROM day_alerts UNION SELECT day FROM day_movers) ORDER BY day DESC LIMIT ?`, limit)
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
