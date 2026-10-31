package orderticket

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// auditRec is one line of the append-only audit (<db dir>/orderticket/audit.jsonl).
// The set of ids in it is the ticket's ownership record: only these orders are
// ever adopted, modified or cancelled.
type auditRec struct {
	T  int64  `json:"t"`
	Ev string `json:"ev"` // place | perm | modify | cancel
	// Kind "vwapx" = entry + TP + real IB stop children (vwapx.go)
	Kind     string  `json:"kind,omitempty"`
	ID       int64   `json:"id"`
	ParentID int64   `json:"parentId,omitempty"`
	PermID   int64   `json:"permId,omitempty"`
	ClientID int     `json:"clientId,omitempty"`
	Sym      string  `json:"sym,omitempty"`
	Side     string  `json:"side,omitempty"`
	Qty      int64   `json:"qty,omitempty"`
	Limit    float64 `json:"limit,omitempty"`
	TP       float64 `json:"tp,omitempty"`
	TPPct    float64 `json:"tpPct,omitempty"` // take-profit percent (absent = legacy 10%)
	// soft stop ("stop" records; the latest one per order wins on replay)
	StopLevel  float64 `json:"stopLevel,omitempty"`
	StopPct    float64 `json:"stopPct,omitempty"`
	StopFollow bool    `json:"stopFollow,omitempty"`
	StopOff    bool    `json:"stopOff,omitempty"`
	StopState  string  `json:"stopState,omitempty"`
	// breakeven (breakeven.go)
	StopBE      bool    `json:"stopBe,omitempty"`
	BEOff       bool    `json:"beOff,omitempty"`
	BEPct       float64 `json:"bePct,omitempty"`
	BEOffsetPct float64 `json:"beOffsetPct,omitempty"`
	// preset (preset.go)
	Preset        string  `json:"preset,omitempty"`
	TPR           float64 `json:"tpR,omitempty"`
	Trail5m       bool    `json:"trail5m,omitempty"`
	DropOff       bool    `json:"dropOff,omitempty"`
	EntryDeadline int64   `json:"entryDeadline,omitempty"`
	FillAt        int64   `json:"fillAt,omitempty"`
	Note          string  `json:"note,omitempty"`
}

// Audit persists the records.
type Audit interface {
	Load() ([]auditRec, error)
	Append(auditRec) error
}

type fileAudit struct {
	dir string
	mu  sync.Mutex
}

func (f *fileAudit) path() string { return filepath.Join(f.dir, "audit.jsonl") }

func (f *fileAudit) Load() ([]auditRec, error) {
	if f.dir == "" {
		return nil, nil
	}
	fh, err := os.Open(f.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []auditRec
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r auditRec
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

func (f *fileAudit) Append(r auditRec) error {
	if f.dir == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(f.path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	_, err = fh.Write(append(b, '\n'))
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	return err
}

// MemAudit is an in-memory Audit (tests).
type MemAudit struct {
	mu   sync.Mutex
	Recs []auditRec
}

func (m *MemAudit) Load() ([]auditRec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]auditRec(nil), m.Recs...), nil
}

func (m *MemAudit) Append(r auditRec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Recs = append(m.Recs, r)
	return nil
}
