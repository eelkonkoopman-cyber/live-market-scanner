// Package shortable loads IBKR's public short-sale availability file and keeps
// a symbol -> availability map for the scanner's shortability columns.
//
// IBKR publishes the whole US list every 15 minutes at
// ftp://shortstock@ftp2.interactivebrokers.com/usa.txt (blank password). One
// ~2 MB download covers ~20k tickers, which is why this beats the TWS API for a
// scanner: per-symbol shortability ticks would burn a market-data line each and
// IB caps concurrent lines far below a scanner universe.
//
// FTP is the only transport IBKR offers for it (no HTTP mirror), so this file
// implements the small slice of RFC 959 needed for one passive download rather
// than pulling in an FTP dependency.
package shortable

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

const (
	defaultAddr = "ftp2.interactivebrokers.com:21"
	defaultFile = "usa.txt"
	defaultUser = "shortstock"
	// IBKR refreshes every 15 minutes; poll on the same cadence.
	refreshEvery = 15 * time.Minute
	dialTimeout  = 20 * time.Second
	// The file writes ">10000000" instead of an exact count for deep names.
	cappedPrefix = ">"
)

type Config struct {
	Addr string // host:port (default ftp2.interactivebrokers.com:21)
	File string // default usa.txt
	User string // default shortstock
	Pass string // blank
	Now  func() time.Time
}

// Store fetches and holds the availability map; readers get an immutable
// snapshot via Map().
type Store struct {
	cfg  Config
	m    atomic.Pointer[map[string]core.ShortRef]
	asOf atomic.Int64
	// onUpdate is invoked after every successful refresh.
	onUpdate func(map[string]core.ShortRef)
}

func New(cfg Config, onUpdate func(map[string]core.ShortRef)) *Store {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.File == "" {
		cfg.File = defaultFile
	}
	if cfg.User == "" {
		cfg.User = defaultUser
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Store{cfg: cfg, onUpdate: onUpdate}
	empty := map[string]core.ShortRef{}
	s.m.Store(&empty)
	return s
}

// Map returns the current snapshot (never nil).
func (s *Store) Map() map[string]core.ShortRef { return *s.m.Load() }

// AsOf is the unix time of the loaded file's own #BOF stamp.
func (s *Store) AsOf() int64 { return s.asOf.Load() }

func (s *Store) Run(ctx context.Context) error {
	s.refresh(ctx) // prime immediately so the column isn't blank on launch
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.refresh(ctx)
		}
	}
}

func (s *Store) refresh(ctx context.Context) {
	body, err := s.fetch(ctx)
	if err != nil {
		logx.L().Warn("shortable file fetch failed", "err", err)
		return
	}
	m, asOf, err := Parse(body)
	if err != nil {
		logx.L().Warn("shortable file parse failed", "err", err)
		return
	}
	s.m.Store(&m)
	s.asOf.Store(asOf)
	logx.L().Info("shortable list refreshed", "tickers", len(m), "asOf", time.Unix(asOf, 0).UTC().Format(time.RFC3339))
	if s.onUpdate != nil {
		s.onUpdate(m)
	}
}

// Parse reads the pipe-delimited availability file.
//
//	#BOF|2026.07.27|10:25:55
//	#SYM|CUR|NAME|CON|ISIN|REBATERATE|FEERATE|AVAILABLE|FIGI|
//	ZZZOF|USD|ZINC ONE RESOURCES INC|489941914|CA98959W2031|2.6466|0.9834|2000|BBG000WJ4YQ5|
//	#EOF|19733
func Parse(body []byte) (map[string]core.ShortRef, int64, error) {
	out := make(map[string]core.ShortRef, 24_000)
	var asOf int64
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "#BOF|") {
				asOf = parseStamp(line)
			}
			continue // header, column names, and the #EOF trailer
		}
		f := strings.Split(line, "|")
		// SYM CUR NAME CON ISIN REBATERATE FEERATE AVAILABLE [FIGI]
		if len(f) < 8 {
			continue
		}
		sym, cur := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		if sym == "" || cur != "USD" {
			continue // usa.txt carries a handful of EUR/CAD listings; skip them
		}
		avail, capped, ok := parseAvailable(f[7])
		if !ok {
			continue
		}
		rebate, _ := strconv.ParseFloat(strings.TrimSpace(f[5]), 64)
		fee, _ := strconv.ParseFloat(strings.TrimSpace(f[6]), 64)
		out[sym] = core.ShortRef{
			Ticker: sym, Available: avail, Capped: capped,
			FeeRate: fee, RebateRate: rebate, AsOf: asOf,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("no rows parsed")
	}
	return out, asOf, nil
}

// parseAvailable handles both an exact count and the ">10000000" cap.
func parseAvailable(s string) (n int64, capped bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, false
	}
	if strings.HasPrefix(s, cappedPrefix) {
		capped = true
		s = strings.TrimSpace(strings.TrimPrefix(s, cappedPrefix))
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, false
	}
	return v, capped, true
}

