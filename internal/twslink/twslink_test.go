package twslink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// frame builds a wire message the way TWS would send it.
func frame(fields ...string) []byte {
	payload := []byte(strings.Join(fields, "\x00") + "\x00")
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload)))
	return append(hdr, payload...)
}

func TestReadMsgSplitsFields(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader(frame("79", "9100", "1")))
	got, err := readMsg(r)
	if err != nil {
		t.Fatalf("readMsg: %v", err)
	}
	if len(got) != 3 || got[0] != "79" || got[2] != "1" {
		t.Fatalf("fields: %#v", got)
	}
}

// nopConn is a net.Conn that only records writes, so tests can assert the
// exact bytes we put on the wire.
type nopConn struct{ w *bytes.Buffer }

func (c *nopConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *nopConn) Write(p []byte) (int, error)      { return c.w.Write(p) }
func (c *nopConn) Close() error                     { return nil }
func (c *nopConn) LocalAddr() net.Addr              { return nil }
func (c *nopConn) RemoteAddr() net.Addr             { return nil }
func (c *nopConn) SetDeadline(time.Time) error      { return nil }
func (c *nopConn) SetReadDeadline(time.Time) error  { return nil }
func (c *nopConn) SetWriteDeadline(time.Time) error { return nil }

func TestSendEncodesLengthPrefixedNullTerminatedFields(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true})
	c.conn = &nopConn{w: &buf}
	if err := c.send(outUpdateDisplayGrp, 1, subReqID, "8314@SMART"); err != nil {
		t.Fatalf("send: %v", err)
	}
	b := buf.Bytes()
	if len(b) < 4 {
		t.Fatal("no output")
	}
	n := binary.BigEndian.Uint32(b[:4])
	payload := b[4:]
	if int(n) != len(payload) {
		t.Fatalf("length prefix %d != payload %d", n, len(payload))
	}
	want := "69\x001\x009001\x008314@SMART\x00"
	if string(payload) != want {
		t.Fatalf("payload = %q want %q", payload, want)
	}
}

// A cached conId must go straight out as conid@exchange with no lookup.
func TestRequestUsesCachedConIDAndFormatsContractInfo(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, Group: 3, Exchange: "SMART"})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true
	c.conIDs["IBM"] = 8314

	c.request("IBM")
	if !strings.Contains(buf.String(), "8314@SMART") {
		t.Fatalf("expected cached conid push, got %q", buf.String())
	}
	if strings.Contains(buf.String(), strconv.Itoa(outReqMatchingSyms)+"\x00") {
		t.Fatal("cached symbol should not trigger a lookup")
	}
}

// An uncached symbol issues reqMatchingSymbols, and the SYMBOL_SAMPLES reply
// must select the exact US stock match (not a fuzzy relative) and then push it.
func TestSymbolSamplesPicksExactUSStockThenPushes(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true
	c.want = "AA"

	c.request("AA")
	if !strings.Contains(buf.String(), "81\x00") {
		t.Fatalf("expected a matching-symbols lookup, got %q", buf.String())
	}
	reqID := c.nextReqID - 1
	buf.Reset()

	// Two candidates: a fuzzy non-US/non-STK first, then the exact match.
	// Layout per item: conId, symbol, secType, primaryExch, currency, nDeriv, [derivs...], desc, issuer
	f := []string{
		"79", strconv.FormatInt(reqID, 10), "2",
		"111", "AAPL", "STK", "NASDAQ", "USD", "1", "OPT", "desc", "iss",
		"222", "AA", "STK", "NYSE", "USD", "0", "desc", "iss",
	}
	c.onSymbolSamples(f)

	if got := c.conIDs["AA"]; got != 222 {
		t.Fatalf("resolved conId = %d, want 222 (exact ticker match)", got)
	}
	if !strings.Contains(buf.String(), "222@SMART") {
		t.Fatalf("expected push of resolved contract, got %q", buf.String())
	}
	if len(c.pending) != 0 {
		t.Fatalf("pending lookup not cleared: %v", c.pending)
	}
}

// A non-USD / non-STK-only result set must not resolve to anything.
func TestSymbolSamplesRejectsNonUSStock(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true
	c.want = "XYZ"
	c.request("XYZ")
	reqID := c.nextReqID - 1
	buf.Reset()

	c.onSymbolSamples([]string{
		"79", strconv.FormatInt(reqID, 10), "1",
		"333", "XYZ", "STK", "LSE", "GBP", "0", "desc", "iss",
	})
	if _, ok := c.conIDs["XYZ"]; ok {
		t.Fatal("non-USD listing should not resolve")
	}
	if buf.Len() != 0 {
		t.Fatalf("nothing should be pushed, got %q", buf.String())
	}
}

