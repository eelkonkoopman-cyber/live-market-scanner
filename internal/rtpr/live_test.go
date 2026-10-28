package rtpr

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
)

// TestLiveRTPR runs the real poller against the real API for ~30s. Gated on
// an env var: it spends real request budget (shared 60/min with the user's
// running scanner instance), so it must never run as part of the normal suite.
//
//	SCANNER_LIVE_RTPR_TEST=1 go test ./internal/rtpr -run TestLiveRTPR -v
func TestLiveRTPR(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_RTPR_TEST") == "" {
		t.Skip("live test; set SCANNER_LIVE_RTPR_TEST=1")
	}
	token := config.RTPRToken()
	if token == "" {
		t.Skip("no RTPR token configured")
	}

	p := New(Config{Token: token, PollInterval: 3 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	deadline := time.After(34 * time.Second)
	got := 0
	for {
		select {
		case b := <-p.Out():
			for _, it := range b.Items {
				got++
				t.Logf("LIVE article: id=%s tickers=%v src=%q ts=%s headline=%.60q body=%dB",
					it.ID, it.Tickers, it.Source, time.UnixMilli(it.TS).Format("15:04:05"), it.Headline, len(b.Bodies[it.ID]))
			}
		case <-deadline:
			// Zero articles in 30s is legitimate on a quiet wire — the pass
			// criterion is "no errors and baseline seeded", not volume.
			if p.baseline {
				t.Fatal("baseline never seeded — the feed API is unreachable or the token is bad")
			}
			t.Logf("live run done: %d fresh article(s) in ~30s (0 is normal off-peak)", got)
			return
		}
	}
}
