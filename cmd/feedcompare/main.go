// Command feedcompare is a read-only side-by-side probe of the market-data
// feeds: it listens to Alpaca (and, only when asked, Massive) for N minutes
// and reports per-symbol 1-minute bar / volume differences, coverage
// (symbols seen) and latency.
//
// It never touches the running scanner: no shared connection, no database,
// no writes under ~/.marketscanner. The Massive side is OFF by default
// because Massive allows about one websocket per key - a second connection
// can disconnect the live scanner. Alpaca allows one stream per key too, so
// do not run this while the scanner itself runs with SCANNER_FEED=alpaca.
//
// Build into a scratch dir (never into the repo):
//
//	go build -o %TEMP%\feedcompare.exe ./cmd/feedcompare
//
// Usage:
//
//	feedcompare -minutes 10                 # Alpaca only: its trade-built bars vs its own minute bars
//	feedcompare -minutes 10 -massive        # also Massive (only with the scanner stopped!)
//	feedcompare -feed iex -syms AAPL,TSLA   # free IEX plan (30-symbol limit, no wildcard)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/alpaca"
	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

func main() {
	minutes := flag.Int("minutes", 5, "how long to listen")
	useMassive := flag.Bool("massive", false, "also connect to Massive (opens a SECOND Massive websocket: stop the scanner first)")
	useAlpaca := flag.Bool("alpaca", true, "connect to Alpaca")
	alpFeed := flag.String("feed", "sip", "Alpaca feed: sip or iex")
	symsFlag := flag.String("syms", "", "comma-separated symbols (default: whole market via \"*\")")
	top := flag.Int("top", 25, "rows in the per-symbol difference tables")
	flag.Parse()

	var syms []string
	for _, s := range strings.Split(*symsFlag, ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			syms = append(syms, s)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*minutes)*time.Minute)
	defer cancel()

	col := newCollector(syms)
	var wg sync.WaitGroup
	if *useAlpaca {
		keys, err := alpaca.LoadKeys(config.ConfigDir())
		if err != nil || !keys.Valid() {
			fmt.Fprintln(os.Stderr, "alpaca: no keys (create ~/.marketscanner/alpaca.json with {\"keyId\":\"...\",\"secret\":\"...\"})", errText(err))
			os.Exit(2)
		}
		ac := alpaca.New(alpaca.Config{Keys: keys, Feed: *alpFeed, Symbols: syms})
		aggs := make(chan core.Agg, 65536)
		bars := make(chan alpaca.MinuteBar, 65536)
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = ac.StreamWith(ctx, aggs, nil, alpaca.StreamOptions{
				Bars:    bars,
				OnTrade: func(t alpaca.Trade, recv int64) { col.latency("alpaca trade", recv-t.TS) },
			})
		}()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case a := <-aggs:
					col.agg("alpaca", a)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case b := <-bars:
					col.bar("alpaca bars", b.Sym, b.Bar, b.Updated)
					col.latency("alpaca bar", time.Now().UnixMilli()-(b.Bar.T+60_000))
				}
			}
		}()
	}
	if *useMassive {
		cfg, _ := config.Load()
		if cfg.MassiveKey == "" {
			fmt.Fprintln(os.Stderr, "massive: no key configured")
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "WARNING: opening a second Massive websocket; a running scanner may be disconnected.")
		mc := feed.New(feed.Config{APIKey: cfg.MassiveKey, WSURL: cfg.MassiveWSURL, RESTBase: cfg.MassiveBase})
		aggs := make(chan core.Agg, 65536)
		wg.Add(2)
		go func() { defer wg.Done(); _ = mc.Stream(ctx, aggs, nil) }()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case a := <-aggs:
					col.latency("massive agg", time.Now().UnixMilli()-a.EndMs)
					col.agg("massive", a)
				}
			}
		}()
	}
	fmt.Fprintf(os.Stderr, "listening for %d minute(s)...\n", *minutes)
	<-ctx.Done()
	wg.Wait()
	col.report(os.Stdout, *top)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return "(" + err.Error() + ")"
}

// ---- collection -----------------------------------------------------------

type minKey struct {
	sym string
	min int64 // minute start, epoch ms
}

type collector struct {
	mu      sync.Mutex
	filter  map[string]bool
	minutes map[string]map[minKey]core.Bar // source -> minute bars
	lat     map[string][]int64
}

func newCollector(syms []string) *collector {
	c := &collector{minutes: map[string]map[minKey]core.Bar{}, lat: map[string][]int64{}}
	if len(syms) > 0 {
		c.filter = map[string]bool{}
		for _, s := range syms {
			c.filter[s] = true
		}
	}
	return c
}

func (c *collector) keep(sym string) bool { return c.filter == nil || c.filter[sym] }

// agg rolls a per-second aggregate into its source's minute bar.
func (c *collector) agg(src string, a core.Agg) {
	if !c.keep(a.Sym) || a.EndMs <= 0 {
		return
	}
	k := minKey{a.Sym, ((a.EndMs - 1) / 60_000) * 60_000}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.src(src)
	b, ok := m[k]
	if !ok {
		b = core.Bar{T: k.min, O: a.Open, H: a.High, L: a.Low}
	}
	b.H, b.L, b.C = max(b.H, a.High), min(b.L, a.Low), a.Close
	b.V += a.Vol
	m[k] = b
}

