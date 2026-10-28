package orderticket

// Short availability for the ticket's symbol, read from the scanner's
// existing IBKR short-stock store (internal/shortable: IBKR's public
// ftp2.interactivebrokers.com usa.txt, refreshed every 15 min). No fetch of
// its own. Shown in the SHORTABLE tile and on a short's confirm line (a
// warning only: IB decides whether the short goes through).

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

const shortStaleAfter = 30 * time.Minute

// ShortInfo is State.Short.
type ShortInfo struct {
	Known     bool    `json:"known"`     // the symbol is in IBKR's file
	Shortable bool    `json:"shortable"` // Avail > 0
	Avail     int64   `json:"avail"`
	Capped    bool    `json:"capped"` // file said ">10000000": Avail is a floor
	Fee       float64 `json:"fee"`    // annual borrow fee %
	AsOf      int64   `json:"asOf"`   // unix s of the file's own stamp (0 = never loaded)
	AgeMin    int64   `json:"ageMin"`
	Stale     bool    `json:"stale"` // never loaded or older than 30 min
	Source    string  `json:"source"`
	Note      string  `json:"note"`
	// TWS live (reqMktData generic tick 236): Live = a tick within 60 s
	Live          bool    `json:"live"`
	LiveShares    float64 `json:"liveShares"`
	LiveIndicator float64 `json:"liveIndicator"`
	LiveAt        int64   `json:"liveAt"` // unix ms of the last live tick
	LiveErr       string  `json:"liveErr"`
	FileAvail     int64   `json:"fileAvail"`
	FileKnown     bool    `json:"fileKnown"`
}

const (
	shortSource = "IBKR short-stock file (usa.txt, 15 min)"
	liveSource  = "TWS live"
)

// shortInfoLocked reads the store for sym (nil when the ticket has no store).
func (t *Ticket) shortInfoLocked(sym string, now time.Time) *ShortInfo {
	live := t.md.sym == sym && sym != "" && !t.md.liveAt.IsZero() && now.Sub(t.md.liveAt) <= liveWithin &&
		(t.md.haveShares || t.md.haveInd)
	if sym == "" || (t.cfg.Shortable == nil && !live && (t.md.sym != sym || t.md.err == "")) {
		return nil
	}
	var ref core.ShortRef
	var ok bool
	var asOf int64
	if t.cfg.Shortable != nil {
		ref, ok, asOf = t.cfg.Shortable(strings.ReplaceAll(sym, ".", " "))
		if !ok && strings.Contains(sym, ".") {
			ref, ok, asOf = t.cfg.Shortable(sym)
		}
	}
	si := t.fileShortInfo(ref, ok, asOf, now)
	if t.md.sym == sym {
		si.LiveErr = t.md.err
		si.LiveShares, si.LiveIndicator = t.md.liveShares, t.md.liveInd
		if !t.md.liveAt.IsZero() {
			si.LiveAt = t.md.liveAt.UnixMilli()
		}
	}
	if live { // TWS's live count wins; the fee stays from the file
		si.Live, si.Source, si.Note, si.Stale = true, liveSource, "", false
		si.Known, si.Capped = true, false
		if t.md.haveShares {
			si.Avail = int64(t.md.liveShares)
			si.Shortable = si.Avail > 0
		} else { // indicator only
			si.Avail = 0
			si.Shortable = t.md.liveInd > 2.5
		}
	} else if si.LiveErr != "" {
		si.Note = strings.TrimSpace(si.Note + " · live: " + si.LiveErr)
	}
	return si
}

// fileShortInfo: the IBKR-file part of ShortInfo.
func (t *Ticket) fileShortInfo(ref core.ShortRef, ok bool, asOf int64, now time.Time) *ShortInfo {
	si := &ShortInfo{Source: shortSource, AsOf: asOf}
	if asOf > 0 {
		si.AgeMin = int64(now.Sub(time.Unix(asOf, 0)) / time.Minute)
	}
	si.Stale = asOf == 0 || now.Sub(time.Unix(asOf, 0)) > shortStaleAfter
	switch {
	case asOf == 0:
		si.Note = "short list not loaded yet"
	case !ok:
		si.Note = "not in IBKR's short list"
	default:
		si.Known, si.FileKnown = true, true
		si.Avail, si.Capped, si.Fee = ref.Available, ref.Capped, ref.FeeRate
		si.FileAvail = ref.Available
		si.Shortable = ref.Available > 0
	}
	if si.Stale && asOf > 0 {
		si.Note = strings.TrimSpace(si.Note + fmt.Sprintf(" (list %dm old)", si.AgeMin))
	}
	return si
}

// fmtCompact: 950 -> "950", 150000 -> "150K", 1_250_000 -> "1.25M".
func fmtCompact(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", float64(n)/1e6), "0"), ".0") + "M"
	case n >= 10_000:
		return fmt.Sprintf("%dK", int64(math.Round(float64(n)/1000)))
	case n >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "K"
	}
	return fmt.Sprintf("%d", n)
}

// shortNote: the short confirm line's " · shortable 150K (fee 12.5%)" part and
// an amber warning when fewer shares are available than qty (never blocks).
func shortNote(si *ShortInfo, qty int64) (note, warn string) {
	if si == nil {
		return "", ""
	}
	if !si.Known {
		return " · shortable unknown", "short availability unknown: " + si.Note
	}
	cap := ""
	if si.Capped {
		cap = "+"
	}
	note = fmt.Sprintf(" · shortable %s%s (fee %s%%)", fmtCompact(si.Avail), cap, trimFloat(si.Fee))
	if si.Stale {
		note += fmt.Sprintf(" [%dm old]", si.AgeMin)
	}
	switch {
	case !si.Shortable:
		warn = "IBKR lists no shares to short — IB will likely reject"
	case !si.Capped && si.Avail < qty:
		warn = fmt.Sprintf("only %s shares available to short (%s wanted) — IB decides", fmtQty(si.Avail), fmtQty(qty))
	}
	return note, warn
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

var _ = core.ShortRef{}
