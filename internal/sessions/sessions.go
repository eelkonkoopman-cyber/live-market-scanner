// Package sessions tracks the current US trading session (ET) and pushes
// transitions to the engine. It uses a simple weekday+clock rule; market
// holidays are a future refinement via the calendar table.
package sessions

import (
	"context"
	"sync/atomic"
	"time"

	_ "time/tzdata" // embed tz DB so America/New_York resolves on Windows

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type Manager struct {
	eng core.Engine
	loc *time.Location
	cur atomic.Int32
	now func() time.Time
}

func New(eng core.Engine) *Manager {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	m := &Manager{eng: eng, loc: loc, now: time.Now}
	m.cur.Store(int32(core.SessionClosed))
	return m
}

// Current returns the latest computed session.
func (m *Manager) Current() core.Session { return core.Session(m.cur.Load()) }

// Run updates the session immediately, then every 5s, until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	m.update(m.now())
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			m.update(m.now())
		}
	}
}

func (m *Manager) update(now time.Time) {
	s := SessionOf(now.In(m.loc))
	if core.Session(m.cur.Swap(int32(s))) != s {
		m.eng.SetSession(s)
	}
}

// SessionOf classifies an ET timestamp into a trading session.
func SessionOf(t time.Time) core.Session {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return core.SessionClosed
	}
	mins := t.Hour()*60 + t.Minute()
	switch {
	case mins >= 240 && mins < 570: // 04:00–09:30
		return core.SessionPre
	case mins >= 570 && mins < 960: // 09:30–16:00
		return core.SessionRegular
	case mins >= 960 && mins < 1200: // 16:00–20:00
		return core.SessionPost
	default:
		return core.SessionClosed
	}
}
