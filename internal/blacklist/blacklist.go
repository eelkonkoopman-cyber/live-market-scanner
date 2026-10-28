// Package blacklist is the user's per-day symbol blacklist: symbols whose scan
// alerts are silenced today (the Signal window) and whose Day Log alerts are
// flagged "blacklisted". The list belongs to one ET day: it is persisted in
// schema_meta with that day, so a restart the same day keeps it, and it is
// cleared at the ET day roll (00:00 ET); a list kept for an earlier day is
// ignored. The meta key and JSON format are the ones the (removed) auto-trader
// used, so a list saved before its removal carries over.
package blacklist

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MetaKey is the schema_meta key the list is kept under (unchanged from the
// auto-trader's, so today's list survives).
const MetaKey = "autotrade.blacklist.v1"

type meta struct {
	Day  string   `json:"day"`
	Syms []string `json:"syms"`
}

var etLoc = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("ET", -5*3600)
	}
	return l
}()

// ETDay is the ET calendar day of ms (YYYY-MM-DD).
func ETDay(ms int64) string { return time.UnixMilli(ms).In(etLoc).Format("2006-01-02") }

var validSym = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,11}$`)

// Norm is sym as the list keys it.
func Norm(sym string) string { return strings.ToUpper(strings.TrimSpace(sym)) }

// Config wires the persistence; nil funcs keep the list in memory only.
type Config struct {
	GetMeta func(ctx context.Context, key string) (string, error)
	SetMeta func(ctx context.Context, key, val string) error
	Now     func() time.Time // default time.Now
}

// List is today's blacklist. Safe for concurrent use.
type List struct {
	cfg     Config
	mu      sync.Mutex
	day     string
	syms    map[string]struct{}
	changed chan struct{}
}

// New builds the list and loads today's kept list (one of an earlier ET day is ignored).
func New(cfg Config) *List {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	l := &List{cfg: cfg, changed: make(chan struct{}, 1)}
	l.day = l.today()
	l.load()
	return l
}

func (l *List) today() string { return ETDay(l.cfg.Now().UnixMilli()) }

// Changed fires (coalesced) after every add / remove / day roll.
func (l *List) Changed() <-chan struct{} { return l.changed }

func (l *List) notify() {
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// roll drops the list when the ET day changed. Caller holds mu.
func (l *List) roll() {
	if d := l.today(); d != l.day {
		had := len(l.syms) > 0
		l.day, l.syms = d, nil
		if had {
			l.save()
			l.notify()
		}
	}
}

// Has reports whether sym is on today's list.
func (l *List) Has(sym string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	_, ok := l.syms[Norm(sym)]
	return ok
}

// View is today's list, sorted (never nil).
func (l *List) View() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	out := make([]string, 0, len(l.syms))
	for s := range l.syms {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Add puts sym on today's list (a no-op when it is already there).
func (l *List) Add(sym string) error {
	s := Norm(sym)
	if !validSym.MatchString(s) {
		return fmt.Errorf("blacklist: %q is not a symbol", sym)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	if _, ok := l.syms[s]; ok {
		return nil
	}
	if l.syms == nil {
		l.syms = map[string]struct{}{}
	}
	l.syms[s] = struct{}{}
	err := l.save()
	l.notify()
	return err
}

// Remove takes sym off today's list.
func (l *List) Remove(sym string) error {
	s := Norm(sym)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	if _, ok := l.syms[s]; !ok {
		return fmt.Errorf("%s is not on today's blacklist", s)
	}
	delete(l.syms, s)
	err := l.save()
	l.notify()
	return err
}

// save persists the list. Caller holds mu. A failed save keeps the in-memory
// list (it holds until a restart) and is returned to the caller.
func (l *List) save() error {
	if l.cfg.SetMeta == nil {
		return nil
	}
	v := ""
	if len(l.syms) > 0 {
		syms := make([]string, 0, len(l.syms))
		for s := range l.syms {
			syms = append(syms, s)
		}
		sort.Strings(syms)
		b, err := json.Marshal(meta{Day: l.day, Syms: syms})
		if err != nil {
			return err
		}
		v = string(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.cfg.SetMeta(ctx, MetaKey, v); err != nil {
		return fmt.Errorf("today's blacklist could not be saved (it holds until a restart): %w", err)
	}
	return nil
}

func (l *List) load() {
	if l.cfg.GetMeta == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	v, err := l.cfg.GetMeta(ctx, MetaKey)
	cancel()
	if err != nil || v == "" {
		return
	}
	var m meta
	if json.Unmarshal([]byte(v), &m) != nil || m.Day != l.day {
		return
	}
	for _, s := range m.Syms {
		if s = Norm(s); validSym.MatchString(s) {
			if l.syms == nil {
				l.syms = map[string]struct{}{}
			}
			l.syms[s] = struct{}{}
		}
	}
}
