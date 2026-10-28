package server

// Benchmarks for the hub's per-frame work: encoding (mustFrame: payload
// json.Marshal, then the envelope json.Marshal around the RawMessage) and the
// per-tick fanout (anyWants + mustFrame + fanout to the viewers). Baselines and
// the commands live in the perf-pass notes; the fixtures are shared with the
// golden test (golden_wire_test.go), so a benchmark measures exactly the frames
// the golden pins.
//
//	go test ./internal/server -run XXX -bench . -benchmem -count 6
//
// encodeFrame is the only place the encode benchmarks name the hub's encoder: if
// the encoding entry point is renamed or split (say a dedicated tick encoder),
// retarget it here and the golden test keeps guarding the bytes.

import (
	"context"
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func encodeFrame(typ string, payload any) []byte { return mustFrame(typ, payload) }

// benchChartCols is a scan that shows the inline chart: each row carries 24 bars.
var benchChartCols = append([]string{"chart"}, wireCols8...)

func BenchmarkEncodeScanTick(b *testing.B) {
	cases := []struct {
		name string
		tick core.ScanTick
	}{
		// A changed-only tick of a live scan: limit 50, every row changed, the
		// scan's columns plus the sidebox extras (8 fields per row).
		{"rows50x8", wireTick("gap", 1, false, 50, 50, wireCols8, 101)},
		// A delta: 50 ordered symbols, 7 rows with a new body.
		{"rows7of50x8", wireTick("gap", 2, false, 50, 7, wireCols8, 102)},
		// A scan with the chart column: 24 bars (~1.5 KB of JSON) per row.
		{"rows50x8_chart", wireTick("gap", 3, false, 50, 50, benchChartCols, 103)},
		// A full resync body.
		{"full120x8", wireTick("gap", 4, true, 120, 120, wireCols8, 104)},
		// Membership/order changed, no row body: what a re-ranking costs.
		{"order50_0rows", core.ScanTick{ID: "gap", Seq: 5, AsOf: 1_791_039_600_000,
			Order: wireTick("gap", 5, false, 50, 1, wireCols8, 105).Order, Rows: map[string]core.RowJSON{}}},
		{"order500_0rows", core.ScanTick{ID: "gap", Seq: 6, AsOf: 1_791_039_600_000,
			Order: wireTick("gap", 6, false, 500, 1, wireCols8, 106).Order, Rows: map[string]core.RowJSON{}}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			out := encodeFrame(core.TScanTick, c.tick)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out = encodeFrame(core.TScanTick, c.tick)
			}
			_ = out
		})
	}
}

func BenchmarkEncodeAlert(b *testing.B) {
	a := core.AlertMsg{ScanID: "gap", Sym: "ABCD", Kind: "enter", At: 1_791_039_601_000, Sound: "ping", Row: wireAlertRow(31)}
	out := encodeFrame(core.TAlert, a)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = encodeFrame(core.TAlert, a)
	}
	_ = out
}

func BenchmarkEncodeStatus(b *testing.B) {
	m := core.StatusMsg{Session: "regular", Feed: "live", Tracked: 10983, MsgRate: 4210, Baseline: "ready", HasKey: true,
		TwsLink: "linked", ClockMs: 1_791_039_600_000,
		Sources: map[string]core.SourceHealth{
			"massive": {State: "ok", Since: 1_791_000_000_000, LastOK: 1_791_039_599_000},
			"rtpr":    {State: "ok", Since: 1_791_000_000_000, LastOK: 1_791_039_599_000},
			"edgar":   {State: "off"},
		}}
	m.Counts = map[string]int{}
	for i := 0; i < 32; i++ { // one count per scan, like the live 32
		m.Counts[wireSym(i, 7)] = i * 3
	}
	out := encodeFrame(core.TStatus, m)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = encodeFrame(core.TStatus, m)
	}
	_ = out
}

func BenchmarkEncodeTapeStat(b *testing.B) {
	st := wireTapeStat(41)
	out := encodeFrame(core.TTapeStat, st)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out = encodeFrame(core.TTapeStat, st)
	}
	_ = out
}

// newBenchClient is a client with no socket: fanout only touches its view and
// send queue. A drain goroutine plays the write loop (until done is closed) so
// the queue never fills and every push is the accepted fast path.
func newBenchClient(done <-chan struct{}, view ...string) *client {
	c := &client{send: make(chan []byte, 1024), view: map[string]bool{}}
	for _, v := range view {
		c.view[v] = true
	}
	go func() {
		for {
			select {
			case <-c.send:
			case <-done:
				return
			}
		}
	}()
	return c
}

// BenchmarkHubFanout drives the real broadcast loop: each op is one scan tick
// taken from the scanner's tick channel and handled exactly as in production
// (anyWants, encode once, fanout to the viewers). The channel is unbuffered, so
// the loop's per-tick cost is the throughput of the sends. 6 windows are
// connected; the viewers of the scan are named in each case, the others view a
// different scan (an empty view would mean "wants everything").
func BenchmarkHubFanout(b *testing.B) {
	cases := []struct {
		name    string
		viewers int
		tick    core.ScanTick
	}{
		{"3of6_rows50x8", 3, wireTick("gap", 1, false, 50, 50, wireCols8, 201)},
		{"3of6_rows7of50x8", 3, wireTick("gap", 2, false, 50, 7, wireCols8, 202)},
		{"3of6_order50_0rows", 3, core.ScanTick{ID: "gap", Seq: 3, AsOf: 1_791_039_600_000,
			Order: wireTick("gap", 3, false, 50, 1, wireCols8, 203).Order, Rows: map[string]core.RowJSON{}}},
		{"6of6_rows50x8", 6, wireTick("gap", 4, false, 50, 50, wireCols8, 204)},
		// Nobody views the scan: the encode is skipped, only anyWants runs.
		{"0of6_rows50x8_skipped", 0, wireTick("gap", 5, false, 50, 50, wireCols8, 205)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			fs := newFake()
			fs.ticks = make(chan core.ScanTick)
			entries := make(chan core.ScanEntry)
			srv := New(Config{Scanner: fs, Entries: entries})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			defer func() { cancel(); close(done) }()
			srv.Start(ctx)
			for i := 0; i < 6; i++ {
				view := "other"
				if i < c.viewers {
					view = "gap"
				}
				srv.addClient(newBenchClient(done, view))
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fs.ticks <- c.tick
			}
			entries <- core.ScanEntry{ScanID: fenceID} // the loop has finished the last tick
			b.StopTimer()
		})
	}
}