// bar records a provider minute bar (an updated bar replaces the final one).
func (c *collector) bar(src, sym string, b core.Bar, updated bool) {
	if !c.keep(sym) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.src(src)[minKey{sym, b.T}] = b
}

func (c *collector) src(name string) map[minKey]core.Bar {
	m := c.minutes[name]
	if m == nil {
		m = map[minKey]core.Bar{}
		c.minutes[name] = m
	}
	return m
}

func (c *collector) latency(src string, ms int64) {
	c.mu.Lock()
	if len(c.lat[src]) < 2_000_000 {
		c.lat[src] = append(c.lat[src], ms)
	}
	c.mu.Unlock()
}

// ---- report ---------------------------------------------------------------

func pct(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	i := int(float64(len(xs)-1) * p)
	return xs[i]
}

type symDiff struct {
	sym          string
	volA, volB   int64
	mins, hlMiss int
}

// compare matches two sources' minute bars on (sym, minute). The newest
// minute of each symbol is skipped (it may still be forming).
func compare(a, b map[minKey]core.Bar) (diffs []symDiff, onlyA, onlyB []string) {
	last := map[string]int64{}
	for _, m := range []map[minKey]core.Bar{a, b} {
		for k := range m {
			if k.min > last[k.sym] {
				last[k.sym] = k.min
			}
		}
	}
	symsA, symsB := map[string]bool{}, map[string]bool{}
	for k := range a {
		symsA[k.sym] = true
	}
	for k := range b {
		symsB[k.sym] = true
	}
	by := map[string]*symDiff{}
	for k, ba := range a {
		if k.min == last[k.sym] {
			continue
		}
		bb, ok := b[k]
		if !ok {
			continue
		}
		d := by[k.sym]
		if d == nil {
			d = &symDiff{sym: k.sym}
			by[k.sym] = d
		}
		d.volA += ba.V
		d.volB += bb.V
		d.mins++
		if !near(ba.H, bb.H) || !near(ba.L, bb.L) {
			d.hlMiss++
		}
	}
	for _, d := range by {
		diffs = append(diffs, *d)
	}
	sort.Slice(diffs, func(i, j int) bool {
		return absI(diffs[i].volA-diffs[i].volB) > absI(diffs[j].volA-diffs[j].volB)
	})
	for s := range symsA {
		if !symsB[s] {
			onlyA = append(onlyA, s)
		}
	}
	for s := range symsB {
		if !symsA[s] {
			onlyB = append(onlyB, s)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return diffs, onlyA, onlyB
}

func near(x, y float64) bool { d := x - y; return d < 1e-6 && d > -1e-6 }

func absI(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func syms(m map[minKey]core.Bar) int {
	s := map[string]bool{}
	for k := range m {
		s[k.sym] = true
	}
	return len(s)
}

func (c *collector) report(w io.Writer, top int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintln(w, "== coverage (symbols seen) ==")
	names := make([]string, 0, len(c.minutes))
	for n := range c.minutes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-12s %6d symbols, %8d minute bars\n", n, syms(c.minutes[n]), len(c.minutes[n]))
	}
	fmt.Fprintln(w, "== latency, ms (receive time - event time) ==")
	ln := make([]string, 0, len(c.lat))
	for n := range c.lat {
		ln = append(ln, n)
	}
	sort.Strings(ln)
	for _, n := range ln {
		xs := c.lat[n]
		sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
		fmt.Fprintf(w, "  %-14s n=%-8d p50=%-6d p95=%-6d p99=%-6d max=%d\n", n, len(xs), pct(xs, .5), pct(xs, .95), pct(xs, .99), pct(xs, 1))
	}
	pairs := [][2]string{{"alpaca", "alpaca bars"}, {"massive", "alpaca"}, {"massive", "alpaca bars"}}
	for _, p := range pairs {
		a, b := c.minutes[p[0]], c.minutes[p[1]]
		if a == nil || b == nil {
			continue
		}
		diffs, onlyA, onlyB := compare(a, b)
		var va, vb int64
		hl, mins := 0, 0
		for _, d := range diffs {
			va, vb, hl, mins = va+d.volA, vb+d.volB, hl+d.hlMiss, mins+d.mins
		}
		fmt.Fprintf(w, "== %s vs %s ==\n", p[0], p[1])
		fmt.Fprintf(w, "  common: %d symbols, %d minutes; volume %d vs %d (%+.2f%%); high/low mismatches in %d minutes\n",
			len(diffs), mins, va, vb, 100*float64(vb-va)/float64(max(va, 1)), hl)
		fmt.Fprintf(w, "  only in %s: %d   only in %s: %d   (first: %s | %s)\n",
			p[0], len(onlyA), p[1], len(onlyB), head(onlyA, 10), head(onlyB, 10))
		fmt.Fprintf(w, "  %-8s %12s %12s %8s %6s %6s\n", "sym", p[0], p[1], "diff%", "mins", "h/l")
		for i, d := range diffs {
			if i >= top {
				break
			}
			fmt.Fprintf(w, "  %-8s %12d %12d %+8.2f %6d %6d\n", d.sym, d.volA, d.volB, 100*float64(d.volB-d.volA)/float64(max(d.volA, 1)), d.mins, d.hlMiss)
		}
	}
}

func head(xs []string, n int) string {
	if len(xs) > n {
		return strings.Join(xs[:n], ",") + ",..."
	}
	return strings.Join(xs, ",")
}
