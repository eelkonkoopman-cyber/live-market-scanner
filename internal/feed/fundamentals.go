package feed

import (
	"context"
	"fmt"
	"sort"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// finBase is the shared base URL for the current (post-financials-vX-sunset)
// fundamentals API: three separate flat-schema endpoints, one per statement.
const finBase = "/stocks/financials/v1"

// finResult covers fields from ALL THREE statements in one struct — each
// endpoint's response only populates the fields it reports; json.Unmarshal
// silently leaves the rest nil, so one shared decode type is enough.
type finResult struct {
	PeriodEnd     string  `json:"period_end"`
	FilingDate    string  `json:"filing_date"`
	FiscalYear    float64 `json:"fiscal_year"`
	FiscalQuarter float64 `json:"fiscal_quarter"`

	// income-statements
	Revenue *float64 `json:"revenue"`

	// balance-sheets
	CashAndEquivalents *float64 `json:"cash_and_equivalents"`

	// cash-flow-statements
	NetCashFromOperatingActivities *float64 `json:"net_cash_from_operating_activities"`
}

type finResp struct {
	Results []finResult `json:"results"`
}

// Fundamentals fetches the last 4 quarters of revenue, cash on hand, and
// operating cash flow for sym, merges them by period, and derives cash burn +
// runway from the newest quarter. Requires the Financials & Ratios add-on;
// un-entitled keys fail with core.ErrNotEntitled.
func (c *Client) Fundamentals(ctx context.Context, sym string) (core.Fundamentals, error) {
	byPeriod := make(map[string]*core.FinQuarter)

	// fetch calls one statement endpoint and merges its rows into byPeriod via
	// fill, which copies just that statement's field(s) onto the shared quarter.
	fetch := func(endpoint string, fill func(*core.FinQuarter, finResult)) error {
		u := fmt.Sprintf("%s%s/%s?tickers=%s&timeframe=quarterly&limit=4&sort=period_end.desc",
			c.restBase, finBase, endpoint, sym)
		var resp finResp
		if err := c.getJSON(ctx, u, &resp); err != nil {
			if IsEntitlementErr(err) {
				return fmt.Errorf("%w: %s", core.ErrNotEntitled, err)
			}
			return err
		}
		for _, r := range resp.Results {
			q, ok := byPeriod[r.PeriodEnd]
			if !ok {
				q = &core.FinQuarter{
					FiscalYear: int(r.FiscalYear), FiscalQuarter: int(r.FiscalQuarter),
					PeriodEnd: r.PeriodEnd, FilingDate: r.FilingDate,
				}
				byPeriod[r.PeriodEnd] = q
			}
			fill(q, r)
		}
		return nil
	}

	if err := fetch("income-statements", func(q *core.FinQuarter, r finResult) { q.Revenue = r.Revenue }); err != nil {
		return core.Fundamentals{}, err
	}
	if err := fetch("balance-sheets", func(q *core.FinQuarter, r finResult) { q.Cash = r.CashAndEquivalents }); err != nil {
		return core.Fundamentals{}, err
	}
	if err := fetch("cash-flow-statements", func(q *core.FinQuarter, r finResult) { q.OperatingCF = r.NetCashFromOperatingActivities }); err != nil {
		return core.Fundamentals{}, err
	}

	quarters := make([]core.FinQuarter, 0, len(byPeriod))
	for _, q := range byPeriod {
		quarters = append(quarters, *q)
	}
	sort.Slice(quarters, func(i, j int) bool { return quarters[i].PeriodEnd > quarters[j].PeriodEnd })
	if len(quarters) > 4 {
		quarters = quarters[:4]
	}

	out := core.Fundamentals{Sym: sym, Quarters: quarters, Source: "massive_financials_v1"}
	// Burn = cash USED in operating activities last quarter, deliberately
	// excluding financing activity (an offering shouldn't shrink apparent
	// burn) — the standard basis biotech/small-cap issuers themselves use for
	// non-GAAP "cash burn" disclosures. Runway assumes the same quarterly rate.
	if len(quarters) > 0 {
		q := quarters[0]
		if q.OperatingCF != nil && *q.OperatingCF < 0 {
			burn := -*q.OperatingCF
			out.BurnQuarterly = &burn
			if q.Cash != nil && burn > 0 {
				months := (*q.Cash / burn) * 3
				out.RunwayMonths = &months
			}
		}
	}
	return out, nil
}
