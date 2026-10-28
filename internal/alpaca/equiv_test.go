package alpaca_test

// Equivalence: the same synthetic market, delivered once as Massive per-second
// "A" aggregates (through internal/feed's real client) and once as Alpaca SIP
// trades (through internal/alpaca's real client, which builds the per-second
// aggregates itself), must produce identical core.Agg streams and identical
// engine rows.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eelkonkoopman/marketscanner/internal/alpaca"
	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/engine"
	"github.com/eelkonkoopman/marketscanner/internal/feed"
)

type synTrade struct {
	sym   string
	ms    int64
	px    float64
	sz    int64
	conds []string
}

var et = func() *time.Location { l, _ := time.LoadLocation("America/New_York"); return l }()

// market builds ~3 minutes of pre-market trading for three symbols, plus a
// few seconds after the 09:30 open for one of them. Every second that has
// trades has at least one price-eligible print; odd lots are mixed in.
func market() []synTrade {
	start := time.Date(2026, 10, 27, 8, 0, 0, 0, et).UnixMilli()
	var out []synTrade
	seed := uint32(7)
	rnd := func(n uint32) uint32 { seed = seed*1664525 + 1013904223; return (seed >> 8) % n }
	for _, s := range []struct {
		sym  string
		base float64
	}{{"AAA", 2.00}, {"BBB", 15.00}, {"CCC", 0.50}} {
		px := s.base
		for sec := int64(0); sec < 180; sec++ {
			if rnd(3) == 0 {
				continue // quiet second
			}
			n := 1 + int(rnd(4))
			for i := 0; i < n; i++ {
				px += (float64(rnd(21)) - 10) * s.base / 2000
				px = float64(int64(px*10000+0.5)) / 10000
				conds := []string{"@", "T"}
				sz := int64(100 * (1 + rnd(10)))
				if i > 0 && rnd(3) == 0 {
					conds = []string{"@", "T", "I"}
					sz = int64(1 + rnd(99))
				}
				out = append(out, synTrade{s.sym, start + sec*1000 + int64(i*100+int(rnd(90))), px, sz, conds})
			}
		}
	}
	open := time.Date(2026, 10, 27, 9, 30, 0, 0, et).UnixMilli()
	for i, px := range []float64{2.10, 2.12, 2.08, 2.15} {
		out = append(out, synTrade{"AAA", open + int64(i)*700, px, 500, []string{"@"}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ms < out[j].ms })
	return out
}

func priceEligible(conds []string) bool {
	for _, c := range conds {
		if c == "I" {
			return false
		}
	}
	return true
}

// massiveAggs is the reference per-second aggregation, written independently
// of internal/alpaca: what Massive's A.* channel sends for these trades.
func massiveAggs(trades []synTrade) []map[string]any {
	type key struct {
		sym string
		sec int64
	}
	type day struct {
		vol      int64
		num, den float64
		op       float64
	}
	open := time.Date(2026, 10, 27, 9, 30, 0, 0, et).UnixMilli()
	days := map[string]*day{}
	var order []key
	groups := map[key][]synTrade{}
	for _, t := range trades {
		k := key{t.sym, t.ms / 1000}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], t)
	}
	var out []map[string]any
	for _, k := range order {
		d := days[k.sym]
		if d == nil {
			d = &day{}
			days[k.sym] = d
		}
		var o, h, l, c, num, den float64
		var vol int64
		first := true
		for _, t := range groups[k] {
			vol += t.sz
			d.vol += t.sz
			if !priceEligible(t.conds) {
				continue
			}
			d.num += t.px * float64(t.sz)
			d.den += float64(t.sz)
			num += t.px * float64(t.sz)
			den += float64(t.sz)
			if first {
				o, h, l, first = t.px, t.px, t.px, false
			}
			h, l, c = max(h, t.px), min(l, t.px), t.px
			if d.op == 0 && t.ms >= open {
				d.op = t.px
			}
		}
		out = append(out, map[string]any{
			"ev": "A", "sym": k.sym, "v": vol, "av": d.vol, "op": d.op, "vw": num / den,
			"o": o, "c": c, "h": h, "l": l, "a": d.num / d.den, "z": 0,
			"s": k.sec * 1000, "e": k.sec*1000 + 1000,
		})
	}
	return out
}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

func serveMassive(t *testing.T, frames [][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		send := func(b []byte) error { return conn.Write(ctx, websocket.MessageText, b) }
		_ = send([]byte(`[{"ev":"status","status":"connected"}]`))
		_, _, _ = conn.Read(ctx)
		_ = send([]byte(`[{"ev":"status","status":"auth_success"}]`))
		_, _, _ = conn.Read(ctx)
		_ = send([]byte(`[{"ev":"status","status":"success","message":"subscribed to: A.*"}]`))
		for _, f := range frames {
			if send(f) != nil {
				return
			}
		}
		<-ctx.Done()
	}))
}

