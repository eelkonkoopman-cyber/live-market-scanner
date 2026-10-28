package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/eelkonkoopman/marketscanner/internal/alpaca"
	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

// marketFeed is every feed method cmd/scanner wires. *feed.Client (Massive,
// the default) and *alpaca.Client both satisfy it; SCANNER_FEED picks one.
type marketFeed interface {
	core.FeedClient
	HasKey() bool
	SetKey(string)
	Intraday(ctx context.Context, sym string) ([]core.Bar, error)
	IntradayWithPrevDay(ctx context.Context, sym string) ([]core.Bar, error)
	IntradayOn(ctx context.Context, sym, date string) ([]core.Bar, error)
	IntradayBetween(ctx context.Context, sym, from, to string) ([]core.Bar, error)
	GroupedDaily(ctx context.Context, date string) (map[string]core.DailyBar, error)
	PrevClose(ctx context.Context, sym string) (float64, error)
	DailyRange(ctx context.Context, sym, fromDate, toDate string) ([]core.DailyBar, error)
	Floats(ctx context.Context, syms []string) ([]core.FloatRef, error)
	WatchTapeSet(syms []string)
}

var (
	_ marketFeed = (*feed.Client)(nil)
	_ marketFeed = (*alpaca.Client)(nil)
)

// feedKind reads SCANNER_FEED: "massive" (default) or "alpaca".
func feedKind() (string, error) {
	switch k := strings.ToLower(strings.TrimSpace(os.Getenv("SCANNER_FEED"))); k {
	case "", "massive":
		return "massive", nil
	case "alpaca":
		return "alpaca", nil
	default:
		return "", fmt.Errorf("SCANNER_FEED=%q: want massive or alpaca", k)
	}
}

// newAlpacaFeed builds the Alpaca client from ~/.marketscanner/alpaca.json
// (or ALPACA_KEY_ID / ALPACA_SECRET_KEY). SCANNER_ALPACA_FEED=iex selects the
// free IEX feed; SCANNER_ALPACA_SYMBOLS=A,B,C replaces the "*" wildcard (the
// IEX plan allows 30 symbols and refuses the wildcard).
func newAlpacaFeed(tp interface {
	In() chan<- core.TapeEvent
	SetError(string)
}) (*alpaca.Client, error) {
	keys, err := alpaca.LoadKeys(config.ConfigDir())
	if err != nil {
		return nil, err
	}
	var syms []string
	for _, s := range strings.Split(os.Getenv("SCANNER_ALPACA_SYMBOLS"), ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			syms = append(syms, s)
		}
	}
	return alpaca.New(alpaca.Config{
		Keys:       keys,
		Feed:       os.Getenv("SCANNER_ALPACA_FEED"),
		Symbols:    syms,
		TapeSink:   tp.In(),
		TapeStatus: tp.SetError,
	}), nil
}
