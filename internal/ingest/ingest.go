// Package ingest bridges the feed into the engine: it seeds day-state from the
// REST snapshot, streams per-second aggregates (filtered to the universe) into
// the engine, and re-seeds on every reconnect. Feed failures degrade the app
// (status -> down) rather than killing it.
package ingest

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
	"github.com/eelkonkoopman/marketscanner/internal/universe"
)

const (
	stDown = iota
	stConnecting
	stLive
)

type Ingest struct {
	feed core.FeedClient
	eng  core.Engine
	uni  *universe.Universe

	state   atomic.Int32
	applied atomic.Int64
	rate    atomic.Int64

	// Live-chart tap: per-second aggregates for whichever symbols currently
	// have a window's SymbolPanel open, teed off the ingest path so each
	// window's chart keeps updating after its initial REST snapshot.
	chartSyms atomic.Pointer[map[string]struct{}]
	chartOut  chan core.Agg

	// 9EMA-flag tap: a second tee, identical in shape to the chart tap, for the
	// symbols the flagscan service is currently evaluating (the pre-market
	// volume leaders, up to ~80). Kept separate from chartSyms because the two
	// have different owners, sets and loss semantics: a chart window's set is
	// whatever the user has open, while this one is replaced wholesale every
	// ~30 s by the service. flagDrops counts the aggregates the non-blocking
	// send had to discard (see FlagDrops).
	flagSyms  atomic.Pointer[map[string]struct{}]
	flagOut   chan core.Agg
	flagDrops atomic.Int64

	// Order-ticket tap: LOSSLESS, unlike the chart tap. The soft stop reads
	// its prices here, so a full channel must never drop one: every aggregate
	// of a ticket symbol is queued per symbol (bounded; on overflow the newest
	// is merged into the last queued one - high/low/close/volume kept) and a
	// 1-slot notify channel wakes the single consumer, which drains them all.
	ticketSyms atomic.Pointer[map[string]struct{}]
	tkMu       sync.Mutex
	tkPend     map[string][]core.Agg
	tkOrder    []string // symbols in first-arrival order since the last drain
	tkNote     chan struct{}
	tkMerged   atomic.Int64

	// onConnect (optional) runs after each (re)connect's re-seed: cmd/scanner
	// wires prefill.Worker.Reseed, which re-seeds session VWAPs from REST bars.
	onConnect atomic.Pointer[func()]
}

// SetOnConnect registers a hook run after every stream (re)connect.
func (i *Ingest) SetOnConnect(f func()) { i.onConnect.Store(&f) }

// flagOutBuf is the flag tap's channel capacity. At the candidate cap (~80
// symbols, one aggregate per symbol per second at most) the service sees well
// under 100 aggregates a second, so 4096 is roughly a minute of slack for a
// consumer that stalls (a REST backfill burst, a GC pause) before the first
// drop -- and the chart tap's 512 would not be, with 80 symbols instead of a
// couple of open windows.
const flagOutBuf = 4096

func New(feed core.FeedClient, eng core.Engine, uni *universe.Universe) *Ingest {
	return &Ingest{
		feed: feed, eng: eng, uni: uni,
		chartOut: make(chan core.Agg, 512),
		flagOut:  make(chan core.Agg, flagOutBuf),
		tkPend:   map[string][]core.Agg{},
		tkNote:   make(chan struct{}, 1),
	}
}

// symSet builds a tap's membership set, ignoring empty symbols.
func symSet(syms []string) *map[string]struct{} {
	set := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		if s != "" {
			set[s] = struct{}{}
		}
	}
	return &set
}

// SetChartSyms points the live-chart tap at exactly this symbol set (nil/empty
// disables it). Holds no per-symbol state (unlike the tape engine), so this is
// a plain atomic replace, not a reconcile — the tap has nothing to preserve.
func (i *Ingest) SetChartSyms(syms []string) { i.chartSyms.Store(symSet(syms)) }

// tkMaxPerSym bounds one symbol's undrained queue (~4 min of seconds).
const tkMaxPerSym = 256

// SetTicketSyms points the lossless order-ticket tap at exactly this set.
func (i *Ingest) SetTicketSyms(syms []string) { i.ticketSyms.Store(symSet(syms)) }

// TicketNotify fires (coalesced) when ticket aggregates are waiting.
func (i *Ingest) TicketNotify() <-chan struct{} { return i.tkNote }

// DrainTicket returns every queued ticket aggregate (per symbol in arrival
// order) and empties the queue.
func (i *Ingest) DrainTicket() []core.Agg {
	i.tkMu.Lock()
	defer i.tkMu.Unlock()
	var out []core.Agg
	for _, s := range i.tkOrder {
		out = append(out, i.tkPend[s]...)
		delete(i.tkPend, s)
	}
	i.tkOrder = i.tkOrder[:0]
	return out
}

// TicketMerged counts aggregates merged (not lost) because a queue was full.
func (i *Ingest) TicketMerged() int64 { return i.tkMerged.Load() }