// parseStamp reads "#BOF|2026.07.27|10:25:55" as a UTC unix time. IBKR stamps
// these in US/Eastern; we only use it for display freshness, so a whole-hour
// offset error is immaterial and UTC keeps it dependency-free.
func parseStamp(line string) int64 {
	p := strings.Split(line, "|")
	if len(p) < 3 {
		return 0
	}
	t, err := time.Parse("2006.01.02 15:04:05", p[1]+" "+p[2])
	if err != nil {
		return 0
	}
	return t.Unix()
}

// --- minimal FTP (RFC 959) passive download ---

func (s *Store) fetch(ctx context.Context) ([]byte, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}

	c := textproto.NewConn(conn)
	defer c.Close()
	if _, _, err := c.ReadResponse(220); err != nil { // greeting
		return nil, fmt.Errorf("greeting: %w", err)
	}
	// 230 = logged in outright; 331 = password required (blank is accepted).
	if err := cmdExpect(c, 230, 331, "USER "+s.cfg.User); err != nil {
		return nil, err
	}
	if err := cmdExpect(c, 230, 202, "PASS "+s.cfg.Pass); err != nil {
		return nil, err
	}
	if err := cmdExpect(c, 200, 0, "TYPE I"); err != nil {
		return nil, err
	}

	dataAddr, err := s.passiveAddr(c)
	if err != nil {
		return nil, err
	}
	dataConn, err := d.DialContext(ctx, "tcp", dataAddr)
	if err != nil {
		return nil, fmt.Errorf("data dial %s: %w", dataAddr, err)
	}
	defer dataConn.Close()
	_ = dataConn.SetDeadline(time.Now().Add(2 * time.Minute))

	// 150 = transfer starting, 125 = already open.
	if err := cmdExpect(c, 150, 125, "RETR "+s.cfg.File); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(dataConn, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	_ = dataConn.Close()
	if _, _, err := c.ReadResponse(226); err != nil { // transfer complete
		return nil, fmt.Errorf("transfer end: %w", err)
	}
	_, _, _ = cmd(c, 221, "QUIT")
	return body, nil
}

// cmd sends one FTP command verbatim and reads its reply.
func cmd(c *textproto.Conn, expect int, command string) (int, string, error) {
	id, err := c.Cmd("%s", command)
	if err != nil {
		return 0, "", err
	}
	c.StartResponse(id)
	defer c.EndResponse(id)
	return c.ReadResponse(expect)
}

// cmdExpect accepts either of two success codes (alt may be 0 for "none").
func cmdExpect(c *textproto.Conn, want, alt int, command string) error {
	code, msg, err := cmd(c, want, command)
	if err == nil {
		return nil
	}
	if alt != 0 && code == alt {
		return nil
	}
	verb := command
	if i := strings.IndexByte(command, ' '); i > 0 {
		verb = command[:i]
	}
	return fmt.Errorf("%s: %d %s: %w", verb, code, msg, err)
}

// passiveAddr negotiates a passive data connection. IBKR's server answers with
// RFC 2428 extended passive mode (229 "(|||port|)"), so EPSV is tried first;
// PASV remains as a fallback for plain servers. EPSV returns only a port — the
// data connection reuses the control connection's host.
func (s *Store) passiveAddr(c *textproto.Conn) (string, error) {
	if _, msg, err := cmd(c, 229, "EPSV"); err == nil {
		port, perr := parseEpsv(msg)
		if perr == nil {
			host, _, herr := net.SplitHostPort(s.cfg.Addr)
			if herr != nil {
				host = s.cfg.Addr
			}
			return net.JoinHostPort(host, strconv.Itoa(port)), nil
		}
	}
	_, msg, err := cmd(c, 227, "PASV")
	if err != nil {
		return "", fmt.Errorf("PASV: %w", err)
	}
	return parsePasv(msg)
}

// parseEpsv reads the port from "Entering Extended Passive Mode (|||31249|)".
func parseEpsv(msg string) (int, error) {
	open := strings.LastIndex(msg, "(")
	closeIdx := strings.LastIndex(msg, ")")
	if open < 0 || closeIdx < open {
		return 0, fmt.Errorf("bad EPSV reply %q", msg)
	}
	inner := msg[open+1 : closeIdx]
	// Format is <d><d><d>port<d> where d is any delimiter (conventionally '|').
	parts := strings.Split(inner, "|")
	if len(parts) < 4 {
		return 0, fmt.Errorf("bad EPSV tuple %q", msg)
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[3]))
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("bad EPSV port %q", inner)
	}
	return port, nil
}

// parsePasv extracts host:port from "227 Entering Passive Mode (h1,h2,h3,h4,p1,p2)".
func parsePasv(msg string) (string, error) {
	open := strings.LastIndex(msg, "(")
	close := strings.LastIndex(msg, ")")
	if open < 0 || close < open {
		return "", fmt.Errorf("bad PASV reply %q", msg)
	}
	parts := strings.Split(msg[open+1:close], ",")
	if len(parts) != 6 {
		return "", fmt.Errorf("bad PASV tuple %q", msg)
	}
	nums := make([]int, 6)
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 || n > 255 {
			return "", fmt.Errorf("bad PASV octet %q", p)
		}
		nums[i] = n
	}
	host := fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], nums[3])
	return fmt.Sprintf("%s:%d", host, nums[4]*256+nums[5]), nil
}
