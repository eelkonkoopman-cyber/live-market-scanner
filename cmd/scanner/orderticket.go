package main

import (
	"os"
	"path/filepath"
	"sync"
)

// The Order Ticket (internal/orderticket): a small always-on-top pane
// (?pane=order) that follows the TWS ticker and sends a 9EMA limit entry with
// an attached take profit on the user's TWS account after a confirm click.
// It starts DISCONNECTED; only the pane's Connect button opens its socket (own
// client id, default 80, port default 7496). SCANNER_ORDERTICKET=0 builds none
// of it (the /api/order/ routes are then not mounted).

func orderTicketEnabled() bool { return os.Getenv("SCANNER_ORDERTICKET") != "0" }

// orderTicketDir holds the ticket's ownership audit: <data dir>/orderticket.
func orderTicketDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "orderticket")
}

// chartSymUnion keeps the ingest chart tap pointed at the windows' chart
// symbols PLUS the ticket's tracked symbols, so the ticket's 1m bars stay live
// whatever the windows show.
type chartSymUnion struct {
	mu     sync.Mutex
	set    func([]string)
	charts []string
	ticket []string
}

func (u *chartSymUnion) apply() {
	seen := map[string]bool{}
	var out []string
	for _, l := range [][]string{u.charts, u.ticket} {
		for _, s := range l {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	u.set(out)
}

func (u *chartSymUnion) SetCharts(s []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.charts = append([]string(nil), s...)
	u.apply()
}

func (u *chartSymUnion) SetTicket(s []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ticket = append([]string(nil), s...)
	u.apply()
}
