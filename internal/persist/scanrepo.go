package persist

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type scanRepo struct{ db *sql.DB }

func (r *scanRepo) List(ctx context.Context) ([]core.ScanDef, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT def_json FROM scan_defs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ScanDef
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var d core.ScanDef
		if err := json.Unmarshal([]byte(js), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *scanRepo) Save(ctx context.Context, d core.ScanDef) error {
	js, err := json.Marshal(d)
	if err != nil {
		return err
	}
	// Denormalize a few columns for cheap listing/filtering without parsing JSON.
	sortField, sortDir := "chg5m", "desc"
	if len(d.Sort) > 0 {
		sortField = d.Sort[0].Field
		if d.Sort[0].Dir != "" {
			sortDir = d.Sort[0].Dir
		}
	}
	now := time.Now().Unix()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO scan_defs(id,name,enabled,sort_field,sort_dir,top_n,def_json,created_at,updated_at,rev)
		VALUES(?,?,?,?,?,?,?,?,?,1)
		ON CONFLICT(id) DO UPDATE SET
		  name=excluded.name, enabled=excluded.enabled,
		  sort_field=excluded.sort_field, sort_dir=excluded.sort_dir,
		  top_n=excluded.top_n, def_json=excluded.def_json,
		  updated_at=excluded.updated_at, rev=scan_defs.rev+1`,
		d.ID, d.Name, b2i(d.Enabled), sortField, sortDir, d.Limit, string(js), now, now)
	return err
}

func (r *scanRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM scan_defs WHERE id=?`, id)
	return err
}