func serveAlpaca(t *testing.T, frames [][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		send := func(b []byte) error { return conn.Write(ctx, websocket.MessageText, b) }
		_ = send([]byte(`[{"T":"success","msg":"connected"}]`))
		_, _, _ = conn.Read(ctx)
		_ = send([]byte(`[{"T":"success","msg":"authenticated"}]`))
		_, _, _ = conn.Read(ctx)
		_ = send([]byte(`[{"T":"subscription","trades":["*"]}]`))
		for _, f := range frames {
			if send(f) != nil {
				return
			}
		}
		<-ctx.Done()
	}))
}

func collect(t *testing.T, ch <-chan core.Agg, n int) []core.Agg {
	t.Helper()
	out := make([]core.Agg, 0, n)
	timeout := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case a := <-ch:
			out = append(out, a)
		case <-timeout:
			t.Fatalf("got %d of %d aggregates", len(out), n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sym != out[j].Sym {
			return out[i].Sym < out[j].Sym
		}
		return out[i].EndMs < out[j].EndMs
	})
	return out
}

func TestMassiveAndAlpacaPathsAreEquivalent(t *testing.T) {
	trades := market()
	ref := massiveAggs(trades)

	// Massive: frames of 20 aggregates.
	var mFrames [][]byte
	for i := 0; i < len(ref); i += 20 {
		b, _ := json.Marshal(ref[i:min(i+20, len(ref))])
		mFrames = append(mFrames, b)
	}
	// Alpaca: batched frames of 37 trades.
	var aFrames [][]byte
	for i := 0; i < len(trades); i += 37 {
		var batch []map[string]any
		for _, tr := range trades[i:min(i+37, len(trades))] {
			batch = append(batch, map[string]any{"T": "t", "S": tr.sym, "i": i, "x": "V", "p": tr.px, "s": tr.sz,
				"c": tr.conds, "z": "C", "t": time.UnixMilli(tr.ms).UTC().Format(time.RFC3339Nano)})
		}
		b, _ := json.Marshal(batch)
		aFrames = append(aFrames, b)
	}

	ms := serveMassive(t, mFrames)
	defer ms.Close()
	as := serveAlpaca(t, aFrames)
	defer as.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mc := feed.New(feed.Config{APIKey: "k", WSURL: wsURL(ms)})
	ac := alpaca.New(alpaca.Config{
		Keys: alpaca.Keys{KeyID: "id", Secret: "sec"}, WSURL: wsURL(as), StaleAfter: -1,
		Now: func() time.Time { return time.Date(2026, 10, 27, 9, 35, 0, 0, et) },
	})
	mOut := make(chan core.Agg, 4096)
	aOut := make(chan core.Agg, 4096)
	go func() { _ = mc.Stream(ctx, mOut, nil) }()
	go func() { _ = ac.Stream(ctx, aOut, nil) }()

	mAggs := collect(t, mOut, len(ref))
	aAggs := collect(t, aOut, len(ref))
	for i := range mAggs {
		if fmt.Sprintf("%+v", mAggs[i]) != fmt.Sprintf("%+v", aAggs[i]) {
			t.Fatalf("agg %d differs:\nmassive %+v\nalpaca  %+v", i, mAggs[i], aAggs[i])
		}
	}
	select {
	case extra := <-aOut:
		t.Fatalf("alpaca emitted an extra aggregate %+v", extra)
	case <-time.After(600 * time.Millisecond):
	}

	// Same aggregates through two engines -> same rows.
	now := time.Date(2026, 10, 27, 9, 31, 0, 0, et)
	rows := func(aggs []core.Agg) map[string]string {
		eng := engine.New(engine.Options{TickHz: 50, Now: func() time.Time { return now }})
		ectx, ecancel := context.WithCancel(ctx)
		defer ecancel()
		go func() { _ = eng.Run(ectx) }()
		for _, a := range aggs {
			eng.Apply(a)
		}
		want := map[string]core.Agg{}
		var vol = map[string]int64{}
		for _, a := range aggs {
			want[a.Sym] = a
			vol[a.Sym] += a.Vol
		}
		deadline := time.After(10 * time.Second)
		for {
			select {
			case snap := <-eng.Snapshots():
				ok := true
				out := map[string]string{}
				for sym, last := range want {
					i, found := snap.Index[sym]
					if !found || snap.Rows[i].Vol != vol[sym] || float32(snap.Rows[i].Last) != float32(last.Close) {
						ok = false
						break
					}
					out[sym] = fmt.Sprintf("%+v", snap.Rows[i])
				}
				if ok {
					return out
				}
			case <-deadline:
				t.Fatal("engine never caught up")
			}
		}
	}
	mRows, aRows := rows(mAggs), rows(aAggs)
	for sym, r := range mRows {
		if aRows[sym] != r {
			t.Errorf("%s row differs:\nmassive %s\nalpaca  %s", sym, r, aRows[sym])
		}
	}
}
