package persist

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// setupArchive persists Claude's structured bull-flag setup reads and serves
// them back as a cache. See core.SetupArchive for the contract — the key
// difference from tapeArchive is that this table is keyed, not appended: one
// row per (sym, as_of), so a symbol that re-enters a flag-filtered scan the
// same day reads back the stored answer instead of spending another call.
type setupArchive struct{ h *sql.DB }

func (r *setupArchive) SaveSetupRead(ctx context.Context, rd core.SetupRead) error {
	risks, err := json.Marshal(rd.Risks)
	if err != nil {
		return err
	}
	// Upsert on the (sym, as_of) primary key: a second read for the same
	// nightly classification (a manual re-read, or a retry after a failed one)
	// replaces it rather than erroring or leaving two answers to choose from.
	_, err = r.h.ExecContext(ctx, `
INSERT INTO setup_reads
  (sym, as_of, ts, quality, verdict, whats_happening, risks_json, invalidation, model, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sym, as_of) DO UPDATE SET
  ts = excluded.ts, quality = excluded.quality, verdict = excluded.verdict,
  whats_happening = excluded.whats_happening, risks_json = excluded.risks_json,
  invalidation = excluded.invalidation, model = excluded.model, error = excluded.error`,
		rd.Sym, rd.AsOf, rd.Ts, rd.Quality, rd.Verdict, rd.WhatsHappening,
		string(risks), rd.Invalidation, rd.Model, rd.Err)
	return err
}

// GetSetupRead is the cache lookup the worker runs BEFORE spending a Claude
// call. The match is exact on both parts of the key: a different as_of for the
// same symbol is a miss, which is what makes each fresh nightly classification
// earn a fresh read instead of inheriting yesterday's.
func (r *setupArchive) GetSetupRead(ctx context.Context, sym, asOf string) (core.SetupRead, bool, error) {
	row := r.h.QueryRowContext(ctx, `
SELECT sym, as_of, ts, quality, verdict, whats_happening, risks_json, invalidation, model, error
FROM setup_reads WHERE sym = ? AND as_of = ?`, sym, asOf)
	rd, err := scanSetupRead(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return core.SetupRead{}, false, nil
	}
	if err != nil {
		return core.SetupRead{}, false, err
	}
	return rd, true, nil
}

// RecentSetupReads returns a symbol's archived reads, newest first — the
// history source for reviewing how a name's setup was read over successive
// nightly classifications.
func (r *setupArchive) RecentSetupReads(ctx context.Context, sym string, limit int) ([]core.SetupRead, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.h.QueryContext(ctx, `
SELECT sym, as_of, ts, quality, verdict, whats_happening, risks_json, invalidation, model, error
FROM setup_reads WHERE sym = ? ORDER BY ts DESC LIMIT ?`, sym, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.SetupRead
	for rows.Next() {
		rd, err := scanSetupRead(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rd)
	}
	return out, rows.Err()
}

// scanSetupRead decodes one setup_reads row. It takes the Scan func rather than
// a *sql.Row/*sql.Rows so the single-row lookup and the list query cannot drift
// apart on column order — the two SELECTs share the same column list above.
func scanSetupRead(scan func(...any) error) (core.SetupRead, error) {
	var rd core.SetupRead
	var risks string
	if err := scan(&rd.Sym, &rd.AsOf, &rd.Ts, &rd.Quality, &rd.Verdict, &rd.WhatsHappening,
		&risks, &rd.Invalidation, &rd.Model, &rd.Err); err != nil {
		return core.SetupRead{}, err
	}
	if risks != "" {
		if err := json.Unmarshal([]byte(risks), &rd.Risks); err != nil {
			return core.SetupRead{}, err
		}
	}
	return rd, nil
}
