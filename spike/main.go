// Phase 0 spike for the Live Market Scanner.
//
// Goal: de-risk the single load-bearing assumption — that Massive (massive.com,
// Polygon.io-API-compatible) supports a WILDCARD subscription to per-second
// aggregates ("A.*") for the whole US market over ONE WebSocket — and gather a
// first reading on throughput AND the `av` (accumulated daily volume) anchor
// that RVOL correctness depends on.
//
// Handshake (order matters — this is the usual failure point):
//  1. Dial wss://socket.massive.com/stocks   (legacy wss://socket.polygon.io/stocks also works)
//  2. <- [{"ev":"status","status":"connected","message":"Connected Successfully"}]
//  3. -> {"action":"auth","params":"<API_KEY>"}
//  4. <- [{"ev":"status","status":"auth_success","message":"authenticated"}]   (or "auth_failed")
//  5. -> {"action":"subscribe","params":"A.*"}
//  6. <- [{"ev":"status","status":"success","message":"subscribed to: A.*"}]
//  7. <- data: [{"ev":"A","sym":"AAPL","v":..,"av":..,"op":..,"vw":..,"o":..,"c":..,"h":..,"l":..,"a":..,"z":..,"s":..,"e":..}, ...]
//
// A = per-second aggregates, AM = per-minute. Wildcard "*" and comma-lists supported.
//
// IF "A.*" IS REJECTED (some entitlements forbid wildcard / per-second):
//
//	rejection arrives as an "ev":"status" frame with status "error"/"max_subscriptions"
//	(or "auth_failed" at step 4). The spike detects this and stops with a clear verdict.
//	Fallback A: subscribe in batches ({"action":"subscribe","params":"A.AAPL,A.MSFT,..."})
//	  over the ~7-11k regular-securities universe in chunks; multiple sockets if needed.
//	Fallback B: poll REST GET /v2/snapshot/locale/us/markets/stocks/tickers every few sec.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultWSURL   = "wss://socket.massive.com/stocks" // legacy: wss://socket.polygon.io/stocks
	defaultRESTURL = "https://api.massive.com"         // legacy: https://api.polygon.io
	defaultDur     = 60 * time.Second
	subTopic       = "A.*"

	readLimit        = 32 << 20 // 32 MiB: whole-market frames get big at the open.
	dialTimeout      = 15 * time.Second
	handshakeTimeout = 20 * time.Second
	writeTimeout     = 5 * time.Second
)

// errFatal marks a config/entitlement failure that must NOT be retried (vs a
// transient network error which should reconnect). Classified structurally, not
// by substring-matching arbitrary error text.
var errFatal = errors.New("fatal handshake")

// ---- wire types ----

type controlMessage struct {
	Action string `json:"action"`
	Params string `json:"params"`
}

type envelope struct {
	Ev string `json:"ev"`
}

