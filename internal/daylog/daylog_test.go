package daylog

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/persist"
)

var et, _ = time.LoadLocation("America/New_York")

// 2026-10-15 is a Thursday.
func etTime(h, m int) time.Time { return time.Date(2026, 10, 15, h, m, 0, 0, et) }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newRec(t *testing.T, cl *clock, mod func(*Config)) (*Recorder, core.DayLogRepo, string) {
	t.Helper()
	ctx := context.Background()
	db, err := persist.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dir := filepath.Join(t.TempDir(), "daylog")
	cfg := Config{Repo: db.DayLogRepo(), Dir: dir, Now: cl.now}
	if mod != nil {
		mod(&cfg)
	}
	return New(cfg), cfg.Repo, dir
}

func f(v float64) *float64 { return &v }

func TestRecordsEveryAlertWithContext(t *testing.T) {
	ctx := context.Background()
	cl := &clock{etTime(10, 0)}
	at := cl.t.UnixMilli()
	r, repo, dir := newRec(t, cl, func(c *Config) {
		c.ScanNames = func() map[string]string { return map[string]string{"gap": "Gap Up"} }
		c.Row = func(sym string) (core.Row, bool) {
			return core.Row{Sym: sym, Last: 9, ChgDay: 12.5, Vol: 123456, Rvol: 3.2}, true
		}
		c.News = func(_ context.Context, sym string) ([]core.NewsItem, error) {
			return []core.NewsItem{
				{Headline: "old", TS: at - 3_600_000},
				{Headline: "Fresh contract win", TS: at - 90_000},
				{Headline: "future", TS: at + 60_000},
			}, nil
		}
		c.Note = func(sym string, atMs int64) string { return "dildo: +9% body" }
		c.Blacklisted = func(sym string) bool { return sym == "BAD" }
	})
	// The row sent with the alert wins over the live row, a missing key falls back to it.
	r.record(ctx, core.AlertMsg{ScanID: "gap", Sym: "ABC", Kind: "enter", At: at,
		Row: core.RowJSON{"last": 2.5, "chgDay": 40.0, "dildoEarly": 1.0, "ignFresh": 1.0}})
	// A blacklisted name and an unknown scan are recorded too, flagged.
	r.record(ctx, core.AlertMsg{ScanID: "mystery", Sym: "BAD", Kind: "cross", At: at + 1000, Row: core.RowJSON{"last": nil}})
	r.flushCSV(ctx)

	got, err := repo.Alerts(ctx, "2026-10-15")
	if err != nil || len(got) != 2 {
		t.Fatalf("alerts: %v %+v", err, got)
	}
	a := got[0]
	if a.ScanName != "Gap Up" || *a.Price != 2.5 || *a.ChgDay != 40 || *a.Vol != 123456 || *a.Rvol != 3.2 {
		t.Fatalf("fields: %+v", a)
	}
	if a.NewsHL != "Fresh contract win" || a.NewsAge == nil || *a.NewsAge != 90 {
		t.Fatalf("latest headline at or before the alert and its age: %q %v", a.NewsHL, a.NewsAge)
	}
	if a.Note != "early dildo; fresh ignition; dildo: +9% body" || a.Blacklisted {
		t.Fatalf("note/blacklist: %q %v", a.Note, a.Blacklisted)
	}
	b := got[1]
	if !b.Blacklisted || b.ScanName != "mystery" || *b.Price != 9 || b.Kind != "cross" {
		t.Fatalf("blacklisted alert is stored and flagged: %+v", b)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-15.csv"))
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	s := strings.TrimPrefix(string(raw), "\xef\xbb\xbf")
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "date,time_et,symbol,scan,") {
		t.Fatalf("csv shape: %q", s)
	}
	if !strings.HasPrefix(lines[1], "2026-10-15,10:00:00,ABC,Gap Up,enter,2.5000,40.00,123456,3.20,1,Fresh contract win,") ||
		!strings.Contains(lines[2], ",BAD,") || !strings.Contains(lines[2], ",yes,") {
		t.Fatalf("csv rows: %q", lines)
	}
}

