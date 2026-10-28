// Package daylog is the Day Log: a record, per ET trading day, of EVERY scan
// alert the scanner fires (also the ones the user muted in the web or on names
// on today's blacklist (internal/blacklist) -- those are stored and flagged, never
// dropped), with what the market looked like at that moment (price, % on the
// day, volume, RVOL, the latest headline and its age, a note such as "early
// dildo" or "fresh ignition") and what happened afterwards: the price change
// 5 / 15 / 30 minutes later and the best / worst move inside those 30 minutes,
// filled in by a background pass from 1-minute bars. At 20:00 ET (or at
// shutdown, or on demand) it also snapshots the day's top gainers and losers.
//
// Everything is stored forever in the day_alerts / day_movers tables and also
// written as a human-readable <dir>/YYYY-MM-DD.csv per day.
//
// OnAlert is called from the server's broadcast loop and never blocks: it
// hands the alert to a buffered channel that Run drains. All slow work
// (row / news lookups, SQLite, bar fetches, CSV files) happens on Run's
// goroutine. The 1-minute bars come from the Config.Bars seam, which main
// wires to the very REST call the chart window and the flag scan already
// make; the pass asks for a symbol at most once a minute and only while one of
// its alerts is waiting for an outcome.
package daylog

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "time/tzdata" // America/New_York on a Windows box with no OS zone files (repo convention)

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	inCap       = 2048             // alert hand-off buffer
	maintEvery  = 15 * time.Second // CSV flush / schedule check
	fillEvery   = 60 * time.Second // outcome pass and movers cache refresh
	fillBudget  = 45 * time.Second // longest one outcome pass
	fillSyms    = 25               // symbols fetched per pass
	maxAttempts = 8                // bar-less passes before an alert is closed with blanks
	lookup      = 3 * time.Second  // one row / news lookup
	minute      = int64(60_000)    // ms
	moversN     = 20               // gainers and losers kept
	moversMinV  = int64(50_000)    // a mover needs this much session volume
	moversMinPx = 0.20             // and at least this price
	eodMinute   = 20 * 60          // 20:00 ET, minutes after midnight
	openMinute  = 4 * 60           // session open, 04:00 ET
	dayLayout   = "2006-01-02"
	listDays    = 400
	dayLogSep   = "; "
	finalBudget = 8 * time.Second
)

// Config wires the recorder. Only Repo is required; every seam left nil is
// skipped (no live row, no news, no bars, no movers ...).
type Config struct {
	Repo core.DayLogRepo
	Dir  string // CSV folder ("" = no files)
	Now  func() time.Time

	Row         func(sym string) (core.Row, bool)                                   // live merged metrics of a symbol
	ScanNames   func() map[string]string                                            // scan id -> display name
	News        func(ctx context.Context, sym string) ([]core.NewsItem, error)      // archived headlines of a symbol
	Note        func(sym string, atMs int64) string                                 // extra note (signal log) for an alert
	Blacklisted func(sym string) bool                                               // on today's blacklist (internal/blacklist)
	Bars        func(ctx context.Context, sym string) ([]core.Bar, error)           // 1-minute bars, oldest first
	TopMovers   func(n int, minVol int64, minPrice float64) (gain, loss []core.Row) // the day's biggest movers
}

// Recorder is the Day Log. Create with New.
type Recorder struct {
	cfg Config
	loc *time.Location
	in  chan core.AlertMsg

	drops int64 // alerts the full hand-off could not take
	mu    sync.Mutex
	// guarded by mu
	names    map[string]string
	namesAt  time.Time
	cacheDay string
	cacheG   []core.Row
	cacheL   []core.Row

	// worker goroutine only
	dirty    map[string]bool
	attempts map[int64]int
	lastTry  map[string]time.Time
	eodDay   string
	lastFill time.Time
}

// New builds a recorder.
func New(cfg Config) *Recorder {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return &Recorder{
		cfg: cfg, loc: loc, in: make(chan core.AlertMsg, inCap),
		dirty: map[string]bool{}, attempts: map[int64]int{}, lastTry: map[string]time.Time{},
	}
}

