package summarize

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// findClaude locates a runnable Claude Code CLI. PATH first; then the copy the
// Claude desktop app bundles under %APPDATA%\Claude\claude-code\<ver>\ (newest
// version wins); then the native-installer and npm-global locations. The
// desktop app's own Squirrel stub (…\AppData\Local\AnthropicClaude\claude.exe)
// is explicitly rejected — running it with -p would open the GUI.
func findClaude() (string, bool) {
	if p, err := exec.LookPath("claude"); err == nil && !isDesktopStub(p) {
		return p, true
	}
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		if m, _ := filepath.Glob(filepath.Join(appdata, "Claude", "claude-code", "*", "claude.exe")); len(m) > 0 {
			sort.Strings(m) // version dirs sort lexically; newest last is good enough
			return m[len(m)-1], true
		}
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local", "bin", "claude.exe"),
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(os.Getenv("APPDATA"), "npm", "claude.cmd"),
	} {
		if p != "" && exists(p) {
			return p, true
		}
	}
	return "", false
}

func isDesktopStub(p string) bool {
	return strings.Contains(strings.ToLower(p), strings.ToLower(filepath.Join("AppData", "Local", "AnthropicClaude")))
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// cliBackend shells out to the local Claude Code CLI in headless print mode
// (`claude -p`), which bills against the user's existing Claude subscription
// instead of API tokens. The prompt goes in via stdin; the reply comes back as
// plain text on stdout.
type cliBackend struct {
	path   string
	model  string
	effort string // passed as --effort; "" leaves the CLI's own default
}

func (b *cliBackend) name() string { return "cli" }

// args builds the headless invocation. Effort mirrors the API backend's
// output_config.effort; Haiku takes no effort level, so it is left off there.
func (b *cliBackend) args(system string) []string {
	args := []string{
		"-p",
		"--output-format", "text",
		"--append-system-prompt", system,
	}
	if b.model != "" {
		args = append(args, "--model", b.model)
	}
	if b.effort != "" && !strings.Contains(b.model, "haiku") {
		args = append(args, "--effort", b.effort)
	}
	return args
}

func (b *cliBackend) run(ctx context.Context, system, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, b.path, b.args(system)...)
	cmd.Stdin = strings.NewReader(prompt)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		// The CLI writes some errors (e.g. "Not logged in") to stdout, not stderr.
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "Not logged in") {
			msg += " — run `claude` in a terminal and /login once (uses your Claude subscription), or set ANTHROPIC_API_KEY"
		}
		return "", fmt.Errorf("claude CLI: %s", clip(msg, 400))
	}
	return out.String(), nil
}