// Generic tick 236 delivers the indicator via tickGeneric (type 46) and the
// exact count via tickSize (type 89).
func TestShortabilityTicks(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveShort: true})
	c.conn = &nopConn{w: &buf}
	c.want = "GME"
	c.short = core.ShortLive{Sym: "GME", Shares: -1}

	// tickGeneric: [45, version, reqId, tickType, value]
	c.onTick([]string{"45", "6", strconv.Itoa(mktReqID), "46", "3.0"}, 4, false)
	got := <-c.shortOut
	if got.Indicator != 3.0 || got.Sym != "GME" {
		t.Fatalf("indicator tick: %+v", got)
	}
	if got.Shares != -1 {
		t.Fatalf("shares should stay unset until tick 89: %d", got.Shares)
	}

	// tickSize: [2, version, reqId, sizeTickType, size]
	c.onTick([]string{"2", "6", strconv.Itoa(mktReqID), "89", "4000"}, 4, true)
	got = <-c.shortOut
	if got.Shares != 4000 || got.Indicator != 3.0 {
		t.Fatalf("shares tick lost prior indicator: %+v", got)
	}
}

func TestShortabilityIgnoresForeignReqAndUnsetSize(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveShort: true})
	c.conn = &nopConn{w: &buf}
	c.want = "GME"
	c.short = core.ShortLive{Sym: "GME", Shares: -1}

	// A tick for someone else's request id must be ignored entirely.
	c.onTick([]string{"45", "6", "12345", "46", "3.0"}, 4, false)
	// An unrelated tick type on our request must be ignored too.
	c.onTick([]string{"45", "6", strconv.Itoa(mktReqID), "4", "12.5"}, 4, false)
	// The max-int sentinel means "not reported", not a real share count.
	c.onTick([]string{"2", "6", strconv.Itoa(mktReqID), "89", "2147483647"}, 4, true)

	select {
	case got := <-c.shortOut:
		t.Fatalf("nothing should have been published, got %+v", got)
	default:
	}
	if c.short.Shares != -1 || c.short.Indicator != 0 {
		t.Fatalf("state mutated: %+v", c.short)
	}
}

// Switching symbols must cancel the previous line before opening a new one, so
// the client never holds more than one of IBKR's market-data lines.
func TestWatchShortHoldsExactlyOneLine(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveShort: true})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true

	c.want = "AAA"
	c.watchShort(111)
	first := buf.String()
	if strings.Contains(first, "\x002\x002\x00") { // cancel before any line is open
		t.Fatal("should not cancel when no subscription is open")
	}
	if !strings.Contains(first, genericTickShortable) {
		t.Fatalf("expected generic tick 236 request: %q", first)
	}

	buf.Reset()
	c.want = "BBB"
	c.watchShort(222)
	second := buf.String()
	cancelPrefix := strconv.Itoa(outCancelMktData) + "\x002\x00" + strconv.Itoa(mktReqID)
	if !strings.HasPrefix(second[4:], cancelPrefix) {
		t.Fatalf("second watch must cancel first: %q", second)
	}
	if !strings.Contains(second, "222") {
		t.Fatalf("expected new conId in re-request: %q", second)
	}
}

func TestLiveShortDisabledSendsNothing(t *testing.T) {
	var buf bytes.Buffer
	c := New(Config{Enabled: true, LiveShort: false})
	c.conn = &nopConn{w: &buf}
	c.subscribed = true
	c.watchShort(111)
	if buf.Len() != 0 {
		t.Fatalf("LiveShort off must not request market data: %q", buf.String())
	}
}

// Regression: the handshake and the read loop must share ONE buffered reader.
// A second reader over the same socket discards whatever the first buffered
// past the greeting, so the stream resyncs mid-message and the session dies
// immediately after every successful connect. This fake TWS writes the
// greeting reply and the post-startApi burst in a SINGLE Write, which is
// exactly the packing that triggered it against the real TWS.
func TestSessionSurvivesBatchedHandshakeBurst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	frame := func(fields ...string) []byte {
		payload := []byte(strings.Join(fields, "\x00") + "\x00")
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(payload)))
		return append(h, payload...)
	}

	served := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the "API\0" greeting + version block, then reply with the
		// server version AND the startApi burst glued together.
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
		var out []byte
		out = append(out, frame("176", "20260727 12:00:00 GMT")...)
		out = append(out, frame("15", "1", "DU123456")...) // managed accounts
		out = append(out, frame("9", "1", "1")...)         // next valid id
		out = append(out, frame("4", "2", "-1", "2104", "Market data farm connection is OK:usfarm")...)
		_, _ = conn.Write(out) // ONE write — the packing that broke it
		close(served)
		// Hold the socket open; the session must stay alive on its own.
		io.Copy(io.Discard, conn)
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	c := New(Config{Enabled: true, Addr: ln.Addr().String()})
	r := bufio.NewReaderSize(conn, 1<<16)

	ver, err := c.handshake(conn, r)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if ver != 176 {
		t.Fatalf("serverVersion = %d", ver)
	}
	<-served
	// handshake() clears the read deadline, so re-arm it: with the bug the
	// frames below are gone and these reads would otherwise block forever
	// instead of failing.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// The three frames that shared the greeting's TCP write must still be
	// readable. If handshake had used its own reader, they were consumed into
	// that reader's buffer and thrown away — these reads would then block until
	// the deadline (or resync onto garbage).
	want := [][]string{
		{"15", "1", "DU123456"},
		{"9", "1", "1"},
		{"4", "2", "-1", "2104", "Market data farm connection is OK:usfarm"},
	}
	for i, w := range want {
		got, err := readMsg(r)
		if err != nil {
			t.Fatalf("frame %d lost across the handshake (reader not shared): %v", i, err)
		}
		if strings.Join(got, "|") != strings.Join(w, "|") {
			t.Fatalf("frame %d = %v, want %v", i, got, w)
		}
	}
}