// CSVDir is the folder the per-day CSV files go to ("" = none).
func (r *Recorder) CSVDir() string { return r.cfg.Dir }

// OnAlert hands one fired scan alert to the recorder. It never blocks.
func (r *Recorder) OnAlert(a core.AlertMsg) {
	select {
	case r.in <- a:
	default:
		r.mu.Lock()
		r.drops++
		n := r.drops
		r.mu.Unlock()
		logx.L().Warn("daylog: alert hand-off full, alert not recorded", "sym", a.Sym, "scan", a.ScanID, "dropped", n)
	}
}

func (r *Recorder) etDay(ms int64) string { return time.UnixMilli(ms).In(r.loc).Format(dayLayout) }

// etMinute is the minutes after ET midnight of ms.
func (r *Recorder) etMinute(ms int64) int {
	t := time.UnixMilli(ms).In(r.loc)
	return t.Hour()*60 + t.Minute()
}

func (r *Recorder) weekend(ms int64) bool {
	d := time.UnixMilli(ms).In(r.loc).Weekday()
	return d == time.Saturday || d == time.Sunday
}

// Run records alerts and runs the background passes until ctx ends. It returns
// nil on cancel after a last flush and the shutdown movers snapshot.
func (r *Recorder) Run(ctx context.Context) error {
	if r.cfg.Repo == nil {
		return nil
	}
	tk := time.NewTicker(maintEvery)
	defer tk.Stop()
	// Today's file may be missing or stale after a restart: write it once.
	r.dirty[r.etDay(r.cfg.Now().UnixMilli())] = true
	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			return nil
		case a := <-r.in:
			r.record(ctx, a)
		case <-tk.C:
			r.maintain(ctx)
		}
	}
}

func (r *Recorder) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), finalBudget)
	defer cancel()
	for {
		select {
		case a := <-r.in:
			r.recordWith(ctx, a, false)
			continue
		default:
		}
		break
	}
	now := r.cfg.Now().UnixMilli()
	day := r.etDay(now)
	if !r.weekend(now) && r.etMinute(now) >= openMinute {
		r.mu.Lock()
		g, l, ok := r.cacheG, r.cacheL, r.cacheDay == day
		r.mu.Unlock()
		if ok {
			if err := r.saveMovers(ctx, day, g, l, true); err != nil {
				logx.L().Warn("daylog: shutdown movers snapshot failed", "err", err)
			}
		}
	}
	r.flushCSV(ctx)
}

// maintain is the periodic work: schedule checks, the outcome pass, CSV files.
func (r *Recorder) maintain(ctx context.Context) {
	now := r.cfg.Now()
	ms := now.UnixMilli()
	day := r.etDay(ms)
	if now.Sub(r.lastFill) >= fillEvery {
		r.lastFill = now
		r.fillOutcomes(ctx, day, ms)
		r.refreshCache(day, ms)
	}
	if !r.weekend(ms) && r.etMinute(ms) >= eodMinute && r.eodDay != day {
		r.eodDay = day
		if err := r.snapshotLive(ctx, day, true); err != nil {
			logx.L().Warn("daylog: end-of-day snapshot failed", "err", err)
		}
	}
	r.flushCSV(ctx)
}

// ---- recording --------------------------------------------------------

func (r *Recorder) record(ctx context.Context, a core.AlertMsg) { r.recordWith(ctx, a, true) }

func (r *Recorder) recordWith(ctx context.Context, a core.AlertMsg, lookups bool) {
	da := r.build(ctx, a, lookups)
	if _, err := r.cfg.Repo.InsertAlert(ctx, da); err != nil {
		logx.L().Warn("daylog: storing an alert failed", "sym", a.Sym, "scan", a.ScanID, "err", err)
		return
	}
	r.dirty[da.Day] = true
}

