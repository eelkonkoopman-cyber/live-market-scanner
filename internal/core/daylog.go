package core

import "context"

// Day Log (internal/daylog): every scan alert the scanner fires, per ET trading
// day, with what the market looked like at that moment and what happened after
// (the +5/+15/+30 minute outcome columns, filled in later from 1-minute bars),
// plus an end-of-day snapshot of the day's biggest gainers and losers. Kept
// forever; the web "Day Log" window reads it back with daylog.get.

// DayAlert is one fired scan alert. Optional numbers are pointers: nil = not
// known (no row, no bars yet), serialised as null.
type DayAlert struct {
	ID       int64  `json:"id"`
	Day      string `json:"day"` // ET trading date YYYY-MM-DD
	At       int64  `json:"at"`  // alert time, epoch ms
	Sym      string `json:"sym"`
	ScanID   string `json:"scanId"`
	ScanName string `json:"scanName"`
	Kind     string `json:"kind"` // enter | cross

	Price   *float64 `json:"price"`
	ChgDay  *float64 `json:"chgDay"` // % vs previous close
	Vol     *int64   `json:"vol"`    // session volume
	Rvol    *float64 `json:"rvol"`
	NewsHL  string   `json:"newsHeadline"`
	NewsAge *int64   `json:"newsAge"` // seconds between the headline and the alert
	Note    string   `json:"note"`    // early dildo, fresh ignition, signal-log note ...

	Blacklisted bool `json:"blacklisted"` // today's blacklist (internal/blacklist) had the name that day

	Out5    *float64 `json:"out5"`    // % price change at +5 min
	Out15   *float64 `json:"out15"`   // % at +15 min
	Out30   *float64 `json:"out30"`   // % at +30 min
	MaxUp   *float64 `json:"maxUp"`   // best % above the alert price inside 30 min (>= 0)
	MaxDown *float64 `json:"maxDown"` // worst % below the alert price inside 30 min (<= 0)
	Done    bool     `json:"done"`    // outcome pass finished for this alert
}

// DayOutcome is the later-filled part of a DayAlert.
type DayOutcome struct {
	Out5, Out15, Out30, MaxUp, MaxDown *float64
	Done                               bool
}

// DayMover is one line of the end-of-day top gainers / losers.
type DayMover struct {
	Day      string   `json:"day"`
	Side     string   `json:"side"` // gain | loss
	Rank     int      `json:"rank"` // 1 = biggest
	Sym      string   `json:"sym"`
	Price    *float64 `json:"price"`
	ChgDay   *float64 `json:"chgDay"`
	Vol      *int64   `json:"vol"`
	Headline string   `json:"headline"`
	TakenAt  int64    `json:"takenAt"` // epoch ms the snapshot was taken
}

// DayLogRepo persists the Day Log (internal/persist).
type DayLogRepo interface {
	InsertAlert(ctx context.Context, a DayAlert) (int64, error)
	Alerts(ctx context.Context, day string) ([]DayAlert, error)
	PendingAlerts(ctx context.Context, day string) ([]DayAlert, error)
	SetOutcome(ctx context.Context, id int64, o DayOutcome) error
	ReplaceMovers(ctx context.Context, day string, ms []DayMover) error
	Movers(ctx context.Context, day string) ([]DayMover, error)
	Days(ctx context.Context, limit int) ([]string, error)
}

// DayLogGetMsg is daylog.get: Date "" = today (ET). Snapshot true asks the
// server to take the gainers / losers snapshot now (today only) first.
type DayLogGetMsg struct {
	Date     string `json:"date"`
	Snapshot bool   `json:"snapshot,omitempty"`
}

// DayLogMsg is daylog.data: one day's alerts (oldest first) and movers, the
// days that have any (newest first, always including today) and the folder
// the per-day CSV files are written to.
type DayLogMsg struct {
	Date   string     `json:"date"`
	Dates  []string   `json:"dates"`
	Alerts []DayAlert `json:"alerts"`
	Movers []DayMover `json:"movers"`
	CSVDir string     `json:"csvDir"`
}
