package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScannerRunning(t *testing.T) {
	// Any status counts as "running" — even a 404 or 500.
	for _, code := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		addr := strings.TrimPrefix(srv.URL, "http://")
		if !scannerRunning(addr) {
			t.Errorf("status %d: scannerRunning(%s) = false, want true", code, addr)
		}
		srv.Close()
	}

	// A port nothing listens on: grab a free one, then release it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	if scannerRunning(addr) {
		t.Errorf("scannerRunning(%s) = true on a closed port", addr)
	}
}

func TestHostPort(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:7878": "127.0.0.1:7878",
		":7878":          "127.0.0.1:7878",
		"0.0.0.0:7878":   "127.0.0.1:7878",
		"[::]:7878":      "127.0.0.1:7878",
		"localhost:80":   "localhost:80",
	} {
		if got := hostPort(in); got != want {
			t.Errorf("hostPort(%q) = %q, want %q", in, got, want)
		}
	}
}