func TestOnAlertNeverBlocksAndRunDrains(t *testing.T) {
	cl := &clock{etTime(10, 0)}
	r, repo, _ := newRec(t, cl, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	for i := 0; i < 5; i++ {
		r.OnAlert(core.AlertMsg{ScanID: "s", Sym: "A", Kind: "enter", At: cl.t.UnixMilli() + int64(i)})
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got, _ := repo.Alerts(context.Background(), "2026-10-15"); len(got) == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("alerts not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	// A full hand-off drops instead of blocking.
	full := New(Config{Repo: repo})
	for i := 0; i < inCap+10; i++ {
		full.OnAlert(core.AlertMsg{Sym: "A"})
	}
}

func bar(min int, o, h, l, c float64) core.Bar {
	return core.Bar{T: etTime(10, 0).UnixMilli() + int64(min)*minute, O: o, H: h, L: l, C: c, V: 1000}
}

func TestOutcomeFromBars(t *testing.T) {
	at := etTime(10, 0).UnixMilli() + 20_000 // 10:00:20
	a := core.DayAlert{At: at, Price: f(10)}
	var bars []core.Bar
	for m := 0; m <= 40; m++ { // close drifts 10.00 .. 10.40, highs 0.1 over, lows 0.1 under
		c := 10 + float64(m)*0.01
		bars = append(bars, bar(m, c, c+0.1, c-0.1, c))
	}
	// Right after the alert nothing is final.
	if o := Outcome(a, bars, at+2*minute); o.Out5 != nil || o.Done {
		t.Fatalf("too early: %+v", o)
	}
	// 6 minutes on: +5 min is known (the last bar ending by 10:05:20 is the 10:04 one), the rest is not.
	o := Outcome(a, bars, at+6*minute)
	if o.Out5 == nil || math.Abs(*o.Out5-0.4) > 1e-9 || o.Out15 != nil || o.Out30 != nil || o.Done {
		t.Fatalf("+5 only: %+v", o)
	}
	a.Out5 = o.Out5
	o = Outcome(a, bars, at+31*minute+1)
	if !o.Done || o.Out15 == nil || o.Out30 == nil || o.MaxUp == nil || o.MaxDown == nil {
		t.Fatalf("complete: %+v", o)
	}
	if math.Abs(*o.Out15-1.4) > 1e-9 || math.Abs(*o.Out30-2.9) > 1e-9 {
		t.Fatalf("+15/+30: %v %v", *o.Out15, *o.Out30)
	}
	if *o.MaxUp < 3.9 || *o.MaxUp > 4.1 || *o.MaxDown != -1 { // best high ~10.40 inside the window, worst low 9.90
		t.Fatalf("max up/down: %v %v", *o.MaxUp, *o.MaxDown)
	}
	// No price or no bars: nothing invented.
	if o := Outcome(core.DayAlert{At: at}, bars, at+40*minute); o.Out5 != nil || o.Done {
		t.Fatalf("no price: %+v", o)
	}
	if o := Outcome(a, nil, at+40*minute); o.Done || o.Out15 != nil {
		t.Fatalf("no bars: %+v", o)
	}
}

func TestFillOutcomesPass(t *testing.T) {
	ctx := context.Background()
	cl := &clock{etTime(10, 0)}
	calls := map[string]int{}
	var bars []core.Bar
	r, repo, _ := newRec(t, cl, func(c *Config) {
		c.Bars = func(_ context.Context, sym string) ([]core.Bar, error) {
			calls[sym]++
			if sym == "NOBARS" {
				return nil, nil
			}
			return bars, nil
		}
	})
	at := cl.t.UnixMilli()
	r.record(ctx, core.AlertMsg{ScanID: "s", Sym: "UP", At: at, Row: core.RowJSON{"last": 10.0}})
	r.record(ctx, core.AlertMsg{ScanID: "s", Sym: "UP", At: at + 60_000, Row: core.RowJSON{"last": 10.0}})
	r.record(ctx, core.AlertMsg{ScanID: "s", Sym: "NOBARS", At: at, Row: core.RowJSON{"last": 5.0}})
	for m := 0; m <= 40; m++ {
		c := 10 + float64(m)*0.05
		bars = append(bars, bar(m, c, c+0.05, c-0.05, c))
	}

	// 3 minutes in: nothing is due, nothing is fetched.
	cl.t = etTime(10, 3)
	r.fillOutcomes(ctx, "2026-10-15", cl.t.UnixMilli())
	if len(calls) != 0 {
		t.Fatalf("fetched too early: %v", calls)
	}
	// 7 minutes: the +5 column fills, one fetch per symbol however many alerts it has.
	cl.t = etTime(10, 7)
	r.fillOutcomes(ctx, "2026-10-15", cl.t.UnixMilli())
	if calls["UP"] != 1 {
		t.Fatalf("one fetch per symbol: %v", calls)
	}
	got, _ := repo.Alerts(ctx, "2026-10-15")
	var first core.DayAlert
	for _, a := range got {
		if a.Sym == "UP" && first.ID == 0 {
			first = a
		}
	}
	if first.Out5 == nil || *first.Out5 <= 0 || first.Out15 != nil || first.Done {
		t.Fatalf("+5 filled only: %+v", first)
	}
	// 35 minutes: everything is final (the per-symbol throttle is wall-clock: reset it).
	r.lastTry = map[string]time.Time{}
	cl.t = etTime(10, 35)
	r.fillOutcomes(ctx, "2026-10-15", cl.t.UnixMilli())
	got, _ = repo.Alerts(ctx, "2026-10-15")
	for _, a := range got {
		switch a.Sym {
		case "UP":
			if !a.Done || a.Out15 == nil || a.Out30 == nil || a.MaxUp == nil || *a.MaxUp <= 0 || *a.MaxDown > 0 {
				t.Fatalf("UP outcome: %+v", a)
			}
		case "NOBARS":
			if a.Out5 != nil || a.Done {
				t.Fatalf("no bars stays blank and pending: %+v", a)
			}
		}
	}
	// A symbol that never has bars is closed with blanks after maxAttempts passes.
	for i := 0; i < maxAttempts; i++ {
		r.lastTry = map[string]time.Time{}
		r.fillOutcomes(ctx, "2026-10-15", cl.t.UnixMilli())
	}
	got, _ = repo.Alerts(ctx, "2026-10-15")
	for _, a := range got {
		if a.Sym == "NOBARS" && (!a.Done || a.Out5 != nil) {
			t.Fatalf("closed with blanks: %+v", a)
		}
	}
}

func row(sym string, chg float64, vol int64) core.Row {
	return core.Row{Sym: sym, Last: 3, ChgDay: chg, Vol: vol}
}

func TestEndOfDaySnapshot(t *testing.T) {
	ctx := context.Background()
	cl := &clock{etTime(19, 59)}
	gain := []core.Row{row("G1", 80, 5e6), row("G2", 40, 2e6)}
	loss := []core.Row{row("L1", -30, 1e6)}
	var lastMin int64
	r, repo, _ := newRec(t, cl, func(c *Config) {
		c.TopMovers = func(n int, minVol int64, minPrice float64) ([]core.Row, []core.Row) {
			lastMin = minVol
			return gain, loss
		}
		c.News = func(_ context.Context, sym string) ([]core.NewsItem, error) {
			if sym == "G1" {
				return []core.NewsItem{{Headline: "G1 news", TS: cl.t.UnixMilli() - 1000}}, nil
			}
			return nil, nil
		}
	})
	r.lastFill = cl.t // keep the outcome / cache pass out of this test
	r.maintain(ctx)
	if ms, _ := repo.Movers(ctx, "2026-10-15"); len(ms) != 0 {
		t.Fatalf("nothing before 20:00: %+v", ms)
	}
	cl.t = etTime(20, 0)
	r.lastFill = cl.t
	r.maintain(ctx)
	ms, _ := repo.Movers(ctx, "2026-10-15")
	if len(ms) != 3 || ms[0].Sym != "G1" || ms[0].Side != "gain" || ms[0].Rank != 1 || ms[0].Headline != "G1 news" ||
		ms[2].Sym != "L1" || ms[2].Side != "loss" || *ms[2].ChgDay != -30 {
		t.Fatalf("snapshot: %+v", ms)
	}
	if lastMin <= 0 {
		t.Fatal("a volume floor is applied")
	}
	// A restart at 21:00 with a thinner market does not overwrite the 20:00 close ...
	gain, loss = []core.Row{row("X", 5, 1e6)}, nil
	cl.t = etTime(21, 0)
	r.eodDay = ""
	r.lastFill = cl.t
	r.maintain(ctx)
	if ms, _ = repo.Movers(ctx, "2026-10-15"); len(ms) != 3 || ms[0].Sym != "G1" {
		t.Fatalf("auto snapshot must not replace the EOD one: %+v", ms)
	}
	// ... but an on-demand snapshot does.
	msg, err := r.Get(ctx, "", true)
	if err != nil || len(msg.Movers) != 1 || msg.Movers[0].Sym != "X" || msg.Date != "2026-10-15" {
		t.Fatalf("on demand: %v %+v", err, msg)
	}
	// An empty market stores nothing (and does not wipe the day).
	gain, loss = nil, nil
	if _, err := r.Get(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	if ms, _ = repo.Movers(ctx, "2026-10-15"); len(ms) != 1 {
		t.Fatalf("empty snapshot keeps the old one: %+v", ms)
	}
}

func TestShutdownSnapshotsFromCacheAndWeekendIsSkipped(t *testing.T) {
	ctx := context.Background()
	cl := &clock{etTime(14, 0)}
	gain := []core.Row{row("G1", 50, 1e6)}
	r, repo, _ := newRec(t, cl, func(c *Config) {
		c.TopMovers = func(int, int64, float64) ([]core.Row, []core.Row) { return gain, nil }
	})
	r.refreshCache("2026-10-15", cl.t.UnixMilli())
	gain = nil // the market is gone by shutdown: only the cache has it
	r.shutdown()
	ms, _ := repo.Movers(ctx, "2026-10-15")
	if len(ms) != 1 || ms[0].Sym != "G1" {
		t.Fatalf("shutdown snapshot: %+v", ms)
	}

	// Saturday: no snapshots, however late.
	sat := time.Date(2026, 10, 17, 21, 0, 0, 0, et)
	cl2 := &clock{sat}
	gain = []core.Row{row("S", 9, 1e6)}
	r2, repo2, _ := newRec(t, cl2, func(c *Config) {
		c.TopMovers = func(int, int64, float64) ([]core.Row, []core.Row) { return gain, nil }
	})
	r2.lastFill = sat
	r2.maintain(ctx)
	r2.shutdown()
	if ms, _ := repo2.Movers(ctx, "2026-10-17"); len(ms) != 0 {
		t.Fatalf("weekend: %+v", ms)
	}
}

func TestGetListsTodayAndPastDays(t *testing.T) {
	ctx := context.Background()
	cl := &clock{etTime(10, 0)}
	r, repo, _ := newRec(t, cl, nil)
	_, _ = repo.InsertAlert(ctx, core.DayAlert{Day: "2026-10-13", At: 1, Sym: "A", ScanID: "s"})
	msg, err := r.Get(ctx, "", false)
	if err != nil || msg.Date != "2026-10-15" || len(msg.Alerts) != 0 || msg.Alerts == nil || msg.Movers == nil {
		t.Fatalf("today is empty but present: %v %+v", err, msg)
	}
	if len(msg.Dates) != 2 || msg.Dates[0] != "2026-10-15" || msg.Dates[1] != "2026-10-13" {
		t.Fatalf("dates: %v", msg.Dates)
	}
	if past, _ := r.Get(ctx, "2026-10-13", false); len(past.Alerts) != 1 {
		t.Fatalf("past day: %+v", past)
	}
}
