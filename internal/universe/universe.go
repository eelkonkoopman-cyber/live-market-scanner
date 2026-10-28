// Package universe maintains the set of symbols the scanner tracks: "regular
// securities" (common stock, ADRs, ETFs), filtered from the ticker reference.
package universe

import (
	"context"
	"sync"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// allowed security types for the regular-securities universe.
var allowed = map[string]bool{"CS": true, "ADRC": true, "ETF": true}

// Universe is a concurrency-safe symbol set. In "all" mode it admits everything
// (a fallback when the reference fetch fails, so the app still shows data).
type Universe struct {
	mu    sync.RWMutex
	set   map[string]struct{}
	types map[string]string // symbol -> security type (CS/ADRC/ETF), for Types()
	all   bool
}

// NewAll returns a permissive universe that admits every symbol.
func NewAll() *Universe { return &Universe{all: true} }

func (u *Universe) Contains(sym string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.all {
		return true
	}
	_, ok := u.set[sym]
	return ok
}

func (u *Universe) Len() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return len(u.set)
}

// Set returns a copy of the current symbol set (empty in "all" mode). Used to
// prune the engine to the universe after a refresh.
func (u *Universe) Set() map[string]struct{} {
	u.mu.RLock()
	defer u.mu.RUnlock()
	m := make(map[string]struct{}, len(u.set))
	for k := range u.set {
		m[k] = struct{}{}
	}
	return m
}

// Types returns a copy of the current symbol -> security-type map (empty in
// "all" mode, same as Set()). Used to feed Engine.SetSecTypes after a refresh.
func (u *Universe) Types() map[string]string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	m := make(map[string]string, len(u.types))
	for k, v := range u.types {
		m[k] = v
	}
	return m
}

// Replace swaps in a new symbol set + its security types (leaving "all" mode).
func (u *Universe) Replace(syms []string, types map[string]string) {
	m := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		m[s] = struct{}{}
	}
	u.mu.Lock()
	u.set, u.types, u.all = m, types, false
	u.mu.Unlock()
}

// Reference is the minimal dependency Load needs (satisfied by *feed.Client).
type Reference interface {
	Reference(ctx context.Context) ([]core.Ticker, error)
}

// Load fetches the ticker reference and builds the regular-securities universe.
func Load(ctx context.Context, r Reference) (*Universe, error) {
	u := &Universe{}
	if err := Refresh(ctx, r, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Refresh re-fetches the reference and swaps the universe's symbol set in place.
// Use this to (re)populate an existing Universe (e.g. a NewAll one) without
// blocking startup.
func Refresh(ctx context.Context, r Reference, u *Universe) error {
	tks, err := r.Reference(ctx)
	if err != nil {
		return err
	}
	syms := make([]string, 0, len(tks))
	types := make(map[string]string, len(tks))
	for _, t := range tks {
		if t.Active && allowed[t.Type] {
			syms = append(syms, t.Symbol)
			types[t.Symbol] = t.Type
		}
	}
	u.Replace(syms, types)
	return nil
}