func (r *Recorder) scanName(id string) string {
	r.mu.Lock()
	n := r.names[id]
	stale := time.Since(r.namesAt) > 30*time.Second
	r.mu.Unlock()
	if (n == "" || stale) && r.cfg.ScanNames != nil {
		if m, ok := within(lookup, r.cfg.ScanNames); ok && m != nil {
			r.mu.Lock()
			r.names, r.namesAt = m, time.Now()
			n = m[id]
			r.mu.Unlock()
		}
	}
	if n == "" {
		return id
	}
	return n
}

// jnum reads a number from a row snapshot sent with the alert (nil = absent / null / NaN).
func jnum(m core.RowJSON, key string) *float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	default:
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func fnum(f float64) *float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func (r *Recorder) build(ctx context.Context, a core.AlertMsg, lookups bool) core.DayAlert {
	at := a.At
	if at <= 0 {
		at = r.cfg.Now().UnixMilli()
	}
	d := core.DayAlert{
		Day: r.etDay(at), At: at, Sym: a.Sym, ScanID: a.ScanID, ScanName: r.scanName(a.ScanID), Kind: a.Kind,
	}
	d.Price, d.ChgDay, d.Rvol = jnum(a.Row, core.FieldLast), jnum(a.Row, core.FieldChgDay), jnum(a.Row, core.FieldRvol)
	if v := jnum(a.Row, core.FieldVol); v != nil {
		n := int64(*v)
		d.Vol = &n
	}
	if lookups && r.cfg.Row != nil && (d.Price == nil || d.ChgDay == nil || d.Vol == nil || d.Rvol == nil) {
		type rr struct {
			row core.Row
			ok  bool
		}
		if got, ok := within(lookup, func() rr { row, ok := r.cfg.Row(a.Sym); return rr{row, ok} }); ok && got.ok {
			if d.Price == nil {
				d.Price = fnum(got.row.Last)
			}
			if d.ChgDay == nil {
				d.ChgDay = fnum(got.row.ChgDay)
			}
			if d.Rvol == nil {
				d.Rvol = fnum(got.row.Rvol)
			}
			if d.Vol == nil && got.row.Vol > 0 {
				v := got.row.Vol
				d.Vol = &v
			}
		}
	}
	if lookups && r.cfg.News != nil {
		if hl, age, ok := r.latestNews(ctx, a.Sym, at); ok {
			d.NewsHL, d.NewsAge = hl, &age
		}
	}
	var notes []string
	if v := jnum(a.Row, core.FieldDildoEarly); v != nil && *v == 1 {
		notes = append(notes, "early dildo")
	}
	if v := jnum(a.Row, core.FieldIgnFresh); v != nil && *v == 1 {
		notes = append(notes, "fresh ignition")
	}
	if v := jnum(a.Row, core.FieldIgnPass); v != nil && *v == 0 {
		notes = append(notes, "ignition not passed")
	}
	if r.cfg.Note != nil {
		if n := strings.TrimSpace(r.cfg.Note(a.Sym, at)); n != "" {
			notes = append(notes, n)
		}
	}
	d.Note = strings.Join(notes, dayLogSep)
	if r.cfg.Blacklisted != nil {
		d.Blacklisted, _ = within(lookup, func() bool { return r.cfg.Blacklisted(a.Sym) })
	}
	return d
}

// latestNews is the newest archived headline of sym published at or before atMs.
func (r *Recorder) latestNews(ctx context.Context, sym string, atMs int64) (string, int64, bool) {
	c, cancel := context.WithTimeout(ctx, lookup)
	defer cancel()
	items, err := r.cfg.News(c, sym)
	if err != nil {
		return "", 0, false
	}
	var best *core.NewsItem
	for i := range items {
		if items[i].TS <= atMs && items[i].Headline != "" && (best == nil || items[i].TS > best.TS) {
			best = &items[i]
		}
	}
	if best == nil {
		return "", 0, false
	}
	return best.Headline, (atMs - best.TS) / 1000, true
}

