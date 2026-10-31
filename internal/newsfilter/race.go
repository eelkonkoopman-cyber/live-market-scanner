package newsfilter

import (
	"fmt"
	"html"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// This file is the pure, I/O-free half of the RTPR-vs-Newsfilter comparison:
// pairing the two sides' articles, the coverage and lead figures, and the
// plain-text report. Reading the day files lives in files.go and reading the
// scanner database in cmd/newsrace, so everything here is testable with
// synthetic data.

const (
	titleKeyLen  = 70                     // normalized title characters that identify a release
	pairWindow   = 6 * time.Hour          // publication times further apart than this never pair
	outlierLead  = 10 * time.Minute       // |lead| beyond this is flagged as a likely mis-pair or replay
	MinPairs     = 200                    // matched pairs needed before the report passes a verdict
	levelBand    = 500 * time.Millisecond // |median lead| inside this reads as "about level"
	nearWindow   = time.Minute            // unmatched articles this close in publication time with a shared ticker are near misses
	nearExamples = 3                      // example title pairs printed for them
)

// The four wire ids Newsfilter documents for press releases. Only these take
// part in the coverage figures; benzinga, reuters, sec-api and the rest are
// other products' rewrites of the same news and would blur a wire-vs-wire
// comparison.
var wireOrder = []string{"prnewswire", "globenewswire", "businesswire", "accesswire"}

var wireLabel = map[string]string{
	"prnewswire": "PR Newswire", "globenewswire": "GlobeNewswire",
	"businesswire": "Business Wire", "accesswire": "Accesswire",
}

// WireOfNewsfilter maps a Newsfilter source id ("prNewswire", "businesswire",
// ...) to its wire key, or "" when it is not one of the four wires.
func WireOfNewsfilter(src string) string {
	k := alnum(src)
	for _, w := range wireOrder {
		if k == w {
			return w
		}
	}
	return ""
}

// WireOfRTPR maps the wire name RTPR scrapes from its article pages ("PR
// Newswire", "Business Wire", "ACCESSWIRE", "GlobeNewswire", ...) to the same
// keys, or "" when Newsfilter does not list that source (Newsfile Corp, ...).
// Accesswire renamed itself ACCESS Newswire, so both spellings map.
func WireOfRTPR(source string) string {
	k := alnum(source)
	switch {
	case strings.Contains(k, "prnewswire"):
		return "prnewswire"
	case strings.Contains(k, "globenewswire"):
		return "globenewswire"
	case strings.Contains(k, "businesswire"):
		return "businesswire"
	case strings.Contains(k, "accesswire"), strings.Contains(k, "accessnewswire"):
		return "accesswire"
	}
	return ""
}

// alnum lowercases s and drops everything that is not a letter or digit.
func alnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TitleKey is the pairing key for a headline: HTML entities decoded,
// lowercased, every run of non-alphanumerics collapsed to one space, cut to
// the first 70 characters. RTPR's titles come out of an HTML parse (entities
// already decoded, curly quotes intact) while Newsfilter's arrive as JSON
// text, so quote style, dashes and "&amp;" are the differences this absorbs.
// The cut absorbs a trailing " - Company Name" or location tag one wire adds
// only when the text before it is already 70 characters long. A shorter
// headline has to match exactly, so a suffix, a prefix or a truncation on one
// side makes it two unmatched articles; nearMisses reports those.
func TitleKey(title string) string {
	title = html.UnescapeString(title)
	var b strings.Builder
	space := true // swallows leading separators
	n := 0
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		} else {
			continue
		}
		if n++; n >= titleKeyLen {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// RTPRArticle is one RTPR article as the scanner archived it
// (news_articles where via='rtpr'). ReceivedMs is stamped when the scanner had
// the article in hand, after RTPR's poll and permalink fetch, not when the
// wire published it.
type RTPRArticle struct {
	ID          string
	Title       string
	Source      string
	Tickers     []string
	PublishedMs int64
	ReceivedMs  int64
}

// Pair is one matched release. R and N index the slices given to Match. Lead
// is rtpr.received - newsfilter.received in ms: positive means Newsfilter had
// it first.
type Pair struct {
	R, N int
	Lead int64
}

// Matching is the outcome of Match.
type Matching struct {
	Pairs    []Pair // ordered by R
	RTPROnly []int  // indices into the RTPR slice, ascending
	NFOnly   []int  // indices into the Newsfilter slice, ascending
}

// normTicker uppercases a symbol and drops a "$" or exchange prefix.
func normTicker(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimPrefix(t, "$")
}

func normTickers(ts []string) []string {
	var out []string
	for _, t := range ts {
		if t = normTicker(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func sharesTicker(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func absI(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Match pairs RTPR articles with Newsfilter records. A pair needs the same
// TitleKey AND a shared ticker (or no tickers on one side) AND publication
// times within six hours when both are known. Press-release boilerplate
// repeats across days and companies, which is exactly what the ticker and
// window checks guard against. When a publication time is missing on either
// side the arrival times stand in for it, with the same window. Every article
// pairs at most once, nearest publication first, so a repeated headline pairs
// with its own occurrence rather than with whichever came first.
func Match(rtpr []RTPRArticle, nf []Record) Matching {
	byKey := make(map[string][]int, len(nf))
	nfSyms := make([][]string, len(nf))
	for j, rec := range nf {
		nfSyms[j] = normTickers(rec.Syms)
		if k := TitleKey(rec.Title); k != "" {
			byKey[k] = append(byKey[k], j)
		}
	}

	type edge struct {
		r, n int
		dt   int64
	}
	var edges []edge
	win := pairWindow.Milliseconds()
	for i, a := range rtpr {
		k := TitleKey(a.Title)
		if k == "" {
			continue
		}
		rs := normTickers(a.Tickers)
		for _, j := range byKey[k] {
			b := nf[j]
			if len(rs) > 0 && len(nfSyms[j]) > 0 && !sharesTicker(rs, nfSyms[j]) {
				continue
			}
			var dt int64
			if a.PublishedMs > 0 && b.Pub > 0 {
				dt = absI(a.PublishedMs - b.Pub)
			} else {
				dt = absI(a.ReceivedMs - b.Ms)
			}
			if dt > win {
				continue
			}
			edges = append(edges, edge{i, j, dt})
		}
	}
	sort.Slice(edges, func(x, y int) bool {
		if edges[x].dt != edges[y].dt {
			return edges[x].dt < edges[y].dt
		}
		if edges[x].r != edges[y].r {
			return edges[x].r < edges[y].r
		}
		return edges[x].n < edges[y].n
	})

	usedR := make([]bool, len(rtpr))
	usedN := make([]bool, len(nf))
	var m Matching
	for _, e := range edges {
		if usedR[e.r] || usedN[e.n] {
			continue
		}
		usedR[e.r], usedN[e.n] = true, true
		m.Pairs = append(m.Pairs, Pair{R: e.r, N: e.n, Lead: rtpr[e.r].ReceivedMs - nf[e.n].Ms})
	}
	sort.Slice(m.Pairs, func(x, y int) bool { return m.Pairs[x].R < m.Pairs[y].R })
	for i := range rtpr {
		if !usedR[i] {
			m.RTPROnly = append(m.RTPROnly, i)
		}
	}
	for j := range nf {
		if !usedN[j] {
			m.NFOnly = append(m.NFOnly, j)
		}
	}
	return m
}

// nearMisses finds unmatched RTPR articles and unmatched Newsfilter records
// that share a ticker and were published within nearWindow of each other (the
// arrival times stand in when a publication time is missing) yet did not pair
// because their headlines differ: a suffix, a prefix, a truncation, an entity
// or Unicode-form difference. Each article counts once, nearest first. A
// systematic headline difference on one wire would otherwise read as a genuine
// Newsfilter coverage hole, so the count and a few example title pairs go in
// the report.
func nearMisses(rw []RTPRArticle, nw []Record, m Matching) (int, []NearMissPair) {
	byTicker := map[string][]int{}
	for _, j := range m.NFOnly {
		for _, t := range normTickers(nw[j].Syms) {
			byTicker[t] = append(byTicker[t], j)
		}
	}
	type edge struct {
		r, n int
		dt   int64
	}
	var edges []edge
	win := nearWindow.Milliseconds()
	for _, i := range m.RTPROnly {
		a := rw[i]
		key := TitleKey(a.Title)
		for _, t := range normTickers(a.Tickers) {
			for _, j := range byTicker[t] {
				b := nw[j]
				var dt int64
				if a.PublishedMs > 0 && b.Pub > 0 {
					dt = absI(a.PublishedMs - b.Pub)
				} else {
					dt = absI(a.ReceivedMs - b.Ms)
				}
				if dt <= win && TitleKey(b.Title) != key {
					edges = append(edges, edge{i, j, dt})
				}
			}
		}
	}
	sort.Slice(edges, func(x, y int) bool {
		if edges[x].dt != edges[y].dt {
			return edges[x].dt < edges[y].dt
		}
		if edges[x].r != edges[y].r {
			return edges[x].r < edges[y].r
		}
		return edges[x].n < edges[y].n
	})
	usedR, usedN := map[int]bool{}, map[int]bool{}
	var ex []NearMissPair
	count := 0
	for _, e := range edges {
		if usedR[e.r] || usedN[e.n] {
			continue
		}
		usedR[e.r], usedN[e.n] = true, true
		count++
		if len(ex) < nearExamples {
			ex = append(ex, NearMissPair{RTPR: clip(rw[e.r].Title, 100), Newsfilter: clip(nw[e.n].Title, 100)})
		}
	}
	return count, ex
}

// --- when Newsfilter was actually connected ---

// Interval is a closed span of epoch milliseconds.
type Interval struct{ From, To int64 }

// Coverage says when the Newsfilter recorder was connected, built from the
// connected/dropped/alive/stopped events. An article RTPR delivered while the
// recorder was not connected says nothing about Newsfilter's coverage, so
// those are excluded from the miss counts.
type Coverage struct {
	Intervals      []Interval // connected spans, ascending, non-overlapping
	Reconnects     int        // connections lost inside the window while the recorder was running
	DisconnectedMs int64      // dropped -> next connected or stopped (or the report time), window part only
	OfflineMs      int64      // recorder stopped or died -> next connected (or the report time), window part only
}

// Covered reports whether the recorder was connected at ms.
func (c Coverage) Covered(ms int64) bool {
	i := sort.Search(len(c.Intervals), func(i int) bool { return c.Intervals[i].To >= ms })
	return i < len(c.Intervals) && c.Intervals[i].From <= ms
}

// CoveredMs is the connected time inside [from, to].
func (c Coverage) CoveredMs(from, to int64) int64 {
	var t int64
	for _, iv := range c.Intervals {
		lo, hi := max(iv.From, from), min(iv.To, to)
		if hi > lo {
			t += hi - lo
		}
	}
	return t
}

// BuildCoverage walks the events (and the records, which prove the connection
// was alive when they arrived) in time order. A connected event with the
// previous connection still open means that process died without a word: that
// connection is closed at the last sign of life. A dropped event closes at
// its Last frame time, because a silently dead socket's gap starts when the
// frames stopped, not when the stream noticed. A stopped event with nothing
// open means the process ended while the stream was down (a backoff wait, a
// failing dial): from then on the gap is the recorder being off, not Newsfilter
// dropping the connection.
//
// fromMs and toMs are the report window. Events from before fromMs are read
// (the state at the start of the window depends on them) but the counts cover
// only the window: a drop outside it is not a reconnect, and a gap that
// straddles an edge contributes only the part inside. toMs doubles as "now": a
// gap still open then runs up to it.
func BuildCoverage(events []Event, records []Record, fromMs, toMs int64) Coverage {
	type item struct {
		ms   int64
		ev   *Event
		rank int
	}
	items := make([]item, 0, len(events)+len(records))
	for i := range events {
		items = append(items, item{ms: events[i].Ms, ev: &events[i]})
	}
	for i := range records {
		items = append(items, item{ms: records[i].Ms, rank: 1}) // after events sharing a millisecond
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].ms != items[b].ms {
			return items[a].ms < items[b].ms
		}
		return items[a].rank < items[b].rank
	})

	var c Coverage
	var (
		open     bool
		from     int64
		alive    int64
		haveEnd  bool
		lastEnd  int64
		lastKind string
	)
	closeAt := func(to int64, kind string) {
		to = max(to, from)
		c.Intervals = append(c.Intervals, Interval{From: from, To: to})
		open, haveEnd, lastEnd, lastKind = false, true, to, kind
	}
	inWindow := func(a, b int64) int64 { // the part of [a, b] inside the report window
		return max(min(b, toMs)-max(a, fromMs), 0)
	}
	openAt := func(ms int64) {
		if open {
			closeAt(alive, "crash")
		}
		if haveEnd && ms > lastEnd {
			if lastKind == EvDropped {
				c.DisconnectedMs += inWindow(lastEnd, ms)
			} else {
				c.OfflineMs += inWindow(lastEnd, ms)
			}
		}
		open, from, alive = true, ms, ms
	}
	for _, it := range items {
		if it.ev == nil {
			if open {
				alive = max(alive, it.ms)
			}
			continue
		}
		switch it.ev.Ev {
		case EvConnected:
			openAt(it.ms)
		case EvAlive:
			if open {
				alive = max(alive, it.ms)
			} else {
				openAt(it.ms) // the connected event is older than the files read: the heartbeat starts what is known
			}
		case EvDropped:
			if open {
				end := it.ms
				if l := it.ev.Last; l >= from && l < end {
					end = l
				}
				closeAt(end, EvDropped)
				if it.ms >= fromMs && it.ms <= toMs {
					c.Reconnects++
				}
			}
		case EvStopped:
			if open {
				closeAt(it.ms, EvStopped)
			} else if haveEnd && lastKind == EvDropped {
				c.DisconnectedMs += inWindow(lastEnd, it.ms)
				lastEnd, lastKind = max(lastEnd, it.ms), EvStopped
			}
		}
	}
	if open {
		closeAt(alive, "open")
	} else if haveEnd && toMs > lastEnd {
		switch lastKind {
		case EvDropped:
			c.DisconnectedMs += inWindow(lastEnd, toMs) // still down
		case EvStopped:
			c.OfflineMs += inWindow(lastEnd, toMs) // still not running
		}
	}
	return c
}

// --- the report ---

// Options tunes Analyze.
type Options struct {
	MaxPx    float64            // "own stocks": latest close at or under this
	Prices   map[string]float64 // latest daily close by ticker
	MinPairs int                // matched pairs needed for a verdict; default MinPairs
}

// Lead summarizes lead times (rtpr.received - newsfilter.received, ms).
type Lead struct {
	N        int
	NFFirst  int // pairs where Newsfilter arrived strictly first
	P10, P50 int64
	P90      int64
	Outliers int // |lead| beyond ten minutes
}

// WireStat is the per-wire coverage tally.
type WireStat struct {
	Wire         string
	RTPR         int // RTPR articles on this wire that count (matched + RTPR-only)
	NF           int // Newsfilter records that name a ticker on this wire (MatchedNSyms + NFOnly)
	MatchedR     int // matched pairs, counted by the RTPR article's wire
	MatchedN     int // matched pairs, counted by the Newsfilter record's wire
	MatchedNSyms int // of MatchedN, those whose Newsfilter record names a ticker
	RTPROnly     int
	NFOnly       int // Newsfilter-only records that name a ticker
	NFNoSyms     int // Newsfilter-only records with no ticker (not counted in NF)
}

// NearMissPair is an unmatched RTPR article and an unmatched Newsfilter record
// that look like one release under different headline wording.
type NearMissPair struct{ RTPR, Newsfilter string }

// Report is everything the command prints.
type Report struct {
	FromMs, ToMs int64

	RTPRTotal      int
	RTPRWire       int
	NotListed      map[string]int // RTPR source -> articles on wires Newsfilter does not list
	NotListedTotal int
	NFTotal        int
	NFWire         int
	NFOther        map[string]int // Newsfilter source -> non-wire records
	Matched        int
	MatchedSyms    int // of Matched, pairs whose Newsfilter record names a ticker
	WireMismatch   int // pairs whose two wires differ (a mapping or pairing smell)
	RTPROnly       int
	NFOnly         int // unmatched Newsfilter wire records that name a ticker: the ones that count against RTPR
	NFNoSyms       int // unmatched Newsfilter wire records with no ticker (private companies and the like): RTPR's feed is keyed by ticker, so these say nothing about its coverage
	ExcludedGap    int // unmatched RTPR articles dropped from the miss counts: recorder was not connected
	NearMiss       int // unmatched pairs sharing a ticker and publication time but not a headline
	NearMissEx     []NearMissPair
	Wires          []WireStat

	Lead Lead // every matched pair
	Own  Lead // pairs on the user's own (low-priced) stocks
	Opts Options

	Cov     Coverage
	Covered int64 // connected ms inside the window
	Verdict string
}

// Analyze pairs the two sides and computes the report. rtpr and nf should
// already be restricted to the window [fromMs, toMs].
func Analyze(rtpr []RTPRArticle, nf []Record, cov Coverage, opt Options, fromMs, toMs int64) Report {
	if opt.MinPairs <= 0 {
		opt.MinPairs = MinPairs
	}
	rep := Report{
		FromMs: fromMs, ToMs: toMs, Opts: opt, Cov: cov,
		RTPRTotal: len(rtpr), NFTotal: len(nf),
		NotListed: map[string]int{}, NFOther: map[string]int{},
		Covered: cov.CoveredMs(fromMs, toMs),
	}

	// Only wire articles take part: RTPR sources Newsfilter does not list are
	// reported on their own rather than counted as misses.
	var rw []RTPRArticle
	for _, a := range rtpr {
		if WireOfRTPR(a.Source) == "" {
			rep.NotListed[sourceLabel(a.Source)]++
			rep.NotListedTotal++
			continue
		}
		rw = append(rw, a)
	}
	var nw []Record
	for _, r := range nf {
		if WireOfNewsfilter(r.Src) == "" {
			rep.NFOther[sourceLabel(r.Src)]++
			continue
		}
		nw = append(nw, r)
	}
	rep.RTPRWire, rep.NFWire = len(rw), len(nw)

	stat := map[string]*WireStat{}
	for _, w := range wireOrder {
		stat[w] = &WireStat{Wire: w}
	}
	m := Match(rw, nw)
	var leads, own []int64
	for _, p := range m.Pairs {
		a, b := rw[p.R], nw[p.N]
		wr, wn := WireOfRTPR(a.Source), WireOfNewsfilter(b.Src)
		stat[wr].MatchedR++
		stat[wn].MatchedN++
		if len(normTickers(b.Syms)) > 0 {
			stat[wn].MatchedNSyms++
			rep.MatchedSyms++
		}
		if wr != wn {
			rep.WireMismatch++
		}
		leads = append(leads, p.Lead)
		if opt.isOwn(a.Tickers, b.Syms) {
			own = append(own, p.Lead)
		}
	}
	rep.Matched = len(m.Pairs)
	for _, i := range m.RTPROnly {
		a := rw[i]
		// Delivered while the recorder was not connected: Newsfilter could not
		// have had it, so it is not a miss. Either end of the article counts.
		if (a.PublishedMs > 0 && !cov.Covered(a.PublishedMs)) || !cov.Covered(a.ReceivedMs) {
			rep.ExcludedGap++
			continue
		}
		rep.RTPROnly++
		stat[WireOfRTPR(a.Source)].RTPROnly++
	}
	for _, j := range m.NFOnly {
		s := stat[WireOfNewsfilter(nw[j].Src)]
		// RTPR's feed is keyed by ticker, so a release naming none (a private
		// company) says nothing about RTPR's coverage: count it apart.
		if len(normTickers(nw[j].Syms)) == 0 {
			rep.NFNoSyms++
			s.NFNoSyms++
			continue
		}
		rep.NFOnly++
		s.NFOnly++
	}
	for _, w := range wireOrder {
		s := stat[w]
		s.RTPR = s.MatchedR + s.RTPROnly
		s.NF = s.MatchedNSyms + s.NFOnly
		rep.Wires = append(rep.Wires, *s)
	}
	rep.NearMiss, rep.NearMissEx = nearMisses(rw, nw, m)
	rep.Lead, rep.Own = leadStats(leads), leadStats(own)
	rep.Verdict = rep.verdict()
	return rep
}

// isOwn reports whether a pair concerns one of the user's own stocks: a ticker
// with a known latest close at or under MaxPx. Shared tickers decide when both
// sides have them (a release naming many symbols is judged on the ones both
// feeds agree on); otherwise whichever side has tickers does.
func (o Options) isOwn(rt, ns []string) bool {
	if o.MaxPx <= 0 || len(o.Prices) == 0 {
		return false
	}
	a, b := normTickers(rt), normTickers(ns)
	var pool []string
	switch {
	case len(a) > 0 && len(b) > 0:
		for _, x := range a {
			if sharesTicker([]string{x}, b) {
				pool = append(pool, x)
			}
		}
	case len(a) > 0:
		pool = a
	default:
		pool = b
	}
	for _, t := range pool {
		if px, ok := o.Prices[t]; ok && px > 0 && px <= o.MaxPx {
			return true
		}
	}
	return false
}

func sourceLabel(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "(none)"
	}
	return s
}

func leadStats(leads []int64) Lead {
	l := Lead{N: len(leads)}
	if l.N == 0 {
		return l
	}
	xs := append([]int64(nil), leads...)
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	for _, x := range xs {
		if x > 0 {
			l.NFFirst++
		}
		if absI(x) > outlierLead.Milliseconds() {
			l.Outliers++
		}
	}
	l.P10, l.P50, l.P90 = percentile(xs, 0.10), percentile(xs, 0.50), percentile(xs, 0.90)
	return l
}

// percentile is the nearest-rank percentile of an ascending slice: the
// smallest value with at least p of the data at or below it.
func percentile(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	k := int(math.Ceil(p*float64(len(xs)))) - 1
	return xs[min(max(k, 0), len(xs)-1)]
}

func pct(n, d int) float64 { return pct64(int64(n), int64(d)) }

func pct64(n, d int64) float64 {
	if d <= 0 {
		return math.NaN()
	}
	return 100 * float64(n) / float64(d)
}

func fmtPct(f float64) string {
	if math.IsNaN(f) {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", f)
}

// NFCoverage is the share of RTPR's countable wire articles Newsfilter also had.
func (r Report) NFCoverage() float64 { return pct(r.Matched, r.Matched+r.RTPROnly) }

// RTPRCoverage is the share of Newsfilter's wire records that name a ticker
// which RTPR also had. Records without a ticker are left out (NFNoSyms): RTPR's
// feed is keyed by ticker. It is a floor on RTPR's real coverage, because the
// scanner archives only part of what RTPR delivers (see Write).
func (r Report) RTPRCoverage() float64 { return pct(r.MatchedSyms, r.MatchedSyms+r.NFOnly) }

func (r Report) verdict() string {
	n := r.Lead.N
	if n < r.Opts.MinPairs {
		return fmt.Sprintf("not enough data yet (%d matched pairs, need %d)", n, r.Opts.MinPairs)
	}
	speed := "about level on speed"
	switch med := time.Duration(r.Lead.P50) * time.Millisecond; {
	case med > levelBand:
		speed = fmt.Sprintf("Newsfilter is usually earlier (median %s ahead)", fmtDur(med))
	case med < -levelBand:
		speed = fmt.Sprintf("RTPR is usually earlier (median %s ahead)", fmtDur(-med))
	}
	// The coverage figures are qualified in place: the line is what a cancel
	// decision is read from, and each figure leaves out something (articles that
	// fell in a gap, wires Newsfilter does not list, releases without a ticker).
	v := fmt.Sprintf("%d matched pairs: %s; Newsfilter first on %s; "+
		"Newsfilter carries %s of RTPR's articles on the four shared wires while it was connected "+
		"(%d more excluded, connected %s of the window; %d, %s of RTPR, are on wires Newsfilter does not list); "+
		"RTPR carries %s of Newsfilter's ticker-bearing wire articles (%d without a ticker left out)",
		n, speed, fmtPct(pct(r.Lead.NFFirst, n)),
		fmtPct(r.NFCoverage()),
		r.ExcludedGap, fmtPct(pct64(r.Covered, r.ToMs-r.FromMs)), r.NotListedTotal, fmtPct(pct(r.NotListedTotal, r.RTPRTotal)),
		fmtPct(r.RTPRCoverage()), r.NFNoSyms)
	if r.NearMiss > 0 {
		v += fmt.Sprintf("; %d unmatched pairs look like headline wording differences, not misses", r.NearMiss)
	}
	return v
}

// fmtDur renders a duration as seconds with two decimals, or minutes and
// seconds from two minutes up.
func fmtDur(d time.Duration) string {
	if d < 0 {
		return "-" + fmtDur(-d)
	}
	if d < 2*time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	d = d.Round(time.Second)
	if d < 2*time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func fmtLead(ms int64) string {
	s := fmtDur(time.Duration(ms) * time.Millisecond)
	if ms >= 0 {
		return "+" + s
	}
	return s
}

func topSources(m map[string]int) string {
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].v != kvs[j].v {
			return kvs[i].v > kvs[j].v
		}
		return kvs[i].k < kvs[j].k
	})
	parts := make([]string, 0, len(kvs))
	for i, e := range kvs {
		if i == 6 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", e.k, e.v))
	}
	return strings.Join(parts, ", ")
}

func (l Lead) write(w io.Writer, indent string) {
	if l.N == 0 {
		fmt.Fprintf(w, "%sno matched pairs\n", indent)
		return
	}
	fmt.Fprintf(w, "%sNewsfilter first on %s of %d pairs\n", indent, fmtPct(pct(l.NFFirst, l.N)), l.N)
	fmt.Fprintf(w, "%slead p10 %s   p50 %s   p90 %s\n", indent, fmtLead(l.P10), fmtLead(l.P50), fmtLead(l.P90))
	if l.Outliers > 0 {
		fmt.Fprintf(w, "%s%d pairs differ by more than %s (likely a mis-pair or a replay)\n", indent, l.Outliers, fmtDur(outlierLead))
	}
}

// Write prints the report as plain text.
func (r Report) Write(w io.Writer) {
	from := time.UnixMilli(r.FromMs).UTC().Format("2006-01-02 15:04")
	to := time.UnixMilli(r.ToMs).UTC().Format("2006-01-02 15:04")
	fmt.Fprintf(w, "Newsfilter vs RTPR, %s to %s UTC\n", from, to)
	fmt.Fprintln(w, "lead = RTPR received - Newsfilter received (positive = Newsfilter first).")
	fmt.Fprintln(w, "RTPR received is when the scanner had finished fetching the article (its 3 s poll and page fetch included);")
	fmt.Fprintln(w, "Newsfilter received is when its frame came off the socket.")
	fmt.Fprintln(w)

	fmt.Fprintf(w, "RTPR articles:        %d  (wire %d; not on Newsfilter's list %d", r.RTPRTotal, r.RTPRWire, r.NotListedTotal)
	if r.NotListedTotal > 0 {
		fmt.Fprintf(w, ": %s", topSources(r.NotListed))
	}
	fmt.Fprintln(w, ")")
	other := r.NFTotal - r.NFWire
	fmt.Fprintf(w, "Newsfilter records:   %d  (wire %d; other sources %d", r.NFTotal, r.NFWire, other)
	if other > 0 {
		fmt.Fprintf(w, ": %s", topSources(r.NFOther))
	}
	fmt.Fprintln(w, ")")
	fmt.Fprintf(w, "Matched pairs:        %d", r.Matched)
	if r.WireMismatch > 0 {
		fmt.Fprintf(w, "  (%d with differing wires)", r.WireMismatch)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Coverage, wire articles only (%d RTPR articles excluded: Newsfilter was not connected when they came in)\n", r.ExcludedGap)
	fmt.Fprintf(w, "  Newsfilter had %s of RTPR's articles   (RTPR-only %d)\n", fmtPct(r.NFCoverage()), r.RTPROnly)
	fmt.Fprintf(w, "  RTPR had %s of Newsfilter's ticker-bearing articles   (Newsfilter-only %d; %d more without a ticker not counted)\n",
		fmtPct(r.RTPRCoverage()), r.NFOnly, r.NFNoSyms)
	fmt.Fprintf(w, "  %-14s %8s %10s %9s %11s %12s\n", "wire", "matched", "rtpr-only", "nf-only", "NF has", "RTPR has")
	for _, s := range r.Wires {
		fmt.Fprintf(w, "  %-14s %8d %10d %9d %11s %12s\n", wireLabel[s.Wire], s.MatchedR, s.RTPROnly, s.NFOnly,
			fmtPct(pct(s.MatchedR, s.RTPR)), fmtPct(pct(s.MatchedNSyms, s.NF)))
	}
	fmt.Fprintln(w, "  (NF has = share of RTPR's articles on that wire that Newsfilter also had; RTPR has = the reverse, for articles that name a ticker)")
	fmt.Fprintln(w, "  RTPR's share of Newsfilter's articles is a floor: the scanner archives only part of what RTPR delivers (non-English")
	fmt.Fprintln(w, "  editions are gated out, archive writes are dropped when the writer falls behind, the first poll after a start is a")
	fmt.Fprintln(w, "  baseline), and an article missing from the archive reads as an RTPR miss here.")
	fmt.Fprintf(w, "  Near misses: %d unmatched pairs share a ticker and a publication time (within %d s) but not a headline\n", r.NearMiss, int(nearWindow.Seconds()))
	for _, e := range r.NearMissEx {
		fmt.Fprintf(w, "    RTPR:       %s\n    Newsfilter: %s\n", e.RTPR, e.Newsfilter)
	}
	if r.NearMiss > 0 {
		fmt.Fprintln(w, "  (a systematic headline difference on one wire inflates both miss counts above)")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Lead, all matched pairs")
	r.Lead.write(w, "  ")
	if r.Opts.MaxPx > 0 {
		fmt.Fprintf(w, "Lead, own stocks (latest close <= $%.2f)\n", r.Opts.MaxPx)
		r.Own.write(w, "  ")
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Newsfilter connection: %d reconnects, disconnected %s, recorder not running %s, connected %s of the window\n",
		r.Cov.Reconnects, fmtDur(time.Duration(r.Cov.DisconnectedMs)*time.Millisecond),
		fmtDur(time.Duration(r.Cov.OfflineMs)*time.Millisecond), fmtDur(time.Duration(r.Covered)*time.Millisecond))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Verdict: %s.\n", r.Verdict)
}
