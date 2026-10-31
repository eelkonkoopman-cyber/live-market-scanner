// Package config loads runtime configuration from environment variables with
// sensible Massive defaults. The API key is optional at startup — it can be set
// later from the UI; until then the feed stays idle.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	MassiveKey   string // MASSIVE_API_KEY (optional; settable at runtime)
	MassiveWSURL string // MASSIVE_WS_URL
	MassiveBase  string // MASSIVE_BASE_URL
	Listen       string // SCANNER_LISTEN
	DBPath       string // SCANNER_DB
	LogLevel     string // SCANNER_LOG
	TickHz       int    // SCANNER_TICKHZ
	OpenBrowser  bool   // SCANNER_NO_BROWSER disables (also gates opening the native window at start)
	Tray         bool   // SCANNER_NO_TRAY disables the system tray icon
	LogFile      string // SCANNER_LOG_FILE (default ~/.marketscanner/logs/scanner.log)

	// News poll cadences. NewsPoll drives the real-time Benzinga source and is the
	// dominant term in news-detection latency; NewsRefPoll gates the hourly-recency
	// reference source, which needs no fast polling.
	NewsPoll    time.Duration // SCANNER_NEWS_POLL (seconds)
	NewsRefPoll time.Duration // SCANNER_NEWS_REF_POLL (seconds)
}

func Load() (Config, error) {
	c := Config{
		MassiveKey:   os.Getenv("MASSIVE_API_KEY"),
		MassiveWSURL: env("MASSIVE_WS_URL", "wss://socket.massive.com/stocks"),
		MassiveBase:  env("MASSIVE_BASE_URL", "https://api.massive.com"),
		Listen:       env("SCANNER_LISTEN", "127.0.0.1:7878"),
		DBPath:       os.Getenv("SCANNER_DB"),
		LogLevel:     env("SCANNER_LOG", "info"),
		// 10 Hz: detection latency ~100ms vs ~234ms at 4 Hz. Measured cost at 11k
		// tickers is ~13.7ms of CPU per snapshot (~14% of one core at 10 Hz), of
		// which ~83% is rebuilding every ticker's candle slice. Raise via
		// SCANNER_TICKHZ once that projection is memoized.
		TickHz:      10,
		OpenBrowser: os.Getenv("SCANNER_NO_BROWSER") == "",
		Tray:        os.Getenv("SCANNER_NO_TRAY") == "",
	}
	c.LogFile = os.Getenv("SCANNER_LOG_FILE")
	if c.LogFile == "" {
		c.LogFile = filepath.Join(ConfigDir(), "logs", "scanner.log")
	}
	if v := os.Getenv("SCANNER_TICKHZ"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.TickHz = n
		}
	}
	c.NewsPoll = envSeconds("SCANNER_NEWS_POLL", 0)
	c.NewsRefPoll = envSeconds("SCANNER_NEWS_REF_POLL", 0)
	if c.DBPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		c.DBPath = filepath.Join(home, ".marketscanner", "scanner.db")
	}
	// API key: environment first, then a saved file. If still empty, the app boots
	// anyway and the UI can set it at runtime.
	if c.MassiveKey == "" {
		c.MassiveKey = readKeyFile()
	}
	return c, nil
}

// ConfigDir returns the per-user config directory (~/.marketscanner).
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".marketscanner"
	}
	return filepath.Join(home, ".marketscanner")
}

// SaveKey persists the API key so it survives restarts.
func SaveKey(key string) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "apikey"), []byte(strings.TrimSpace(key)+"\n"), 0o600)
}

// readKeyFile returns the API key saved in the config dir, if any. It tolerates
// a UTF-8 BOM (Windows editors add one) and surrounding whitespace.
func readKeyFile() string {
	return readSecretFile("apikey", "apikey.txt")
}

// BenzingaWSToken returns the token for Benzinga's own push WebSocket:
// SCANNER_BZ_WS_TOKEN first, then ~/.marketscanner/bztoken (same file
// conventions as the Massive key). Empty = the stream stays disabled.
func BenzingaWSToken() string {
	if t := strings.TrimSpace(os.Getenv("SCANNER_BZ_WS_TOKEN")); t != "" {
		return t
	}
	return readSecretFile("bztoken", "bztoken.txt")
}

// RTPRToken returns the API key for RTPR (rtpr.io, the Business Wire /
// PR Newswire / GlobeNewswire / AccessWire aggregator): SCANNER_RTPR_TOKEN
// first, then ~/.marketscanner/rtprtoken. Empty = the poller stays disabled.
func RTPRToken() string {
	if t := strings.TrimSpace(os.Getenv("SCANNER_RTPR_TOKEN")); t != "" {
		return t
	}
	return readSecretFile("rtprtoken", "rtprtoken.txt")
}

// NewsfilterToken returns the API key for Newsfilter (newsfilter.io), which the
// scanner only RECORDS (internal/newsfilter) so its delivery times can be
// compared with RTPR's. SCANNER_NEWSFILTER=0 is an explicit off switch and is
// checked first, so a preview or test environment can never open a stream on
// the key even when the token file exists; then SCANNER_NEWSFILTER_TOKEN, then
// ~/.marketscanner/newsfiltertoken. Empty = the recorder is not started.
func NewsfilterToken() string {
	if strings.TrimSpace(os.Getenv("SCANNER_NEWSFILTER")) == "0" {
		return ""
	}
	if t := strings.TrimSpace(os.Getenv("SCANNER_NEWSFILTER_TOKEN")); t != "" {
		return t
	}
	return readSecretFile("newsfiltertoken", "newsfiltertoken.txt")
}

func readSecretFile(names ...string) string {
	for _, name := range names {
		if b, err := os.ReadFile(filepath.Join(ConfigDir(), name)); err == nil {
			s := strings.TrimPrefix(string(b), "\xef\xbb\xbf") // UTF-8 BOM bytes
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// envSeconds reads an integer-seconds env var, returning def when unset or
// unparseable. Zero means "let the consumer use its own default".
func envSeconds(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