// Regression: TWS closes the socket — silently, with no error frame — if any
// request arrives before the startApi handshake finishes. This fake TWS
// enforces that rule: it drops the connection if it sees a request before it
// has sent nextValidId, exactly as the real one does.
func TestNoRequestsBeforeNextValidID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	frame := func(fields ...string) []byte {
		payload := []byte(strings.Join(fields, "\x00") + "\x00")
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(payload)))
		return append(h, payload...)
	}

	violation := make(chan string, 1)
	subscribed := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReaderSize(conn, 1<<16)
		// Frame-exact: "API\0" is a bare 4 bytes, then a length-prefixed
		// version block. A loose Read here would straddle into startApi.
		if _, err := io.ReadFull(r, make([]byte, 4)); err != nil {
			return
		}
		if _, err := readMsg(r); err != nil { // v100..176
			return
		}
		_, _ = conn.Write(frame("176", "20260727 12:00:00 GMT"))

		if _, err := readMsg(r); err != nil { // startApi
			return
		}
		// One reader goroutine only — the next frame the client sends,
		// whenever it sends it. Racing two readMsg calls on one reader would
		// let this steal the frame the assertion below is waiting for.
		next := make(chan []string, 1)
		go func() {
			if f, err := readMsg(r); err == nil {
				next <- f
			}
			io.Copy(io.Discard, r)
		}()

		// Anything arriving in this window is a pre-handshake request: the bug.
		select {
		case f := <-next:
			violation <- strings.Join(f, "|")
			return // the real TWS just closes the socket
		case <-time.After(400 * time.Millisecond):
		}

		// Handshake completes: notices, then nextValidId.
		_, _ = conn.Write(frame("15", "1", "DU1"))
		_, _ = conn.Write(frame("4", "2", "-1", "2104", "Market data farm connection is OK:usfarm"))
		_, _ = conn.Write(frame("9", "1", "1")) // nextValidId — requests are legal now
		select {
		case f := <-next:
			if len(f) > 0 && f[0] == strconv.Itoa(outSubscribeGroup) {
				subscribed <- struct{}{}
			}
		case <-time.After(3 * time.Second):
		}
	}()

	c := New(Config{Enabled: true, Addr: ln.Addr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = c.session(ctx) }()

	select {
	case f := <-violation:
		t.Fatalf("sent a request before nextValidId (TWS would drop us): %s", f)
	case <-subscribed:
		// correct: waited for the handshake, then subscribed
	case <-time.After(4 * time.Second):
		t.Fatal("never subscribed after the handshake completed")
	}
}

// Show must never block, even with no connection and a full buffer.
func TestShowNeverBlocks(t *testing.T) {
	c := New(Config{Enabled: true})
	for i := 0; i < 100; i++ {
		c.Show("ABCD") // buffer is 1; must not deadlock
	}
}

// TestShowKeepsOnlyLatest proves Show is latest-wins: when nothing is
// draining c.show (e.g. TWS is disconnected/reconnecting so the session loop
// isn't running), a burst of clicks must leave only the newest symbol queued,
// not the oldest — replaying a stale symbol once the link comes back is
// exactly the bug this guards against.
func TestShowKeepsOnlyLatest(t *testing.T) {
	c := New(Config{Enabled: true})
	c.Show("A")
	c.Show("B")
	c.Show("C")

	select {
	case sym := <-c.show:
		if sym != "C" {
			t.Fatalf("want latest-wins symbol %q, got %q", "C", sym)
		}
	default:
		t.Fatal("expected a queued symbol, channel was empty")
	}

	select {
	case sym := <-c.show:
		t.Fatalf("channel should be empty after draining the one pending symbol, got %q", sym)
	default:
	}
}

func TestDisabledClientIgnoresShow(t *testing.T) {
	c := New(Config{Enabled: false})
	c.Show("ABCD")
	if len(c.show) != 0 {
		t.Fatal("disabled client should not queue symbols")
	}
}

func TestDefaultsApplied(t *testing.T) {
	c := New(Config{Enabled: true})
	if c.cfg.Addr != "127.0.0.1:7496" || c.cfg.ClientID != 77 || c.cfg.Group != 1 || c.cfg.Exchange != "SMART" {
		t.Fatalf("defaults: %+v", c.cfg)
	}
	// group must stay inside TWS's documented 1..7 range
	if g := New(Config{Enabled: true, Group: 99}).cfg.Group; g != 1 {
		t.Fatalf("out-of-range group should fall back to 1, got %d", g)
	}
}