type statusMsg struct {
	Ev      string `json:"ev"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// aggMsg mirrors the Massive/Polygon "A" per-second aggregate, full schema.
// v/av/z are integers on the wire; o/c/h/l/op/vw/a are floats.
type aggMsg struct {
	Ev  string  `json:"ev"`  // "A"
	Sym string  `json:"sym"` // ticker
	V   int64   `json:"v"`   // tick volume in this window
	AV  int64   `json:"av"`  // accumulated daily volume (anchor under test)
	OP  float64 `json:"op"`  // official day open
	VW  float64 `json:"vw"`  // window VWAP
	O   float64 `json:"o"`
	C   float64 `json:"c"`
	H   float64 `json:"h"`
	L   float64 `json:"l"`
	A   float64 `json:"a"` // day VWAP
	Z   int64   `json:"z"` // average trade size
	S   int64   `json:"s"` // window start, unix ms
	E   int64   `json:"e"` // window end, unix ms
	OTC bool    `json:"otc"`
}

// ---- stats ----

type stats struct {
	mu sync.Mutex

	frames     int64
	statusMsgs int64
	aggs       int64
	distinct   map[string]struct{}

	curBucketTs int64
	curBucket   int64
	peakPerSec  int64
	secsSeen    int64
	sumPerSec   int64

	maxAV    int64
	maxAVSym string

	samples []aggMsg

	rejected   bool
	rejectInfo string
}

func newStats() *stats { return &stats{distinct: make(map[string]struct{}, 16384)} }

func (s *stats) incFrame()  { s.mu.Lock(); s.frames++; s.mu.Unlock() }
func (s *stats) incStatus() { s.mu.Lock(); s.statusMsgs++; s.mu.Unlock() }

func (s *stats) markRejected(status, msg string) {
	s.mu.Lock()
	s.rejected = true
	s.rejectInfo = fmt.Sprintf("status=%q message=%q", status, msg)
	s.mu.Unlock()
}

func (s *stats) record(a aggMsg, nowSec int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aggs++
	s.distinct[a.Sym] = struct{}{}
	if a.AV > s.maxAV {
		s.maxAV, s.maxAVSym = a.AV, a.Sym
	}
	if nowSec != s.curBucketTs {
		if s.curBucketTs != 0 {
			s.flushBucketLocked()
		}
		s.curBucketTs, s.curBucket = nowSec, 0
	}
	s.curBucket++
	if len(s.samples) < 8 {
		s.samples = append(s.samples, a)
	}
}

func (s *stats) flushBucketLocked() {
	if s.curBucket > s.peakPerSec {
		s.peakPerSec = s.curBucket
	}
	s.sumPerSec += s.curBucket
	s.secsSeen++
}

func (s *stats) finalize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.curBucketTs != 0 {
		s.flushBucketLocked()
		s.curBucketTs = 0
	}
}

// ---- connection ----

// runOnce performs one full handshake + read loop. Returns nil on context
// cancellation (clean stop / duration elapsed), errFatal-wrapped on a config
// problem, or a transient error to trigger reconnect.
func runOnce(ctx context.Context, wsURL, apiKey string, st *stats) error {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	conn.SetReadLimit(readLimit)

	readFrame := func(rctx context.Context) ([]json.RawMessage, error) {
		_, data, err := conn.Read(rctx)
		if err != nil {
			return nil, err
		}
		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) != nil {
			arr = []json.RawMessage{json.RawMessage(data)} // tolerate a bare object
		}
		return arr, nil
	}
	send := func(m controlMessage) error {
		b, _ := json.Marshal(m)
		wctx, wc := context.WithTimeout(ctx, writeTimeout)
		defer wc()
		return conn.Write(wctx, websocket.MessageText, b)
	}

	// Handshake (bounded).
	hsCtx, hsCancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hsCancel()
	if err := waitForStatus(hsCtx, readFrame, "connected"); err != nil {
		return fmt.Errorf("handshake(connected): %w", err)
	}
	fmt.Println("[handshake] connected")
	if err := send(controlMessage{Action: "auth", Params: apiKey}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}
	if err := waitForStatus(hsCtx, readFrame, "auth_success"); err != nil {
		return err // already errFatal-wrapped on auth_failed
	}
	fmt.Println("[handshake] auth_success")
	if err := send(controlMessage{Action: "subscribe", Params: subTopic}); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}
	fmt.Printf("[handshake] sent subscribe %q\n", subTopic)

	// Keepalive so a quiet market doesn't look like a dead connection.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 10*time.Second)
				_ = conn.Ping(pctx)
				pc()
			}
		}
	}()

	// Data loop on the parent ctx (no per-read deadline -> quiet markets are fine).
	for {
		if ctx.Err() != nil {
			return nil
		}
		arr, err := readFrame(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		st.incFrame()
		nowSec := time.Now().Unix()
		for _, raw := range arr {
			var env envelope
			if json.Unmarshal(raw, &env) != nil {
				continue
			}
			switch env.Ev {
			case "A":
				var a aggMsg
				if json.Unmarshal(raw, &a) == nil {
					st.record(a, nowSec)
				}
			case "status":
				var sm statusMsg
				_ = json.Unmarshal(raw, &sm)
				st.incStatus()
				switch sm.Status {
				case "success":
					fmt.Printf("[subscribe] OK: %s\n", sm.Message)
				case "error", "max_subscriptions", "auth_failed":
					fmt.Printf("[subscribe] REJECTED: status=%q message=%q\n", sm.Status, sm.Message)
					st.markRejected(sm.Status, sm.Message)
					return fmt.Errorf("%w: subscribe rejected (%s)", errFatal, sm.Message)
				default:
					fmt.Printf("[status] %q: %s\n", sm.Status, sm.Message)
				}
			}
		}
	}
}

// waitForStatus reads frames until status == want, failing hard (errFatal) on
// auth_failed / error so the spike reports the real reason instead of hanging.
func waitForStatus(ctx context.Context, readFrame func(context.Context) ([]json.RawMessage, error), want string) error {
	for {
		arr, err := readFrame(ctx)
		if err != nil {
			return err
		}
		for _, raw := range arr {
			var sm statusMsg
			if json.Unmarshal(raw, &sm) != nil || sm.Ev != "status" {
				continue
			}
			switch sm.Status {
			case want:
				return nil
			case "connected":
				// fine while waiting for auth_success; keep reading.
			case "auth_failed":
				return fmt.Errorf("%w: auth_failed: %s", errFatal, sm.Message)
			case "error", "max_subscriptions":
				return fmt.Errorf("%w: handshake error status=%q message=%q "+
					"(if this names A.* or per-second aggregates, your entitlement may forbid "+
					"the wildcard — see fallbacks at top of file)", errFatal, sm.Status, sm.Message)
			default:
				fmt.Printf("[handshake] interim status=%q message=%q\n", sm.Status, sm.Message)
			}
		}
	}
}

// ---- main ----

func main() {
	apiKey := os.Getenv("MASSIVE_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "FATAL: MASSIVE_API_KEY is not set. Export your real-time key and retry.")
		os.Exit(2)
	}
	wsURL := getenv("MASSIVE_WS_URL", defaultWSURL)
	restURL := getenv("MASSIVE_BASE_URL", defaultRESTURL)
	dur := defaultDur
	if v := os.Getenv("SPIKE_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			dur = d
		} else {
			fmt.Fprintf(os.Stderr, "WARN: bad SPIKE_DURATION %q, using %s\n", v, dur)
		}
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, dur)
	defer cancel()

	fmt.Printf("Massive WS spike  url=%s  topic=%s  duration=%s\n", wsURL, subTopic, dur)
	fmt.Println("Connecting... (Ctrl-C to stop early)")

	st := newStats()
	start := time.Now()

	backoff := time.Second
	for ctx.Err() == nil {
		err := runOnce(ctx, wsURL, apiKey, st)
		if err == nil {
			break // ctx ended -> clean stop
		}
		if errors.Is(err, errFatal) {
			fmt.Fprintf(os.Stderr, "\nFATAL (not retrying): %v\n", err)
			break
		}
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "connection error: %v -> reconnecting in %s\n", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}

	st.finalize()
	printSummary(st, time.Since(start))

	// Best-effort av-anchor cross-check via REST snapshot (de-risks RVOL).
	st.mu.Lock()
	topSym, wsAV := st.maxAVSym, st.maxAV
	st.mu.Unlock()
	if topSym != "" {
		checkAVAnchor(context.Background(), restURL, apiKey, topSym, wsAV)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func printSummary(st *stats, elapsed time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()

	avg := 0.0
	if st.secsSeen > 0 {
		avg = float64(st.sumPerSec) / float64(st.secsSeen)
	}
	wildcard := st.aggs > 0 && len(st.distinct) > 50

	fmt.Println("\n==================== MASSIVE WS SPIKE SUMMARY ====================")
	fmt.Printf("Elapsed:                 %s\n", elapsed.Round(time.Millisecond))
	if st.rejected {
		fmt.Printf("WILDCARD 'A.*':          REJECTED (%s)\n", st.rejectInfo)
	} else {
		fmt.Printf("WILDCARD 'A.*' WORKED:   %v\n", wildcard)
	}
	fmt.Printf("Raw WS frames:           %d\n", st.frames)
	fmt.Printf("Status messages:         %d\n", st.statusMsgs)
	fmt.Printf("Agg ('A') events:        %d\n", st.aggs)
	fmt.Printf("Distinct symbols:        %d\n", len(st.distinct))
	fmt.Printf("Avg events/sec (active): %.1f\n", avg)
	fmt.Printf("Peak events/sec:         %d\n", st.peakPerSec)
	if elapsed.Seconds() > 0 {
		fmt.Printf("Overall events/sec:      %.1f\n", float64(st.aggs)/elapsed.Seconds())
	}
	if st.maxAVSym != "" {
		fmt.Printf("Highest-volume symbol:   %s (av=%d)\n", st.maxAVSym, st.maxAV)
	}

	fmt.Println("\nSample agg events:")
	for i, a := range st.samples {
		fmt.Printf("  [%d] %-6s c=%.4f v=%d av=%d vw=%.4f win=%dms\n",
			i, a.Sym, a.C, a.V, a.AV, a.VW, a.E-a.S)
	}

	switch {
	case st.rejected:
		fmt.Println("\nVERDICT: 'A.*' REJECTED by your entitlement. Use batched-subscribe or REST-snapshot fallback.")
	case wildcard:
		fmt.Println("\nVERDICT: Whole-market per-second aggregates over ONE WebSocket CONFIRMED.")
	case st.aggs == 0:
		fmt.Println("\nVERDICT: No data. Check key/entitlement and market hours; re-run during RTH (09:30-16:00 ET).")
	default:
		fmt.Println("\nVERDICT: Data arrived but few symbols — market may be quiet; re-run during RTH to confirm fan-out.")
	}
	fmt.Println("=================================================================")
}

// ---- av-anchor cross-check (RVOL de-risk) ----

type snapResp struct {
	Status string `json:"status"`
	Ticker struct {
		Ticker string `json:"ticker"`
		Day    struct {
			V float64 `json:"v"`
		} `json:"day"`
		Min struct {
			AV float64 `json:"av"`
			V  float64 `json:"v"`
		} `json:"min"`
		PrevDay struct {
			V float64 `json:"v"`
		} `json:"prevDay"`
	} `json:"ticker"`
}

func checkAVAnchor(ctx context.Context, baseURL, apiKey, sym string, wsAV int64) {
	fmt.Println("\n---- av-anchor cross-check (RVOL de-risk) ----")
	url := fmt.Sprintf("%s/v2/snapshot/locale/us/markets/stocks/tickers/%s?apiKey=%s",
		strings.TrimRight(baseURL, "/"), sym, apiKey)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("  (skipped: REST snapshot request failed: %v)\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  (skipped: REST snapshot HTTP %d: %s)\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	var sr snapResp
	if err := json.Unmarshal(body, &sr); err != nil {
		fmt.Printf("  (skipped: parse error: %v)\n", err)
		return
	}
	fmt.Printf("  symbol:                 %s\n", sym)
	fmt.Printf("  WS  av (accum day vol): %d\n", wsAV)
	fmt.Printf("  REST min.av:            %.0f\n", sr.Ticker.Min.AV)
	fmt.Printf("  REST day.v:             %.0f\n", sr.Ticker.Day.V)
	fmt.Printf("  REST prevDay.v:         %.0f\n", sr.Ticker.PrevDay.V)
	fmt.Println("  interpretation: WS av should ~= REST min.av (same accumulator).")
	fmt.Println("  To pin the anchor (04:00 vs 09:30): run this spike right at 09:30:05 ET.")
	fmt.Println("    - av already in the millions one second after the open => av INCLUDES")
	fmt.Println("      pre-market, so the RVOL baseline (folded from 04:00) and live av share")
	fmt.Println("      one anchor. Trust av directly.")
	fmt.Println("    - av resets to near-zero at 09:30 => engine must sum per-bar v from 04:00")
	fmt.Println("      instead of trusting av (see persistence design).")
}
