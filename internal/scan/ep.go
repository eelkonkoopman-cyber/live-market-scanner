package scan

import (
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/ep"
)

// epHL is the newest earnings-matching headline seen for a symbol.
type epHL struct {
	ts int64
	hl string
}

// epState is the Episodic Pivot overlay's memory (Run goroutine only). All of
// it is per ET day except the earnings headlines (filtered by their own
// timestamp against the previous 16:00 ET) and the worker's daily refs.
type epState struct {
	day  string
	news map[string]epHL
	ref  map[string]core.EPRef
	qual map[string]bool    // passed EP Watch before the open today (latched)
	orh  map[string]float64 // 09:30-09:35 high, built while the range is open
	brk  map[string]int64   // first break above ORH after 09:35 (ms)
}

func newEPState() epState {
	return epState{news: map[string]epHL{}, ref: map[string]core.EPRef{}, qual: map[string]bool{}, orh: map[string]float64{}, brk: map[string]int64{}}
}

// markEPNews keeps the newest earnings-titled headline per ticker.
func (s *Scanner) markEPNews(items []core.NewsItem) {
	for _, it := range items {
		if !ep.IsEarnings(it.Headline) {
			continue
		}
		for _, sym := range it.Tickers {
			if cur, ok := s.ep.news[sym]; !ok || it.TS > cur.ts {
				s.ep.news[sym] = epHL{ts: it.TS, hl: it.Headline}
			}
		}
	}
	if len(s.ep.news) > 20000 {
		cutoff := time.Now().Add(-72 * time.Hour).UnixMilli()
		for sym, e := range s.ep.news {
			if e.ts < cutoff {
				delete(s.ep.news, sym)
			}
		}
	}
}

// SetEP merges the worker's per-candidate daily context (3-month change, ADR20).
func (s *Scanner) SetEP(m map[string]core.EPRef) {
	if len(m) == 0 {
		return
	}
	s.ctrl <- func(sc *Scanner) {
		for k, v := range m {
			sc.ep.ref[k] = v
		}
	}
}

// EPCandidates lists symbols that gap >= 10% on >= $5 and still lack a daily
// context today (the worker fetches those, at most max per call).
func (s *Scanner) EPCandidates(max int) []string {
	reply := make(chan []string, 1)
	s.ctrl <- func(sc *Scanner) {
		var out []string
		if sc.last != nil {
			for i := range sc.last.Rows {
				r := &sc.last.Rows[i]
				if _, ok := sc.ep.ref[r.Sym]; ok {
					continue
				}
				if g := epGap(r); !core.IsNull(g) && g >= ep.MinGapPct && r.Last >= ep.MinPrice {
					out = append(out, r.Sym)
					if len(out) >= max {
						break
					}
				}
			}
		}
		reply <- out
	}
	return <-reply
}

// epGap is the gap vs the previous close: the open's once the day has one,
// else the pre-market last (ChgDay).
func epGap(r *core.Row) float64 {
	if !core.IsNull(r.Gap) && r.Sess == core.SessionRegular {
		return r.Gap
	}
	return r.ChgDay
}

func clearEP(r *core.Row) {
	n := core.Null
	r.EpPass, r.EpGap, r.EpVolX, r.EpEarnAge, r.Ep3m, r.EpAdr, r.EpAdrDays = n, n, n, n, n, n, n
	r.EpStop, r.EpOrh, r.EpBrkAge, r.EpRiskPct = n, n, n, n
	r.EpEarnHL = ""
}

// overlayEP stamps the EP Watch / EP Break fields (see core.FieldEpPass) onto
// the snapshot rows. EP Watch rules apply 04:00-11:00 ET; the EP Break window
// is 09:35-11:00 against the 09:30-09:35 high (built from the regular-session
// high seen up to 09:35:00, so a scanner started after 09:35 has no ORH and
// never fires a break that day).
func (s *Scanner) overlayEP(snap *core.MetricSnapshot) {
	at := time.UnixMilli(snap.AsOf).In(s.etLoc)
	if day := at.Format("2006-01-02"); day != s.ep.day {
		// Only a real TRANSITION clears: the first snapshot must not wipe refs
		// the worker published before it arrived.
		if s.ep.day != "" {
			s.ep.ref = map[string]core.EPRef{}
			s.ep.qual = map[string]bool{}
			s.ep.orh = map[string]float64{}
			s.ep.brk = map[string]int64{}
		}
		s.ep.day = day
	}
	sec := at.Hour()*3600 + at.Minute()*60 + at.Second()
	inWindow := sec >= 4*3600 && sec < ep.BreakEndMin*60
	cut := ep.PrevClose16(at, s.etLoc).UnixMilli()
	orSecEnd := ep.OREndMin * 60
	for i := range snap.Rows {
		r := &snap.Rows[i]
		clearEP(r)
		if !inWindow {
			continue
		}
		gap := epGap(r)
		qual := s.ep.qual[r.Sym]
		if (core.IsNull(gap) || gap < ep.MinGapPct) && !qual {
			continue
		}
		// open (after 09:30) or last (pre-market) is the price the rules read.
		px := r.Last
		if r.Sess == core.SessionRegular && !core.IsNull(r.Gap) && !core.IsNull(r.ChgDay) && r.ChgDay > -99 {
			if pc := r.Last / (1 + r.ChgDay/100); pc > 0 {
				px = pc * (1 + r.Gap/100)
			}
		}
		ref, hasRef := s.ep.ref[r.Sym]
		chg3m, adr := core.Null, core.Null
		if hasRef {
			chg3m, adr = ref.Chg3m, ref.AdrPct
			r.Ep3m, r.EpAdr, r.EpAdrDays = ref.Chg3m, ref.AdrPct, float64(ref.AdrDays)
		}
		n, hasNews := s.ep.news[r.Sym]
		earn := hasNews && n.ts >= cut && n.ts <= snap.AsOf
		if earn {
			r.EpEarnAge = float64((snap.AsOf - n.ts) / 1000)
			r.EpEarnHL = n.hl
		}
		r.EpGap = gap
		r.EpVolX = ep.VolX(float64(r.Vol), r.AvgVol)
		pass := ep.Pass(ep.Input{GapPct: gap, Price: px, Vol: float64(r.Vol), AvgVol: r.AvgVol, Rvol: r.Rvol, Earnings: earn, Chg3m: chg3m})
		if pass {
			r.EpPass = 1
			if r.Sess == core.SessionPre {
				s.ep.qual[r.Sym] = true
				qual = true
			}
		}
		if stop := ep.StopPrice(px, adr); !core.IsNull(stop) {
			r.EpStop = stop
			if r.Last > 0 {
				r.EpRiskPct = (r.Last - stop) / r.Last * 100
			}
		}
		// Opening range and the break (regular session only).
		if r.Sess != core.SessionRegular || sec < ep.RegularOpenMn*60 {
			continue
		}
		if sec < orSecEnd && !core.IsNull(r.SessHigh) && r.SessHigh > 0 && (pass || qual) {
			if r.SessHigh > s.ep.orh[r.Sym] {
				s.ep.orh[r.Sym] = r.SessHigh
			}
			continue
		}
		orh, ok := s.ep.orh[r.Sym]
		if !ok || orh <= 0 {
			continue
		}
		r.EpOrh = orh
		if _, done := s.ep.brk[r.Sym]; !done && (pass || qual) && r.Last > orh {
			s.ep.brk[r.Sym] = snap.AsOf
		}
		if ms, done := s.ep.brk[r.Sym]; done {
			r.EpBrkAge = float64(max(snap.AsOf-ms, 0) / 1000)
		}
	}
}
