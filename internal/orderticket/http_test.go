package orderticket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPGuards(t *testing.T) {
	r := newRig(t, nil)
	h := r.tk.Handler()
	do := func(method, path, body string, hdr map[string]string, remote string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = remote
		req.Host = "127.0.0.1:7878"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	ok := map[string]string{"X-Order-Ticket": "1", "Content-Type": "application/json"}
	if c := do("GET", "/api/order/state", "", nil, "127.0.0.1:5000"); c != 200 {
		t.Fatalf("state %d", c)
	}
	if c := do("GET", "/api/order/state", "", nil, "192.168.1.5:5000"); c != http.StatusNotFound {
		t.Fatalf("LAN caller %d", c)
	}
	if c := do("POST", "/api/order/connect", "{}", map[string]string{"Content-Type": "application/json"}, "127.0.0.1:5000"); c != 403 {
		t.Fatalf("no header %d", c)
	}
	if c := do("POST", "/api/order/connect", "{}", map[string]string{"X-Order-Ticket": "1", "Content-Type": "text/plain"}, "127.0.0.1:5000"); c != 403 {
		t.Fatalf("simple content type %d", c)
	}
	evil := map[string]string{"X-Order-Ticket": "1", "Content-Type": "application/json", "Origin": "http://evil.example"}
	if c := do("POST", "/api/order/connect", "{}", evil, "127.0.0.1:5000"); c != 403 {
		t.Fatalf("cross origin %d", c)
	}
	if c := do("POST", "/api/order/confirm", `{"token":"x"}`, ok, "127.0.0.1:5000"); c != 400 {
		t.Fatalf("confirm without prepare %d", c)
	}
	if r.f.conns != 0 || len(r.f.placesCopy()) != 0 {
		t.Fatal("a refused request reached the client")
	}
}
