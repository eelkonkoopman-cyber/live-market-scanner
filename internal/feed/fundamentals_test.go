package feed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func TestFundamentalsMergesStatementsAndComputesBurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "income-statements"):
			_, _ = w.Write([]byte(`{"results":[
				{"period_end":"2026-03-31","filing_date":"2026-05-01","fiscal_year":2026,"fiscal_quarter":1,"revenue":1000000},
				{"period_end":"2025-12-31","filing_date":"2026-02-01","fiscal_year":2025,"fiscal_quarter":4,"revenue":800000}
			]}`))
		case strings.Contains(r.URL.Path, "balance-sheets"):
			_, _ = w.Write([]byte(`{"results":[
				{"period_end":"2026-03-31","filing_date":"2026-05-01","fiscal_year":2026,"fiscal_quarter":1,"cash_and_equivalents":6000000},
				{"period_end":"2025-12-31","filing_date":"2026-02-01","fiscal_year":2025,"fiscal_quarter":4,"cash_and_equivalents":8000000}
			]}`))
		case strings.Contains(r.URL.Path, "cash-flow-statements"):
			_, _ = w.Write([]byte(`{"results":[
				{"period_end":"2026-03-31","filing_date":"2026-05-01","fiscal_year":2026,"fiscal_quarter":1,"net_cash_from_operating_activities":-2000000},
				{"period_end":"2025-12-31","filing_date":"2026-02-01","fiscal_year":2025,"fiscal_quarter":4,"net_cash_from_operating_activities":-1500000}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	f, err := c.Fundamentals(context.Background(), "ABCD")
	if err != nil {
		t.Fatalf("fundamentals: %v", err)
	}
	if f.Sym != "ABCD" || len(f.Quarters) != 2 {
		t.Fatalf("shape: %+v", f)
	}
	// newest first
	q0 := f.Quarters[0]
	if q0.PeriodEnd != "2026-03-31" || q0.Revenue == nil || *q0.Revenue != 1000000 ||
		q0.Cash == nil || *q0.Cash != 6000000 || q0.OperatingCF == nil || *q0.OperatingCF != -2000000 {
		t.Fatalf("newest quarter merge wrong: %+v", q0)
	}
	q1 := f.Quarters[1]
	if q1.PeriodEnd != "2025-12-31" || q1.Revenue == nil || *q1.Revenue != 800000 {
		t.Fatalf("second quarter merge wrong: %+v", q1)
	}

	// Burn = -OperatingCF of the newest quarter; Runway = Cash / (Burn/3).
	if f.BurnQuarterly == nil || *f.BurnQuarterly != 2000000 {
		t.Fatalf("burn: %+v", f.BurnQuarterly)
	}
	wantRunway := (6000000.0 / 2000000.0) * 3 // 9 months
	if f.RunwayMonths == nil || *f.RunwayMonths != wantRunway {
		t.Fatalf("runway: got %v want %v", f.RunwayMonths, wantRunway)
	}
}

func TestFundamentalsCashFlowPositiveNoBurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "income-statements"):
			_, _ = w.Write([]byte(`{"results":[{"period_end":"2026-03-31","revenue":50000000}]}`))
		case strings.Contains(r.URL.Path, "balance-sheets"):
			_, _ = w.Write([]byte(`{"results":[{"period_end":"2026-03-31","cash_and_equivalents":100000000}]}`))
		case strings.Contains(r.URL.Path, "cash-flow-statements"):
			// profitable: operating cash flow positive -> no burn, no runway
			_, _ = w.Write([]byte(`{"results":[{"period_end":"2026-03-31","net_cash_from_operating_activities":3000000}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	f, err := c.Fundamentals(context.Background(), "PROF")
	if err != nil {
		t.Fatalf("fundamentals: %v", err)
	}
	if f.BurnQuarterly != nil {
		t.Fatalf("cash-flow-positive company should have nil burn, got %v", *f.BurnQuarterly)
	}
	if f.RunwayMonths != nil {
		t.Fatalf("no burn -> no runway, got %v", *f.RunwayMonths)
	}
}

func TestFundamentalsNotEntitled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status":"NOT_AUTHORIZED"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	_, err := c.Fundamentals(context.Background(), "ABCD")
	if err == nil {
		t.Fatal("expected error on HTTP 403")
	}
	if !errors.Is(err, core.ErrNotEntitled) {
		t.Fatalf("expected ErrNotEntitled, got: %v", err)
	}
}

func TestFundamentalsCapsAtFourQuarters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "income-statements") {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[
			{"period_end":"2026-03-31","revenue":1},
			{"period_end":"2025-12-31","revenue":2},
			{"period_end":"2025-09-30","revenue":3},
			{"period_end":"2025-06-30","revenue":4},
			{"period_end":"2025-03-31","revenue":5}
		]}`))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", RESTBase: srv.URL})
	f, err := c.Fundamentals(context.Background(), "ABCD")
	if err != nil {
		t.Fatalf("fundamentals: %v", err)
	}
	if len(f.Quarters) != 4 {
		t.Fatalf("want capped at 4 quarters, got %d", len(f.Quarters))
	}
	if f.Quarters[0].PeriodEnd != "2026-03-31" {
		t.Fatalf("newest should sort first, got %s", f.Quarters[0].PeriodEnd)
	}
}
