package persist

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// tapeArchive persists tape-reading regime events + reads. See
// core.TapeArchive for the contract — the Go mirror of the standalone Python
// tape-reader app's recorder.py, but one shared tape_reads table across every
// symbol (via the sym column) instead of recorder.py's
// one-SQLite-file-per-symbol layout.
type tapeArchive struct{ h *sql.DB }

func (r *tapeArchive) SaveTapeRead(ctx context.Context, event core.RegimeEvent, verdict core.TapeVerdict, whatsHappening, watchFor, model, errMsg string) error {
	evidence, err := json.Marshal(verdict.Evidence)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(event.Snapshot)
	if err != nil {
		return err
	}
	_, err = r.h.ExecContext(ctx, `
INSERT INTO tape_reads
  (ts, sym, trigger, headline, control, confidence, whats_happening, evidence_json, watch_for, model, error, snapshot_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Ts, event.Sym, event.Trigger, event.Headline,
		verdict.Control, verdict.Confidence, whatsHappening,
		string(evidence), watchFor, model, errMsg, string(snapshot))
	return err
}

// Recent returns a symbol's archived reads, newest first — the history source
// for reviewing a session's regime shifts after the fact.
func (r *tapeArchive) Recent(ctx context.Context, sym string, limit int) ([]core.TapeReadRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.h.QueryContext(ctx, `
SELECT ts, sym, trigger, headline, control, confidence, whats_happening, evidence_json, watch_for, model, error, snapshot_json
FROM tape_reads WHERE sym = ? ORDER BY ts DESC LIMIT ?`, sym, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.TapeReadRecord
	for rows.Next() {
		var rec core.TapeReadRecord
		var evidence, snapshot string
		if err := rows.Scan(&rec.Ts, &rec.Sym, &rec.Trigger, &rec.Headline, &rec.Control, &rec.Confidence,
			&rec.WhatsHappening, &evidence, &rec.WatchFor, &rec.Model, &rec.Err, &snapshot); err != nil {
			return nil, err
		}
		if evidence != "" {
			if err := json.Unmarshal([]byte(evidence), &rec.Evidence); err != nil {
				return nil, err
			}
		}
		if snapshot != "" {
			if err := json.Unmarshal([]byte(snapshot), &rec.Snapshot); err != nil {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