func (i *Ingest) teeTicket(a core.Agg) {
	p := i.ticketSyms.Load()
	if p == nil {
		return
	}
	if _, ok := (*p)[a.Sym]; !ok {
		return
	}
	i.tkMu.Lock()
	q, had := i.tkPend[a.Sym]
	if !had {
		i.tkOrder = append(i.tkOrder, a.Sym)
	}
	if len(q) >= tkMaxPerSym {
		m := &q[len(q)-1]
		if a.High > m.High {
			m.High = a.High
		}
		if a.Low > 0 && (m.Low <= 0 || a.Low < m.Low) {
			m.Low = a.Low
		}
		m.Close, m.Vol, m.DayVWAP, m.AccVol, m.EndMs = a.Close, m.Vol+a.Vol, a.DayVWAP, a.AccVol, a.EndMs
		i.tkMerged.Add(1)
	} else {
		q = append(q, a)
	}
	i.tkPend[a.Sym] = q
	i.tkMu.Unlock()
	select {
	case i.tkNote <- struct{}{}:
	default: // a wake-up is already pending; it drains this one too
	}
}

// ChartAggs streams per-second aggregates for the selected symbol.
func (i *Ingest) ChartAggs() <-chan core.Agg { return i.chartOut }

// SetFlagSyms points the 9EMA-flag tap at exactly this symbol set (nil/empty
// disables it): a plain atomic replace, like SetChartSyms, so the flagscan
// service can call it every candidate refresh without coordinating with the
// pump. The service sets the tap BEFORE it queues a symbol's REST backfill, so
// no second between the backfill's cut-off and the first live print is lost.
func (i *Ingest) SetFlagSyms(syms []string) { i.flagSyms.Store(symSet(syms)) }

// FlagAggs streams the per-second aggregates of the symbols in the flag tap,
// in arrival order. Single consumer (the flagscan service).
func (i *Ingest) FlagAggs() <-chan core.Agg { return i.flagOut }

// FlagDrops is the running count of flag-tap aggregates discarded because the
// consumer's channel was full. The tee must never block the pump (that would
// stall the whole market feed behind a slow flag service), so a full channel
// drops the aggregate instead -- and unlike the chart tap, where the next
// second's aggregate supersedes a dropped one, a dropped flag aggregate loses
// that second's volume and its high/low from the forming 1-minute bar. The
// service logs this counter once a minute and its periodic REST resync heals
// the bar; a steadily rising count means the consumer is not keeping up.
func (i *Ingest) FlagDrops() int64 { return i.flagDrops.Load() }

// State returns the feed connection state for the status frame.
func (i *Ingest) State() string {
	switch i.state.Load() {
	case stLive:
		return "live"
	case stConnecting:
		return "reconnecting"
	default:
		return "down"
	}
}

// Rate returns the most recent per-second applied-aggregate count.
func (i *Ingest) Rate() int { return int(i.rate.Load()) }

func (i *Ingest) Run(ctx context.Context) error {
	go i.sampleRate(ctx)

	i.seed(ctx) // initial seed before streaming
	out := make(chan core.Agg, 16384)
	go i.pump(ctx, out)

	i.state.Store(stConnecting)
	onReset := func() {
		i.state.Store(stLive)
		i.seed(ctx) // re-seed day-state on each (re)connect
		if f := i.onConnect.Load(); f != nil && *f != nil {
			(*f)()
		}
	}
	// Stream returns nil on ctx cancel, or a fatal error on bad auth. Either way
	// we keep the process up so the UI stays reachable (status -> down).
	if err := i.feed.Stream(ctx, out, onReset); err != nil {
		logx.L().Error("feed stream ended; UI stays up, feed=down", "err", err)
	}
	i.state.Store(stDown)
	return nil
}

func (i *Ingest) pump(ctx context.Context, out <-chan core.Agg) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-out:
			i.teeTicket(a) // first: the soft stop's lossless feed
			// Tee to any live charts before the universe filter, so a selected
			// symbol keeps streaming even if the universe hasn't loaded yet.
			if p := i.chartSyms.Load(); p != nil {
				if _, ok := (*p)[a.Sym]; ok {
					select {
					case i.chartOut <- a:
					default: // UI behind; the next second's agg supersedes
					}
				}
			}
			// Same tee for the 9EMA-flag candidates, same place (before the
			// universe filter) and same non-blocking send -- but a drop here is
			// counted, because it loses a second of a forming bar (FlagDrops).
			if p := i.flagSyms.Load(); p != nil {
				if _, ok := (*p)[a.Sym]; ok {
					select {
					case i.flagOut <- a:
					default:
						i.flagDrops.Add(1)
					}
				}
			}
			if i.uni.Contains(a.Sym) {
				i.eng.Apply(a)
				i.applied.Add(1)
			}
		}
	}
}

func (i *Ingest) seed(ctx context.Context) {
	seeds, err := i.feed.Snapshot(ctx)
	if err != nil {
		logx.L().Warn("snapshot seed failed", "err", err)
		return
	}
	n := 0
	for _, s := range seeds {
		if !i.uni.Contains(s.Sym) {
			continue
		}
		i.eng.Seed(s)
		n++
	}
	logx.L().Info("seeded day-state", "tickers", n)
}

func (i *Ingest) sampleRate(ctx context.Context) {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	var last int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			cur := i.applied.Load()
			i.rate.Store(cur - last)
			last = cur
		}
	}
}
