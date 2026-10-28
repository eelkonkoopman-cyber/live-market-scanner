package alpaca

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keys is an Alpaca API key pair. String() never prints the secret or the key
// id, so a Keys value that slips into a log line or %v stays harmless.
type Keys struct {
	KeyID  string `json:"keyId"`
	Secret string `json:"secret"`
	// TradingBase optionally overrides the trading API host used ONLY for the
	// read-only /v2/assets list (default https://api.alpaca.markets; paper
	// keys need https://paper-api.alpaca.markets).
	TradingBase string `json:"tradingBase,omitempty"`
}

func (k Keys) String() string {
	if k.Valid() {
		return "alpaca.Keys{set}"
	}
	return "alpaca.Keys{empty}"
}

// GoString keeps %#v from dumping the fields.
func (k Keys) GoString() string { return k.String() }

// Valid reports whether both halves are present.
func (k Keys) Valid() bool { return k.KeyID != "" && k.Secret != "" }

// KeyFileName is the file under the scanner's config dir holding the keys.
const KeyFileName = "alpaca.json"

// LoadKeys reads the key pair: ALPACA_KEY_ID / ALPACA_SECRET_KEY from the
// environment first, then <configDir>/alpaca.json ({"keyId":"...","secret":"..."}).
// A missing file is not an error (empty Keys, nil); a malformed one is, and the
// error message never contains the file's content.
func LoadKeys(configDir string) (Keys, error) {
	if id, sec := strings.TrimSpace(os.Getenv("ALPACA_KEY_ID")), strings.TrimSpace(os.Getenv("ALPACA_SECRET_KEY")); id != "" && sec != "" {
		return Keys{KeyID: id, Secret: sec, TradingBase: os.Getenv("ALPACA_TRADING_BASE")}, nil
	}
	b, err := os.ReadFile(filepath.Join(configDir, KeyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Keys{}, nil
	}
	if err != nil {
		return Keys{}, fmt.Errorf("read %s: %w", KeyFileName, err)
	}
	return parseKeys(b)
}

func parseKeys(b []byte) (Keys, error) {
	b = []byte(strings.TrimPrefix(string(b), "\xef\xbb\xbf")) // Windows editors add a BOM
	var k Keys
	if err := json.Unmarshal(b, &k); err != nil {
		// Deliberately not wrapping err: a json.SyntaxError can quote content.
		return Keys{}, fmt.Errorf("%s is not valid JSON (expected {\"keyId\":\"...\",\"secret\":\"...\"})", KeyFileName)
	}
	k.KeyID, k.Secret = strings.TrimSpace(k.KeyID), strings.TrimSpace(k.Secret)
	k.TradingBase = strings.TrimRight(strings.TrimSpace(k.TradingBase), "/")
	if !k.Valid() {
		return Keys{}, fmt.Errorf("%s needs both \"keyId\" and \"secret\"", KeyFileName)
	}
	return k, nil
}
