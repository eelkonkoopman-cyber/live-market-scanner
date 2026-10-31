package newsfilter

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The API key sits in the dial URL and websocket.Dial errors quote the URL, so
// the scrubbing is exercised with real dial failures, not just unit-tested.

// keySpellings are the forms of the key that must never appear anywhere.
func keySpellings() []string {
	out := append([]string(nil), secretsFor(testToken)...)
	// The distinctive pieces catch any other encoding of the same key.
	return append(out, "-TOKEN", "sec ret", "sec+ret", "sec%20ret", url.QueryEscape(testToken), url.PathEscape(testToken))
}

// assertNoKey fails if the key shows up in the captured log or in any file the
// stream wrote.
func assertNoKey(t *testing.T, r *rig) {
	t.Helper()
	texts := map[string]string{"log": r.log.String()}
	_ = filepath.WalkDir(r.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			texts[filepath.Base(path)] = string(b)
		}
		return nil
	})
	for name, body := range texts {
		for _, needle := range keySpellings() {
			if strings.Contains(body, needle) {
				t.Errorf("the API key leaked into %s (matched %q):\n%s", name, needle, body)
			}
		}
	}
}

// freePort returns a loopback address nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// dialErrorQuotesKey proves the precondition: a raw dial failure DOES contain
// the escaped key, so a passing leak test means the scrubbing worked rather
// than that the library happened to be quiet.
func dialErrorQuotesKey(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, keyed(base, testToken), nil)
	if err == nil {
		t.Fatalf("dial of %s unexpectedly worked", base)
	}
	if !strings.Contains(err.Error(), url.QueryEscape(testToken)) {
		t.Fatalf("precondition: expected the raw dial error to quote the key, got %v", err)
	}
}

func TestKeyNeverLeaksOnConnectionRefused(t *testing.T) {
	base := "ws://" + freePort(t) + "/socket.io/?EIO=3&transport=websocket"
	dialErrorQuotesKey(t, base)

	r := newRig(t, []string{base}).run()
	waitFor(t, "two failed attempts", func() bool { return len(r.sl.durations()) >= 2 })
	r.stop()
	if log := r.log.String(); !strings.Contains(log, "apiKey=***") {
		t.Fatalf("expected the failure to be logged with the key masked:\n%s", log)
	}
	assertNoKey(t, r)
}

func TestKeyNeverLeaksOnTLSFailureAndFallback(t *testing.T) {
	tls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tls.Config.ErrorLog = log.New(io.Discard, "", 0) // the expected handshake failures are noise
	tls.StartTLS()
	t.Cleanup(tls.Close)
	wss := "wss" + strings.TrimPrefix(tls.URL, "https") + "/socket.io/?EIO=3&transport=websocket"
	dialErrorQuotesKey(t, wss)

	good := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		c.Close(websocket.StatusGoingAway, "bye")
	})
	r := newRig(t, []string{wss, good.url()}).run()
	waitEvent(t, r.dir, "dropped", 1)
	r.stop()

	// The untrusted wss endpoint failed, the plaintext one carried the session.
	ev := eventsOf(readDirStrict(t, r.dir), "connected")
	if len(ev) == 0 || ev[0]["url"] != "ws" {
		t.Fatalf("connected = %v", ev)
	}
	if !strings.Contains(r.log.String(), "plaintext") {
		t.Error("falling back to ws:// must warn")
	}
	assertNoKey(t, r)
}

func TestKeyNeverLeaksWhenTheServerEchoesIt(t *testing.T) {
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		if n == 1 {
			send(ctx, c, `44"rejected apiKey=`+testToken+` also `+url.QueryEscape(testToken)+`"`)
			serve(ctx, c, nil)
			return
		}
		send(ctx, c, evt(art("echo", "headline carrying "+testToken, map[string]any{
			"sourceUrl": "https://example.test/?k=" + url.QueryEscape(testToken),
			"symbols":   []string{testToken},
			"source":    map[string]any{"id": testToken, "name": "wire " + testToken},
		})))
		c.Close(websocket.StatusPolicyViolation, "bad key "+testToken)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 1)
	waitEvent(t, r.dir, "dropped", 2)
	r.stop()

	arts := articlesOf(readDirStrict(t, r.dir))
	if len(arts) != 1 || !strings.Contains(arts[0]["title"].(string), "***") {
		t.Fatalf("the recorded article should carry the masked marker, got %v", arts)
	}
	assertNoKey(t, r)
}

// The recorded fields are cut to a maximum length, and a cut that falls inside
// an echoed key leaves a prefix that no spelling in the scrub list matches. So
// the scrub has to run first. Each field here carries the key 16 bytes before
// its cut.
func TestKeyEchoedAcrossAFieldCutNeverLeaks(t *testing.T) {
	pad := func(n int) string { return strings.Repeat("x", n) }
	srv := newNFServer(t, func(ctx context.Context, c *websocket.Conn, n int) {
		hello(ctx, c)
		send(ctx, c, evt(art("cut", pad(maxTitle-16)+testToken+" tail", map[string]any{
			"sourceUrl": "https://example.test/" + pad(maxURL-16-len("https://example.test/")) + url.QueryEscape(testToken),
			"source":    map[string]any{"id": pad(maxSrc-16) + testToken, "name": pad(maxSrc-16) + testToken},
		})))
		serve(ctx, c, nil)
	})
	r := newRig(t, []string{srv.url()}).run()
	waitArticles(t, r.dir, 1)
	r.stop()

	arts := articlesOf(readDirStrict(t, r.dir))
	if len(arts) != 1 {
		t.Fatalf("articles = %v", arts)
	}
	for _, f := range []string{"title", "url", "src", "srcName"} {
		if s := arts[0][f].(string); !strings.Contains(s, "***") {
			t.Errorf("%s should carry the masked marker, got %q", f, s[len(s)-30:])
		}
	}
	assertNoKey(t, r)
}

func TestKeyNeverLeaksFromAnHTTPRefusalBody(t *testing.T) {
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unknown key "+r.URL.Query().Get("apiKey")+" ("+r.URL.RawQuery+")", http.StatusUnauthorized)
	}))
	t.Cleanup(refuse.Close)
	r := newRig(t, []string{wsURLOf(refuse)}).run()
	waitFor(t, "backoff wait", func() bool { return len(r.sl.durations()) >= 1 })
	r.stop()
	assertNoKey(t, r)
}

func TestErrorsAreScrubbedAndCarryNoKey(t *testing.T) {
	s := New(Config{Token: testToken})
	raw := &url.Error{Op: "Get", URL: keyed("http://h/socket.io/?EIO=3", testToken), Err: io.ErrUnexpectedEOF}
	got := s.cleanErr(raw)
	if strings.Contains(got.Error(), "-TOKEN") || !strings.Contains(got.Error(), "apiKey=***") {
		t.Fatalf("cleanErr = %v", got)
	}
	// A *url.Error in the chain would still hold the keyed URL; the cleaned
	// error must be a plain string error.
	var ue *url.Error
	if errors.As(got, &ue) {
		t.Fatal("cleaned error still wraps the *url.Error")
	}
	if s.cleanErr(nil) != nil {
		t.Fatal("cleanErr(nil)")
	}
	if s.scrub("nothing to hide") != "nothing to hide" {
		t.Fatal("scrub changed an innocent string")
	}
	for _, v := range secretsFor(testToken) {
		if strings.Contains(s.scrub("a "+v+" b"), v) {
			t.Errorf("spelling %q survives scrub", v)
		}
	}
	if New(Config{}).scrub("x") != "x" {
		t.Fatal("an empty token must scrub nothing")
	}
}