// within runs fn and gives up waiting after d (the goroutine is left to finish
// on its own): a scanner that is shutting down must never hang the recorder.
func within[T any](d time.Duration, fn func() T) (T, bool) {
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	select {
	case v := <-ch:
		return v, true
	case <-time.After(d):
		var z T
		return z, false
	}
}

// ---- outcomes ---------------------------------------------------------

func round2(v float64) *float64 {
	x := math.Round(v*100) / 100
	return &x
}

// Outcome computes what happened after alert a from 1-minute bars (oldest
// first) as of nowMs. A horizon is filled once its minute is over (the bar that
// holds it has closed): the price then is the close of the last bar ending at
// or before the horizon, so a name that stopped printing carries its last
// price. Max up / max down (best / worst % against the alert price over the 30
// minutes, never on the wrong side of zero) and Done come with the 30 minute
// mark. No price or no bars gives the zero outcome.
func Outcome(a core.DayAlert, bars []core.Bar, nowMs int64) core.DayOutcome {
	o := core.DayOutcome{Out5: a.Out5, Out15: a.Out15, Out30: a.Out30, MaxUp: a.MaxUp, MaxDown: a.MaxDown, Done: a.Done}
	if a.Price == nil || *a.Price <= 0 || len(bars) == 0 {
		return o
	}
	base := *a.Price
	at := func(mins int64) *float64 {
		target := a.At + mins*minute
		if nowMs < target+minute {
			return nil
		}
		var c float64
		found := false
		for _, b := range bars {
			if b.T+minute > target {
				break
			}
			c, found = b.C, true
		}
		if !found || c <= 0 {
			return nil
		}
		return round2((c/base - 1) * 100)
	}
	if o.Out5 == nil {
		o.Out5 = at(5)
	}
	if o.Out15 == nil {
		o.Out15 = at(15)
	}
	if o.Out30 == nil {
		o.Out30 = at(30)
	}
	if !o.Done && nowMs >= a.At+30*minute+minute {
		hi, lo := base, base
		for _, b := range bars {
			if b.T+minute <= a.At || b.T >= a.At+30*minute {
				continue
			}
			if b.H > hi {
				hi = b.H
			}
			if b.L > 0 && b.L < lo {
				lo = b.L
			}
		}
		o.MaxUp, o.MaxDown = round2((hi/base-1)*100), round2((lo/base-1)*100)
		o.Done = true
	}
	return o
}

func needsFill(a core.DayAlert, nowMs int64) bool {
	age := nowMs - a.At
	return (age >= 6*minute && a.Out5 == nil) || (age >= 16*minute && a.Out15 == nil) || (age >= 31*minute && !a.Done)
}

func (r *Recorder) fillOutcomes(ctx context.Context, day string, nowMs int64) {
	if r.cfg.Bars == nil {
		return
	}
	pend, err := r.cfg.Repo.PendingAlerts(ctx, day)
	if err != nil {
		logx.L().Warn("daylog: loading pending alerts failed", "err", err)
		return
	}
	bySym := map[string][]core.DayAlert{}
	var order []string
	for _, a := range pend {
		if !needsFill(a, nowMs) {
			continue
		}
		if _, ok := bySym[a.Sym]; !ok {
			order = append(order, a.Sym)
		}
		bySym[a.Sym] = append(bySym[a.Sym], a)
	}
	pc, cancel := context.WithTimeout(ctx, fillBudget)
	defer cancel()
	fetched := 0
	for _, sym := range order {
		if fetched >= fillSyms || pc.Err() != nil {
			break
		}
		if t, ok := r.lastTry[sym]; ok && time.Since(t) < fillEvery-5*time.Second {
			continue
		}
		r.lastTry[sym] = time.Now()
		fetched++
		bars, err := r.cfg.Bars(pc, sym)
		for _, a := range bySym[sym] {
			if err != nil || len(bars) == 0 {
				if r.attempts[a.ID]++; r.attempts[a.ID] >= maxAttempts && nowMs-a.At > 31*minute {
					_ = r.cfg.Repo.SetOutcome(ctx, a.ID, core.DayOutcome{Done: true}) // no bars: closed with blanks
					r.dirty[day] = true
				}
				continue
			}
			o := Outcome(a, bars, nowMs)
			if o.Done == a.Done && o.Out5 == a.Out5 && o.Out15 == a.Out15 && o.Out30 == a.Out30 {
				continue
			}
			if err := r.cfg.Repo.SetOutcome(ctx, a.ID, o); err != nil {
				logx.L().Warn("daylog: storing an outcome failed", "sym", a.Sym, "err", err)
				continue
			}
			r.dirty[day] = true
		}
	}
}

