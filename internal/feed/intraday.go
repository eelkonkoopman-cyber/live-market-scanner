package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type aggBar struct {
	T int64   `json:"t"` // bar start, epoch ms
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
	// VW is the bar's VWAP. No other declared tag case-folds to "vw".
	VW float64 `json:"vw"`
}

type aggResp struct {
	Results []aggBar `json:"results"`
}

// Intraday fetches today's 1-minute OHLC bars (extended hours included) for a
// symbol via the Massive/Polygon aggregates endpoint — used by the click-to-
// expand full chart.
//
// DO NOT widen this to a multi-day range: internal/prefill's windowStats
// filters bars by MINUTE-OF-DAY ONLY, with zero date filtering ("prefill date
// blindness" — see internal/prefill/prefill.go's package comment). A second
// calendar day of bars here would silently double-count into prefill's frozen
// pre-market SessionStats. Multi-day fetches belong on IntradayWithPrevDay
// below, which prefill does NOT call.
func (c *Client) Intraday(ctx context.Context, sym string) ([]core.Bar, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	date := time.Now().In(loc).Format("2006-01-02")
	return c.intradayRange(ctx, sym, date, date)
}

// IntradayWithPrevDay fetches the previous TRADING day's bars together with
// today's, in one request, for the monitor/watchlist charts (MonitorChart.tsx):
// morning continuity (gap vs yesterday's close) needs yesterday's bars drawn
// alongside today's, not just a prevClose line. The aggregates endpoint
// accepts independent from/to dates and returns results sort=asc across the
// whole range, so bars from both days arrive pre-sorted by T (absolute epoch
// ms) with no special-casing needed downstream.
//
// This is a NEW method, not a widening of Intraday: Intraday must stay
// today-only (see its doc comment) because internal/prefill depends on that
// contract by construction, not by convention. Only cmd/scanner's chart.get
// wiring calls this; internal/prefill continues to call Intraday, unchanged.
func (c *Client) IntradayWithPrevDay(ctx context.Context, sym string) ([]core.Bar, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc)
	prev := prevTradingDay(today)
	return c.intradayRange(ctx, sym, prev.Format("2006-01-02"), today.Format("2006-01-02"))
}

// IntradayOn fetches ONE explicit ET date's 1-minute bars (extended hours
// included) for a symbol. internal/prevah uses it for the previous day's
// after-hours high; the single date keeps it clear of prefill's "date
// blindness" (see Intraday), and prevah filters on the date again anyway.
func (c *Client) IntradayOn(ctx context.Context, sym, date string) ([]core.Bar, error) {
	return c.intradayRange(ctx, sym, date, date)
}

// IntradayBetween fetches a symbol's 1-minute bars (extended hours included)
// for the ET dates from..to inclusive, in ONE request. internal/rvolfill uses
// it to rebuild past volume curves; the caller must filter each bar on its ET
// date. One request holds at most 50,000 bars (~50 full extended sessions), so
// keep ranges well under that: there is no paging.
func (c *Client) IntradayBetween(ctx context.Context, sym, from, to string) ([]core.Bar, error) {
	return c.intradayRange(ctx, sym, from, to)
}

// prevTradingDay steps back one calendar day from t, skipping Saturday and
// Sunday. Weekends only — like internal/sessions.SessionOf and
// internal/schedule.ensureCalendar, this has no holiday calendar (a future
// refinement), so the day after a market holiday is reported as the previous
// trading day when it is actually the holiday's eve.
func prevTradingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday; wd = d.Weekday() {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// intradayRange fetches 1-minute OHLC bars (extended hours included) over
// [fromDate, toDate] (inclusive, "YYYY-MM-DD" ET dates) via the Massive/
// Polygon aggregates endpoint.
func (c *Client) intradayRange(ctx context.Context, sym, fromDate, toDate string) ([]core.Bar, error) {
	u := fmt.Sprintf("%s/v2/aggs/ticker/%s/range/1/minute/%s/%s?adjusted=true&sort=asc&limit=50000",
		c.restBase, sym, fromDate, toDate)
	var resp aggResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := make([]core.Bar, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, core.Bar{T: r.T, O: r.O, H: r.H, L: r.L, C: r.C, V: int64(r.V), VW: r.VW})
	}
	return out, nil
}

