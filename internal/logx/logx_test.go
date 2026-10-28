package logx

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitFileRotatesLargeLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "logs", "scanner.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 11<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := InitFile("info", path)
	if err != nil {
		t.Fatal(err)
	}
	L().Info("hello from test")
	defer c.Close() // also releases the crash-output handle (needed for TempDir cleanup on Windows)

	old, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("expected rotated file: %v", err)
	}
	if old.Size() != 11<<20 {
		t.Errorf("rotated size = %d", old.Size())
	}
	cur, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Size() >= 1<<20 {
		t.Errorf("new log should be small, got %d", cur.Size())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "hello from test") {
		t.Errorf("log line not written: %q", b)
	}
}

func TestInitFileSmallLogAppends(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "s.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := InitFile("info", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("small log must not rotate")
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "old\n") {
		t.Errorf("existing content lost: %q", b)
	}
}
