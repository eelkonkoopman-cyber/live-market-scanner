package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Guards on the "send to TWS when a scan alerts" action (AlertConfig.SendToTWS).
// The action only moves the linked TWS window through the display-group link
// (Config.AlertToTWS); it never places an order. These two guards keep it from
// being a nuisance on a live tape:
const (
	// A symbol the user picked themselves (tape.watch from any window) in the
	// last twsAutoUserQuietMs is left alone: an alert for a different symbol
	// does not yank TWS away from what they are looking at. An alert for the
	// SAME symbol is harmless and still goes.
	twsAutoUserQuietMs = 20_000
	// At most one auto-send per twsAutoMinGapMs, so a burst of alerts (a
	// minute boundary can fire several at once) does not flip TWS faster than
	// anyone can read it. The first alert of a burst wins.
	twsAutoMinGapMs = 5_000
)

// twsAutoState is the guards' memory, under Server.twsMu.
type twsAutoState struct {
	userSym string // the user's latest own (non-empty) tape.watch selection
	userAt  time.Time
	sentSym string // the last symbol auto-sent to TWS
	sentAt  time.Time
}

// noteUserSelection records a selection the user made themselves (a window's
// tape.watch with a symbol). Called from the websocket read loop.
func (s *Server) noteUserSelection(sym string) {
	s.twsMu.Lock()
	s.tws.userSym, s.tws.userAt = strings.ToUpper(strings.TrimSpace(sym)), s.now()
	s.twsMu.Unlock()
}

// runTwsAuto handles the alerts the broadcast loop queued, one at a time and in
// arrival order, so the first alert of a burst is the one that wins the gap.
func (s *Server) runTwsAuto(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-s.twsAlerts:
			s.sendAlertToTWS(a)
		}
	}
}

// sendAlertToTWS applies a flagged scan's alert to TWS and tells every window
// what happened. It runs on runTwsAuto, not the broadcast loop (the scan lookup
// goes through the scanner's control loop), and is a no-op when the feature is off, the scan is
// unknown, or it is not flagged.
func (s *Server) sendAlertToTWS(a core.AlertMsg) {
	if s.cfg.AlertToTWS == nil || !s.twsFollow() {
		return
	}
	var def *core.ScanDef
	for _, d := range s.cfg.Scanner.List() {
		if d.ID == a.ScanID {
			d := d
			def = &d
			break
		}
	}
	if def == nil || !def.Alert.SendToTWS {
		return
	}
	sym := strings.ToUpper(strings.TrimSpace(a.Sym))
	if sym == "" {
		return
	}

	// The check and the record are one critical section so two flagged alerts
	// racing here cannot both pass the min-gap guard.
	s.twsMu.Lock()
	now := s.now()
	sent, reason := true, ""
	if s.tws.userSym != "" && s.tws.userSym != sym && now.Sub(s.tws.userAt) < twsAutoUserQuietMs*time.Millisecond {
		sent, reason = false, fmt.Sprintf("you picked %s %ds ago", s.tws.userSym, secsAgo(now, s.tws.userAt))
	} else if s.tws.sentSym != "" && now.Sub(s.tws.sentAt) < twsAutoMinGapMs*time.Millisecond {
		sent, reason = false, fmt.Sprintf("%s was sent %ds ago", s.tws.sentSym, secsAgo(now, s.tws.sentAt))
	}
	if sent {
		s.tws.sentSym, s.tws.sentAt = sym, now
		s.cfg.AlertToTWS(sym) // non-blocking by contract (twslink Show only queues)
	}
	s.twsMu.Unlock()

	s.fanoutAll(mustFrame(core.TTwsAuto, core.TwsAutoMsg{
		Sym: sym, ScanID: a.ScanID, ScanName: def.Name, Sent: sent, Reason: reason, At: now.UnixMilli(),
	}))
}

// secsAgo is the whole seconds between two instants, rounded to nearest.
func secsAgo(now, then time.Time) int {
	return int(now.Sub(then).Seconds() + 0.5)
}
