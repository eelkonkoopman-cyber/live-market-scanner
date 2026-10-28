package core

// ScanDef is a user-defined scan: a filter tree, a sort, a row cap, the columns
// to stream, and an alert config. Persisted in SQLite (scan_defs.def_json) and
// exchanged with the UI via the WS scan.upsert / scan.list messages. The scan
// package compiles Filter into an evaluable predicate (Match lives there, not
// here — core is data only).
type ScanDef struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Enabled bool        `json:"enabled"`
	Filter  Filter      `json:"filter"`
	Sort    []SortKey   `json:"sort"`
	Limit   int         `json:"limit"`
	Columns []string    `json:"columns"`
	Alert   AlertConfig `json:"alert"`
}

// Filter is a recursive boolean tree. A node is either a GROUP (Op set to "and"
// or "or", with Conds) or a LEAF (Field/Cmp/Value set). The UI may emit a flat
// top-level "and" group; nesting is supported for OR sub-groups.
type Filter struct {
	// Group node:
	Op    string   `json:"op,omitempty"`    // "and" | "or"
	Conds []Filter `json:"conds,omitempty"` // child predicates

	// Leaf node:
	Field string `json:"field,omitempty"` // one of the Field* constants
	Cmp   string `json:"cmp,omitempty"`   // see Cmp* constants
	Value any    `json:"value,omitempty"` // number, [lo,hi] for between, string, or list for "in"
}

// IsLeaf reports whether this node is a comparison rather than a group.
func (f Filter) IsLeaf() bool { return f.Op == "" && f.Field != "" }

// Comparison operators. The first group is valid in scan filters; CmpCrossAbove
// / CmpCrossBelow are stateful and valid ONLY inside AlertConfig.Trigger.
const (
	CmpGT      = ">"
	CmpGTE     = ">="
	CmpLT      = "<"
	CmpLTE     = "<="
	CmpBetween = "between"
	CmpEq      = "=="
	CmpIn      = "in"
	CmpIsTrue  = "isTrue"

	CmpCrossAbove = "crossAbove" // alert trigger only
	CmpCrossBelow = "crossBelow" // alert trigger only
)

// SortKey is one ordering term; multiple keys sort lexicographically.
type SortKey struct {
	Field string `json:"field"`
	Dir   string `json:"dir"` // "asc" | "desc"
}

// AlertConfig controls sound alerts for a scan.
type AlertConfig struct {
	Enabled        bool    `json:"enabled"`
	On             string  `json:"on"`                // "enter" | "cross"
	Trigger        *Filter `json:"trigger,omitempty"` // leaf predicate when On=="cross"
	Sound          string  `json:"sound"`             // sound id played client-side
	CooldownSec    int     `json:"cooldownSec"`
	RequireReentry bool    `json:"requireReentry"`
	// Speak asks the client to say the alerting symbol aloud (text-to-speech)
	// right after the alert's tone, so a trader looking at another monitor
	// hears WHICH name fired, not just that one did. Purely a client-side
	// presentation flag -- the scanner never reads it -- persisted with the scan
	// like Sound. omitempty keeps every existing scan's stored JSON unchanged
	// and a missing key decodes to false, so old scans stay silent.
	Speak bool `json:"speak,omitempty"`
	// SendToTWS is a SERVER-side action: when this scan alerts, the server moves
	// the linked TWS window to the alerting symbol through the display-group link
	// (internal/twslink Show -- it never places or touches an order). Guarded in
	// internal/server so it cannot yank TWS away from a symbol the trader just
	// picked, nor flip it faster than they can read: skipped for 20 s after the
	// user's own selection and for 5 s after the previous auto-send. Every
	// outcome (sent or skipped, with the reason) is broadcast as a tws.auto
	// frame. omitempty keeps every existing scan's stored JSON unchanged and a
	// missing key decodes to false, so old scans never touch TWS.
	SendToTWS bool `json:"sendToTws,omitempty"`
}

const (
	AlertOnEnter = "enter"
	AlertOnCross = "cross"
)
