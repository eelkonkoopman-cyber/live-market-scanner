// Package logx is a thin wrapper over log/slog so packages share one configured
// logger via logx.L().
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
)

// maxLogBytes is the size above which InitFile rotates the log at startup.
const maxLogBytes = 10 << 20

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Init configures the default slog logger (text handler to stderr).
func Init(level string) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)})))
}

// lenientWriter forwards to w but never reports an error, so a broken stderr
// (a Windows GUI-subsystem process has an invalid stderr handle) cannot make
// an io.MultiWriter drop log lines or fail.
type lenientWriter struct{ w io.Writer }

func (l lenientWriter) Write(p []byte) (int, error) {
	_, _ = l.w.Write(p)
	return len(p), nil
}

// InitFile configures the default slog logger to append to the file at path
// (and to stderr too when stderr is usable). If the file already exists and is
// larger than 10 MB it is first renamed to path+".1", replacing any older
// rotation. Go panics and fatal runtime errors are appended to the same file
// via debug.SetCrashOutput. The returned Closer closes the log file.
func InitFile(level, path string) (io.Closer, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	var w io.Writer = f
	if _, serr := os.Stderr.Stat(); serr == nil {
		w = io.MultiWriter(f, lenientWriter{os.Stderr})
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})))
	// Best effort: route panics / fatal runtime errors into the log file.
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	return fileCloser{f}, nil
}

// fileCloser releases the crash-output duplicate of the handle before closing
// the log file itself (on Windows an open duplicate blocks deleting the file).
type fileCloser struct{ f *os.File }

func (c fileCloser) Close() error {
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	return c.f.Close()
}

// L returns the default logger.
func L() *slog.Logger { return slog.Default() }
