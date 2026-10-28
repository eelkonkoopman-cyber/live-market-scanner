package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/config"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// shellExeName is the native WebView2 window shell shipped next to scanner.exe.
const shellExeName = "scannerwin.exe"

// hostPort turns a listen address into one that can be dialled: a wildcard or
// empty host (":7878", "0.0.0.0:7878", "[::]:7878") becomes loopback.
func hostPort(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// baseURL is the scanner's own http origin for the given listen address.
func baseURL(listen string) string { return "http://" + hostPort(listen) }

// scannerRunning reports whether something is answering HTTP on addr (any
// status counts). It is the single-instance check and the "server is up" probe.
func scannerRunning(addr string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get("http://" + hostPort(addr) + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// waitAndOpenUI polls until the local server answers (every 250 ms, up to 30 s)
// and then opens the scanner window once.
func waitAndOpenUI(ctx context.Context, cfg config.Config) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if scannerRunning(cfg.Listen) {
			openUI(cfg)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	logx.L().Warn("server did not come up within 30s; not opening the window", "addr", cfg.Listen)
}

// shellExe returns the path of scannerwin.exe next to this executable, if any.
func shellExe() (string, bool) {
	if runtime.GOOS != "windows" {
		return "", false
	}
	self, err := os.Executable()
	if err != nil {
		return "", false
	}
	p := filepath.Join(filepath.Dir(self), shellExeName)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

var (
	spawnMu   sync.Mutex
	lastSpawn time.Time // when the main window was last launched
)

// spawnDetached starts exe as an independent process and lets go of it.
func spawnDetached(exe string, args ...string) {
	cmd := newCmd(exe, args...)
	if err := cmd.Start(); err != nil {
		logx.L().Warn("spawn failed", "exe", exe, "err", err)
		return
	}
	_ = cmd.Process.Release()
}

// openUI shows the main scanner window: it focuses an existing native window,
// else launches scannerwin.exe, else (no shell binary) opens the browser.
func openUI(cfg config.Config) {
	exe, ok := shellExe()
	if !ok {
		openBrowser(baseURL(cfg.Listen))
		return
	}
	spawnMu.Lock()
	defer spawnMu.Unlock()
	if focusMainWindow() {
		return
	}
	// A window launched moments ago may not have a title yet; don't stack a
	// second one on top of an impatient double-click.
	if !lastSpawn.IsZero() && time.Since(lastSpawn) < 4*time.Second {
		return
	}
	lastSpawn = time.Now()
	spawnDetached(exe, "--pane", "main", "--base", baseURL(cfg.Listen))
}

// openCharts opens the Charts Monitor window (or the browser equivalent).
func openCharts(cfg config.Config) {
	exe, ok := shellExe()
	if !ok {
		openBrowser(baseURL(cfg.Listen) + "/?pane=charts")
		return
	}
	spawnDetached(exe, "--pane", "charts", "--base", baseURL(cfg.Listen))
}

// openLogFile opens the log with the default handler.
func openLogFile(path string) { openBrowser(path) }
