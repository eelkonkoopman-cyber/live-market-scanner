package shortable

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Sample rows copied verbatim from a real IBKR usa.txt fetch, including the
// ">10000000" cap, a negative rebate rate (hard-to-borrow), and the #EOF
// trailer — all of which broke naive parsers.
const sample = `#BOF|2026.07.27|10:25:55
#SYM|CUR|NAME|CON|ISIN|REBATERATE|FEERATE|AVAILABLE|FIGI|
AAPL|USD|APPLE INC|265598|US0378331005|3.3800|0.2500|>10000000|BBG000B9XRY4|
0JS|EUR|MEDMEN ENTERPRISES INC-CL B|603519992|CA58507M1077|-10.6708|12.8558|15000|BBG00L7M6F43|
GME|USD|GAMESTOP CORP-CLASS A|36285627|US36467W1099|-8.2500|11.7500|4000|BBG000BB5BF6|
ZZZOF|USD|ZINC ONE RESOURCES INC|489941914|CA98959W2031|2.6466|0.9834|2000|BBG000WJ4YQ5|
#EOF|4
`

func TestParseRealWorldFile(t *testing.T) {
	m, asOf, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// EUR listing must be skipped; the #BOF/#SYM/#EOF lines must not become rows.
	if len(m) != 3 {
		t.Fatalf("want 3 USD rows, got %d: %v", len(m), keys(m))
	}
	if _, ok := m["0JS"]; ok {
		t.Fatal("non-USD listing should be skipped")
	}

	// The ">10000000" cap must parse as a floor, not be dropped or mis-read.
	aapl := m["AAPL"]
	if aapl.Available != 10_000_000 || !aapl.Capped {
		t.Fatalf("capped row: avail=%d capped=%v", aapl.Available, aapl.Capped)
	}

	// Hard-to-borrow: negative rebate, high fee.
	gme := m["GME"]
	if gme.Available != 4000 || gme.Capped {
		t.Fatalf("GME avail: %d capped=%v", gme.Available, gme.Capped)
	}
	if gme.FeeRate != 11.75 || gme.RebateRate != -8.25 {
		t.Fatalf("GME rates: fee=%v rebate=%v", gme.FeeRate, gme.RebateRate)
	}

	want := time.Date(2026, 7, 27, 10, 25, 55, 0, time.UTC).Unix()
	if asOf != want {
		t.Fatalf("asOf = %d want %d", asOf, want)
	}
	if m["AAPL"].AsOf != want {
		t.Fatal("row AsOf should carry the file stamp")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, _, err := Parse([]byte("#BOF|x\n#EOF|0\n")); err == nil {
		t.Fatal("a file with no data rows should error, not return an empty map")
	}
}

func TestParseAvailableForms(t *testing.T) {
	cases := []struct {
		in     string
		n      int64
		capped bool
		ok     bool
	}{
		{"2000", 2000, false, true},
		{">10000000", 10_000_000, true, true},
		{"> 10000000", 10_000_000, true, true},
		{"", 0, false, false},
		{"N/A", 0, false, false},
	}
	for _, c := range cases {
		n, capped, ok := parseAvailable(c.in)
		if n != c.n || capped != c.capped || ok != c.ok {
			t.Fatalf("parseAvailable(%q) = %d,%v,%v want %d,%v,%v", c.in, n, capped, ok, c.n, c.capped, c.ok)
		}
	}
}

// IBKR's server replies in RFC 2428 extended passive mode.
func TestParseEpsv(t *testing.T) {
	got, err := parseEpsv(`Extended Passive Mode Entered (|||31249|).`)
	if err != nil {
		t.Fatalf("parseEpsv: %v", err)
	}
	if got != 31249 {
		t.Fatalf("port = %d want 31249", got)
	}
	for _, bad := range []string{"no parens", "(||)", "(|||notaport|)", "(|||0|)"} {
		if _, err := parseEpsv(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestParsePasv(t *testing.T) {
	got, err := parsePasv("Entering Passive Mode (192,168,1,5,19,137)")
	if err != nil {
		t.Fatalf("parsePasv: %v", err)
	}
	if got != "192.168.1.5:5001" { // 19*256+137
		t.Fatalf("got %s", got)
	}
	for _, bad := range []string{"no parens", "(1,2,3)", "(1,2,3,4,5,999)"} {
		if _, err := parsePasv(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestStoreStartsEmptyNotNil(t *testing.T) {
	s := New(Config{}, nil)
	if s.Map() == nil {
		t.Fatal("Map() must never be nil")
	}
	if s.cfg.Addr != defaultAddr || s.cfg.File != "usa.txt" || s.cfg.User != "shortstock" {
		t.Fatalf("defaults: %+v", s.cfg)
	}
}

// TestLiveShortableFile downloads IBKR's real file over FTP and sanity-checks it.
//
//	SCANNER_LIVE_SHORT_TEST=1 go test ./internal/shortable -run TestLiveShortable -v
func TestLiveShortableFile(t *testing.T) {
	if os.Getenv("SCANNER_LIVE_SHORT_TEST") == "" {
		t.Skip("set SCANNER_LIVE_SHORT_TEST=1 to download the real IBKR file")
	}
	s := New(Config{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	body, err := s.fetch(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	t.Logf("downloaded %d bytes", len(body))
	m, asOf, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("parsed %d USD tickers, file stamped %s", len(m), time.Unix(asOf, 0).UTC().Format(time.RFC3339))
	if len(m) < 1000 {
		t.Fatalf("suspiciously few tickers: %d", len(m))
	}
	var capped, htb int
	for _, r := range m {
		if r.Capped {
			capped++
		}
		if r.FeeRate > 10 {
			htb++
		}
	}
	t.Logf("easy-to-borrow (capped >10M): %d | fee > 10%%: %d", capped, htb)
	for _, probe := range []string{"AAPL", "TSLA"} {
		if r, ok := m[probe]; ok {
			t.Logf("  %s: avail=%d capped=%v fee=%.2f%% rebate=%.2f%%", probe, r.Available, r.Capped, r.FeeRate, r.RebateRate)
		} else {
			t.Logf("  %s: not listed", probe)
		}
	}
	if !strings.HasPrefix(string(body), "#BOF|") {
		t.Error("file no longer starts with #BOF — format may have changed")
	}
}

func keys(m map[string]core.ShortRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
