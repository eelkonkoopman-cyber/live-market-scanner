package newsfilter

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// --- wire format ---
//
// Socket.io v2 over Engine.IO v3, text frames only. Built from Newsfilter's
// docs, NOT from observed traffic (nobody had a key when this was written), so
// every parser here is deliberately forgiving: a surprise is counted and
// logged, never fatal, and the first real run's raw-frames.jsonl is there to
// check the docs against reality.

// Field caps. A title is a title; these only stop a hostile or broken frame
// from turning one article into a megabyte-per-line day file, or from filling
// the seen-set (which holds ids for the life of the process) with huge ids.
//
// maxTitle, maxURL and maxSrc are CUTS and are applied by the recorder after
// the key scrub (Stream.capped), never here: a cut before the scrub could
// land inside an echoed key and leave a prefix nothing would recognise.
// maxID and maxSym are refusals, nothing is cut, so they are enforced here: an
// id longer than maxID is no id (the article is skipped as having none) and a
// symbol longer than maxSym is no ticker (dropped).
const (
	maxTitle = 2000
	maxURL   = 2000
	maxSrc   = 200 // source id and source name
	maxID    = 256
	maxSym   = 32
	maxSyms  = 64
)

// article is one Newsfilter article reduced to what the recorder keeps.
type article struct {
	ID      string
	Title   string
	URL     string
	PubMs   int64 // 0 when publishedAt is missing or unparseable
	Src     string
	SrcName string
	Syms    []string

	badFields int // fields present with a type we could not use
}

var (
	errNotObject = errors.New("article is not a JSON object")
	errNoEvent   = errors.New("event frame has no event name")
	errNoPayload = errors.New("articles event has no payload")
	errBadList   = errors.New("articles payload is neither an array nor {\"articles\":[...]}")
)

// pubLayouts covers every publishedAt shape seen in their docs:
//
//	2020-06-08T12:23:00-04:00     RFC 3339 with a colon offset
//	2020-06-08T15:50:02Z          Zulu
//	2020-05-20T22:32:22+0000      offset without the colon
//	2020-05-20T20:18:32.731Z      fractional seconds (Parse accepts a fraction
//	                              after the seconds field whatever the layout)
var pubLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700"}

// parsePub returns the publication time in epoch ms, or 0 when s is not one of
// the known shapes. A zone-less stamp is rejected rather than guessed: the
// recorded value is compared against another feed's clock, and a wrong zone
// would silently shift it by hours.
func parsePub(s string) int64 {
	s = strings.TrimSpace(s)
	for _, layout := range pubLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() < 2000 {
				return 0
			}
			return t.UnixMilli()
		}
	}
	return 0
}

// parseOpen reads the Engine.IO open packet body (the part after the leading
// "0"): {"sid":"..","pingInterval":25000,"pingTimeout":5000,...}. Zero means
// unknown; a malformed body is not an error, the defaults simply apply.
func parseOpen(p []byte) (pingInterval, pingTimeout time.Duration) {
	var o struct {
		PingInterval json.Number `json:"pingInterval"`
		PingTimeout  json.Number `json:"pingTimeout"`
	}
	if json.Unmarshal(p, &o) != nil {
		return 0, 0
	}
	ms := func(n json.Number) time.Duration {
		f, err := n.Float64()
		if err != nil || f <= 0 || f > 24*3600*1000 {
			return 0
		}
		return time.Duration(f * float64(time.Millisecond))
	}
	return ms(o.PingInterval), ms(o.PingTimeout)
}

// eventResult is what one "42[...]" frame yielded.
type eventResult struct {
	articles []article
	bad      int  // article elements that were not objects
	noID     int  // article objects without an id
	other    bool // a well-formed event that is not "articles"
}

