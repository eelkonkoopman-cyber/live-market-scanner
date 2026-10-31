package config

import (
	"os"
	"path/filepath"
	"testing"
)

// nfHome points ConfigDir (os.UserHomeDir) at a fresh temp dir and returns the
// ~/.marketscanner path inside it. USERPROFILE is what Windows resolves, HOME
// is what everything else does.
func nfHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("USERPROFILE", h)
	t.Setenv("HOME", h)
	dir := filepath.Join(h, ".marketscanner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func nfWriteToken(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewsfilterTokenEmptyByDefault(t *testing.T) {
	nfHome(t)
	t.Setenv("SCANNER_NEWSFILTER", "")
	t.Setenv("SCANNER_NEWSFILTER_TOKEN", "")
	if got := NewsfilterToken(); got != "" {
		t.Fatalf("no env, no file: want empty, got %q", got)
	}
}

func TestNewsfilterTokenOffSwitchBeatsEverything(t *testing.T) {
	dir := nfHome(t)
	nfWriteToken(t, dir, "newsfiltertoken", "file-token")
	t.Setenv("SCANNER_NEWSFILTER_TOKEN", "env-token")
	t.Setenv("SCANNER_NEWSFILTER", "0")
	if got := NewsfilterToken(); got != "" {
		t.Fatalf("SCANNER_NEWSFILTER=0 must win over env token and file, got %q", got)
	}
	// Only the literal 0 switches it off.
	t.Setenv("SCANNER_NEWSFILTER", "1")
	if got := NewsfilterToken(); got != "env-token" {
		t.Fatalf("SCANNER_NEWSFILTER=1 is not an off switch, got %q", got)
	}
}

func TestNewsfilterTokenEnvBeatsFile(t *testing.T) {
	dir := nfHome(t)
	t.Setenv("SCANNER_NEWSFILTER", "")
	nfWriteToken(t, dir, "newsfiltertoken", "file-token")
	t.Setenv("SCANNER_NEWSFILTER_TOKEN", "  env-token \t")
	if got := NewsfilterToken(); got != "env-token" {
		t.Fatalf("env must beat the file and be trimmed, got %q", got)
	}
	t.Setenv("SCANNER_NEWSFILTER_TOKEN", "   ")
	if got := NewsfilterToken(); got != "file-token" {
		t.Fatalf("blank env must fall through to the file, got %q", got)
	}
}

func TestNewsfilterTokenFileBOMAndWhitespace(t *testing.T) {
	dir := nfHome(t)
	t.Setenv("SCANNER_NEWSFILTER", "")
	t.Setenv("SCANNER_NEWSFILTER_TOKEN", "")
	nfWriteToken(t, dir, "newsfiltertoken", "\xef\xbb\xbf  file-token\r\n")
	if got := NewsfilterToken(); got != "file-token" {
		t.Fatalf("BOM and whitespace must be trimmed, got %q", got)
	}
	// The .txt spelling is accepted too, like every other secret file.
	if err := os.Remove(filepath.Join(dir, "newsfiltertoken")); err != nil {
		t.Fatal(err)
	}
	nfWriteToken(t, dir, "newsfiltertoken.txt", "\xef\xbb\xbftxt-token\n")
	if got := NewsfilterToken(); got != "txt-token" {
		t.Fatalf("newsfiltertoken.txt fallback: got %q", got)
	}
}