// ---- gainers / losers -------------------------------------------------

func (r *Recorder) refreshCache(day string, ms int64) {
	if r.cfg.TopMovers == nil || r.weekend(ms) {
		return
	}
	if m := r.etMinute(ms); m < openMinute || m >= eodMinute+5 {
		return
	}
	type gl struct{ g, l []core.Row }
	got, ok := within(5*time.Second, func() gl {
		g, l := r.cfg.TopMovers(moversN, moversMinV, moversMinPx)
		return gl{g, l}
	})
	if !ok || (len(got.g) == 0 && len(got.l) == 0) {
		return
	}
	r.mu.Lock()
	r.cacheDay, r.cacheG, r.cacheL = day, got.g, got.l
	r.mu.Unlock()
}

func (r *Recorder) snapshotLive(ctx context.Context, day string, auto bool) error {
	if r.cfg.TopMovers == nil {
		return nil
	}
	type gl struct{ g, l []core.Row }
	got, ok := within(5*time.Second, func() gl {
		g, l := r.cfg.TopMovers(moversN, moversMinV, moversMinPx)
		return gl{g, l}
	})
	if !ok {
		r.mu.Lock()
		got.g, got.l = r.cacheG, r.cacheL
		if r.cacheDay != day {
			got.g, got.l = nil, nil
		}
		r.mu.Unlock()
	}
	return r.saveMovers(ctx, day, got.g, got.l, auto)
}

// saveMovers stores the snapshot. An automatic snapshot never replaces one taken
// at or after 20:00 ET (a restart at 21:00 must not overwrite the real close),
// and nothing is stored when there is nothing to store.
func (r *Recorder) saveMovers(ctx context.Context, day string, gain, loss []core.Row, auto bool) error {
	if len(gain) == 0 && len(loss) == 0 {
		return nil
	}
	if auto {
		if old, err := r.cfg.Repo.Movers(ctx, day); err == nil && len(old) > 0 && r.etMinute(old[0].TakenAt) >= eodMinute {
			return nil
		}
	}
	now := r.cfg.Now().UnixMilli()
	var out []core.DayMover
	add := func(side string, rows []core.Row) {
		for i, row := range rows {
			m := core.DayMover{Day: day, Side: side, Rank: i + 1, Sym: row.Sym, Price: fnum(row.Last), ChgDay: fnum(row.ChgDay), TakenAt: now}
			if row.Vol > 0 {
				v := row.Vol
				m.Vol = &v
			}
			if r.cfg.News != nil {
				if hl, _, ok := r.latestNews(ctx, row.Sym, now); ok {
					m.Headline = hl
				}
			}
			out = append(out, m)
		}
	}
	add("gain", gain)
	add("loss", loss)
	return r.cfg.Repo.ReplaceMovers(ctx, day, out)
}

// ---- serving ----------------------------------------------------------