// parseEvent decodes the part of a "42" frame after the "42". An error means
// the frame as a whole was unusable; per-article problems are reported in the
// result instead, so one bad article never costs the others in its frame.
func parseEvent(p []byte) (eventResult, error) {
	var res eventResult
	i := bytes.IndexByte(p, '[')
	if i < 0 || !validEventPrefix(p[:i]) {
		return res, errNoEvent
	}
	var args []json.RawMessage
	if err := json.Unmarshal(p[i:], &args); err != nil {
		return res, err
	}
	if len(args) == 0 {
		return res, errNoEvent
	}
	var name string
	if err := json.Unmarshal(args[0], &name); err != nil || name == "" {
		return res, errNoEvent
	}
	if name != "articles" {
		res.other = true
		return res, nil
	}
	if len(args) < 2 {
		return res, errNoPayload
	}
	list, err := articleList(args[1])
	if err != nil {
		return res, err
	}
	for _, raw := range list {
		a, err := parseArticle(raw)
		switch {
		case err != nil:
			res.bad++
		case a.ID == "":
			res.noID++
		default:
			res.articles = append(res.articles, a)
		}
	}
	return res, nil
}

// validEventPrefix accepts what socket.io v2 may put between "42" and the JSON
// array: an optional namespace ("/ns,") and an optional ack id (digits).
func validEventPrefix(p []byte) bool {
	if len(p) > 0 && p[0] == '/' {
		c := bytes.IndexByte(p, ',')
		if c < 0 {
			return false
		}
		p = p[c+1:]
	}
	for _, b := range p {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

// articleList accepts the payload as {"articles":[...]} or as a bare array.
func articleList(raw json.RawMessage) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errBadList
	}
	var list []json.RawMessage
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	case '{':
		var o struct {
			Articles json.RawMessage `json:"articles"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		o.Articles = bytes.TrimSpace(o.Articles)
		if len(o.Articles) == 0 || o.Articles[0] != '[' {
			return nil, errBadList
		}
		if err := json.Unmarshal(o.Articles, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	return nil, errBadList
}

// parseArticle reads one article field by field, so a wrongly typed field
// costs only that field (counted in badFields), never the article: the id and
// the arrival time are what the comparison needs, the rest is context.
func parseArticle(raw json.RawMessage) (article, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return article{}, errNotObject
	}
	var a article
	a.ID = a.id(m["id"])
	a.Title = a.str(m, "title")
	a.URL = a.str(m, "sourceUrl")
	if s := a.str(m, "publishedAt"); s != "" {
		a.PubMs = parsePub(s)
	}
	if r, ok := m["source"]; ok && !isNull(r) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(r, &obj) == nil && obj != nil {
			a.Src = a.str(obj, "id")
			a.SrcName = a.str(obj, "name")
		} else if s, ok := a.asString(r); ok { // tolerate "source":"businesswire"
			a.Src = s
		}
	}
	if r, ok := m["symbols"]; ok && !isNull(r) {
		var syms []json.RawMessage
		if err := json.Unmarshal(r, &syms); err != nil {
			a.badFields++
		}
		for _, sr := range syms {
			s, ok := a.asString(sr)
			if !ok {
				continue
			}
			// Case is folded by the caller, after the key scrub: uppercasing
			// first would turn a key that a server echoed into a spelling no
			// scrub list contains.
			s = strings.TrimSpace(s)
			switch {
			case s == "":
			case len(s) > maxSym: // not a ticker
				a.badFields++
			case len(a.Syms) < maxSyms:
				a.Syms = append(a.Syms, s)
			}
		}
	}
	return a, nil
}

func isNull(r json.RawMessage) bool { return string(bytes.TrimSpace(r)) == "null" }

// str returns m[key] as a string; a present non-string, non-null value counts
// as a bad field.
func (a *article) str(m map[string]json.RawMessage, key string) string {
	r, ok := m[key]
	if !ok || isNull(r) {
		return ""
	}
	s, _ := a.asString(r)
	return s
}

func (a *article) asString(r json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(r, &s); err != nil {
		a.badFields++
		return "", false
	}
	return s, true
}

// id accepts a string, or a bare number (rendered as written), as the id. One
// longer than maxID is refused (counted as a bad field, so the article reads as
// having no id): the seen-set would keep it for the life of the process.
func (a *article) id(r json.RawMessage) string {
	id := a.idText(r)
	if len(id) > maxID {
		a.badFields++
		return ""
	}
	return id
}

func (a *article) idText(r json.RawMessage) string {
	if len(r) == 0 || isNull(r) {
		return ""
	}
	r = bytes.TrimSpace(r)
	if r[0] == '"' {
		s, _ := a.asString(r)
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(r, &n) == nil && n != "" {
		return n.String()
	}
	a.badFields++
	return ""
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n-- // s[n] is mid-rune: back up to the rune's first byte
	}
	return s[:n]
}