type groupedBar struct {
	Sym string  `json:"T"` // grouped results carry the ticker per bar
	O   float64 `json:"o"`
	H   float64 `json:"h"`
	L   float64 `json:"l"`
	C   float64 `json:"c"`
	// V is float64 for the same reason aggBar.V is: grouped volumes arrive
	// FRACTIONAL on the wire ("v":868021.675123). An int64 field here would
	// fail to decode the row — and, decoding being all-or-nothing per response,
	// would take the whole date's ~12k bars down with it.
	V float64 `json:"v"`
	// BarStart is unused but MUST be declared: every row also carries a
	// lowercase "t" (bar-start epoch ms). Go's encoding/json matches JSON keys
	// case-INSENSITIVELY when no exact-case field is declared for them — with
	// no field claiming "t", the decoder falls back to the closest match,
	// which is Sym's "T" tag, and fails trying to decode that number into a
	// string. Confirmed live: this broke 100% of GroupedDaily calls silently
	// (each failure just logged a Warn and moved to the next date), so
	// daily_close_history stayed empty however long the app had been running.
	//
	// Same rule governs the two keys still left undeclared, "vw" (VWAP) and
	// "n" (trade count): they are dropped safely only because NO declared tag
	// case-folds to either of them. Any field added here must be re-checked
	// against the full real key set — T, v, vw, o, c, h, l, t, n — for a
	// case-insensitive collision, exactly as TestGroupedDailyParsesRealShape
	// pins it.
	BarStart int64 `json:"t"`
}

type groupedResp struct {
	Results []groupedBar `json:"results"`
}

// GroupedDaily fetches EVERY US stock's daily OHLCV bar for one trading date in
// a single call (~12k tickers, ~1.3 MB) — the whole market's daily history
// costs ~25 calls at first boot and one per night after, instead of a call
// per symbol. Returns an empty map for holidays (the endpoint has no results).
// Each bar's Date is stamped with the requested date (the endpoint's per-row
// "t" is the bar-start epoch, not the ET trade date the history is keyed by).
func (c *Client) GroupedDaily(ctx context.Context, date string) (map[string]core.DailyBar, error) {
	u := fmt.Sprintf("%s/v2/aggs/grouped/locale/us/market/stocks/%s?adjusted=true", c.restBase, date)
	var resp groupedResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]core.DailyBar, len(resp.Results))
	for _, r := range resp.Results {
		if r.Sym != "" && r.C > 0 {
			out[r.Sym] = core.DailyBar{Date: date, O: r.O, H: r.H, L: r.L, C: r.C, V: int64(r.V)}
		}
	}
	return out, nil
}

// PrevClose fetches the prior session's official close for a symbol — the gap /
// breakeven reference drawn as a horizontal line on the chart. Returns 0 if the
// endpoint yields no result.
func (c *Client) PrevClose(ctx context.Context, sym string) (float64, error) {
	u := fmt.Sprintf("%s/v2/aggs/ticker/%s/prev?adjusted=true", c.restBase, sym)
	var resp aggResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].C, nil
}

// DailyRange fetches one symbol's adjusted DAILY bars over [fromDate, toDate]
// ("YYYY-MM-DD" ET dates, inclusive), ascending. The Episodic Pivot worker
// reads ~100 calendar days (the 63-session-ago close and ADR20) once per day
// for each >=10% gapper.
func (c *Client) DailyRange(ctx context.Context, sym, fromDate, toDate string) ([]core.DailyBar, error) {
	u := fmt.Sprintf("%s/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&limit=500",
		c.restBase, sym, fromDate, toDate)
	var resp aggResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := make([]core.DailyBar, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.C <= 0 {
			continue
		}
		d := time.UnixMilli(r.T).UTC().Format("2006-01-02") // daily bars are stamped 00:00 ET = 04:00/05:00Z
		out = append(out, core.DailyBar{Date: d, O: r.O, H: r.H, L: r.L, C: r.C, V: int64(r.V)})
	}
	return out, nil
}