// Get serves one day (date "" = today). With snapshot true (today only) the
// gainers / losers snapshot is taken first, from the live rows.
func (r *Recorder) Get(ctx context.Context, date string, snapshot bool) (core.DayLogMsg, error) {
	today := r.etDay(r.cfg.Now().UnixMilli())
	if date == "" {
		date = today
	}
	if snapshot && date == today {
		if err := r.snapshotLive(ctx, date, false); err != nil {
			return core.DayLogMsg{}, err
		}
	}
	alerts, err := r.cfg.Repo.Alerts(ctx, date)
	if err != nil {
		return core.DayLogMsg{}, err
	}
	movers, err := r.cfg.Repo.Movers(ctx, date)
	if err != nil {
		return core.DayLogMsg{}, err
	}
	days, err := r.cfg.Repo.Days(ctx, listDays)
	if err != nil {
		return core.DayLogMsg{}, err
	}
	days = append(days, today)
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	uniq := make([]string, 0, len(days))
	for _, d := range days {
		if n := len(uniq); n > 0 && uniq[n-1] == d {
			continue
		}
		uniq = append(uniq, d)
	}
	if alerts == nil {
		alerts = []core.DayAlert{}
	}
	if movers == nil {
		movers = []core.DayMover{}
	}
	return core.DayLogMsg{Date: date, Dates: uniq, Alerts: alerts, Movers: movers, CSVDir: r.cfg.Dir}, nil
}

// ---- CSV --------------------------------------------------------------

// CSVHeader is the header row of the per-day files.
var CSVHeader = []string{"date", "time_et", "symbol", "scan", "kind", "price", "chg_day_pct", "volume", "rvol",
	"news_age_min", "news_headline", "note", "blacklisted", "chg_5m_pct", "chg_15m_pct", "chg_30m_pct", "max_up_30m_pct", "max_down_30m_pct"}

func optF(p *float64, dec int) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', dec, 64)
}

// CSVRows renders alerts as CSV records (without the header).
func CSVRows(loc *time.Location, alerts []core.DayAlert) [][]string {
	out := make([][]string, 0, len(alerts))
	for _, a := range alerts {
		vol, age := "", ""
		if a.Vol != nil {
			vol = strconv.FormatInt(*a.Vol, 10)
		}
		if a.NewsAge != nil {
			age = strconv.FormatInt(*a.NewsAge/60, 10)
		}
		bl := ""
		if a.Blacklisted {
			bl = "yes"
		}
		out = append(out, []string{
			a.Day, time.UnixMilli(a.At).In(loc).Format("15:04:05"), a.Sym, a.ScanName, a.Kind,
			optF(a.Price, 4), optF(a.ChgDay, 2), vol, optF(a.Rvol, 2), age, a.NewsHL, a.Note, bl,
			optF(a.Out5, 2), optF(a.Out15, 2), optF(a.Out30, 2), optF(a.MaxUp, 2), optF(a.MaxDown, 2),
		})
	}
	return out
}

func (r *Recorder) flushCSV(ctx context.Context) {
	if r.cfg.Dir == "" {
		r.dirty = map[string]bool{}
		return
	}
	for day := range r.dirty {
		if err := r.writeCSV(ctx, day); err != nil {
			logx.L().Warn("daylog: writing the day file failed (will retry)", "day", day, "err", err)
			continue
		}
		delete(r.dirty, day)
	}
}

func (r *Recorder) writeCSV(ctx context.Context, day string) error {
	alerts, err := r.cfg.Repo.Alerts(ctx, day)
	if err != nil {
		return err
	}
	if len(alerts) == 0 && !r.csvExists(day) {
		return nil // nothing to say yet: no empty file
	}
	if err := os.MkdirAll(r.cfg.Dir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(r.cfg.Dir, day+".csv")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, _ = f.WriteString("\xef\xbb\xbf") // BOM so Excel reads the UTF-8
	w := csv.NewWriter(f)
	_ = w.Write(CSVHeader)
	_ = w.WriteAll(CSVRows(r.loc, alerts))
	w.Flush()
	if werr := w.Error(); werr != nil {
		f.Close()
		os.Remove(tmp)
		return werr
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", final, err)
	}
	return nil
}

func (r *Recorder) csvExists(day string) bool {
	_, err := os.Stat(filepath.Join(r.cfg.Dir, day+".csv"))
	return err == nil
}
