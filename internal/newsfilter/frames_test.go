package newsfilter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParsePubShapes(t *testing.T) {
	ms := func(y int, mo time.Month, d, h, mi, s, nsec int) int64 {
		return time.Date(y, mo, d, h, mi, s, nsec, time.UTC).UnixMilli()
	}
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"2020-06-08T12:23:00-04:00", ms(2020, 6, 8, 16, 23, 0, 0)},
		{"2020-06-08T15:50:02Z", ms(2020, 6, 8, 15, 50, 2, 0)},
		{"2020-05-20T22:32:22+0000", ms(2020, 5, 20, 22, 32, 22, 0)},
		{"2020-05-20T20:18:32.731Z", ms(2020, 5, 20, 20, 18, 32, 731e6)},
		{"2020-05-20T20:18:32.731+0100", ms(2020, 5, 20, 19, 18, 32, 731e6)},
		{"  2020-06-08T15:50:02Z  ", ms(2020, 6, 8, 15, 50, 2, 0)},
		{"2020-06-08T12:23:00", 0}, // no zone: refused rather than guessed
		{"June 8, 2020", 0},
		{"", 0},
		{"0001-01-01T00:00:00Z", 0},
	} {
		if got := parsePub(c.in); got != c.want {
			t.Errorf("parsePub(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseOpenIsForgiving(t *testing.T) {
	for _, c := range []struct {
		in     string
		pi, pt time.Duration
	}{
		{`{"sid":"x","pingInterval":25000,"pingTimeout":5000}`, 25 * time.Second, 5 * time.Second},
		{`{"pingInterval":"25000","pingTimeout":"5000"}`, 25 * time.Second, 5 * time.Second},
		{`{"pingInterval":1500.5}`, 1500500 * time.Microsecond, 0},
		{`{"pingInterval":-1,"pingTimeout":"x"}`, 0, 0},
		{`{"pingInterval":999999999999}`, 0, 0},
		{`not json`, 0, 0},
		{``, 0, 0},
	} {
		pi, pt := parseOpen([]byte(c.in))
		if pi != c.pi || pt != c.pt {
			t.Errorf("parseOpen(%q) = %v %v, want %v %v", c.in, pi, pt, c.pi, c.pt)
		}
	}
}

func TestParseEventForms(t *testing.T) {
	one := `{"id":"a","title":"T"}`
	ok := func(name, in string, wantIDs ...string) {
		t.Helper()
		res, err := parseEvent([]byte(in))
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
			return
		}
		var got []string
		for _, a := range res.articles {
			got = append(got, a.ID)
		}
		if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
			t.Errorf("%s: ids %v, want %v", name, got, wantIDs)
		}
	}
	ok("object payload", `["articles",{"articles":[`+one+`]}]`, "a")
	ok("bare array", `["articles",[`+one+`]]`, "a")
	ok("namespace", `/news,["articles",[`+one+`]]`, "a")
	ok("ack id", `12["articles",[`+one+`]]`, "a")
	ok("namespace and ack id", `/news,12["articles",[`+one+`]]`, "a")
	ok("extra args are ignored", `["articles",[`+one+`],"ack"]`, "a")
	ok("empty list", `["articles",{"articles":[]}]`)
	ok("extra keys beside articles", `["articles",{"count":1,"articles":[`+one+`]}]`, "a")

	for _, bad := range []string{
		``, `x`, `not json`, `[`, `[]`, `[1]`, `[null]`,
		`["articles"]`,
		`["articles",{"articles":"x"}]`,
		`["articles",{"articles":{"id":"a"}}]`,
		`["articles",{"nothing":[]}]`,
		`["articles","x"]`,
		`["articles",7]`,
		`["articles",null]`,
		`junk["articles",[]]`,
		`/ns["articles",[]]`, // namespace without its comma
	} {
		if _, err := parseEvent([]byte(bad)); err == nil {
			t.Errorf("parseEvent(%q) should fail", bad)
		}
	}

	res, err := parseEvent([]byte(`["other",{"articles":[` + one + `]}]`))
	if err != nil || !res.other || len(res.articles) != 0 {
		t.Errorf("a foreign event is well-formed but not news: %+v %v", res, err)
	}
}

func TestParseEventPerArticleDamage(t *testing.T) {
	res, err := parseEvent([]byte(`["articles",{"articles":[
		{"id":"a","title":"fine"},
		"a string",
		[1,2],
		null,
		{"title":"no id"},
		{"id":"","title":"empty id"},
		{"id":null},
		{"id":{"x":1},"title":"object id"},
		{"id":"b","title":["not","a","string"],"publishedAt":12,"sourceUrl":false,"symbols":[1,null,"x"]}
	]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if res.bad != 3 {
		t.Errorf("bad = %d, want 3 (a string, an array, null)", res.bad)
	}
	if res.noID != 4 {
		t.Errorf("noID = %d, want 4", res.noID)
	}
	if len(res.articles) != 2 || res.articles[0].ID != "a" || res.articles[1].ID != "b" {
		t.Fatalf("articles = %+v", res.articles)
	}
	b := res.articles[1]
	if b.Title != "" || b.PubMs != 0 || b.URL != "" || len(b.Syms) != 1 || b.Syms[0] != "x" {
		t.Errorf("the damaged fields are dropped, the good one kept: %+v", b)
	}
	if b.badFields != 4 { // title, publishedAt, sourceUrl, symbols[0]
		t.Errorf("badFields = %d, want 4", b.badFields)
	}
}

func TestParseArticleCapsAndNumbers(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"id": 12345, "title": strings.Repeat("é", maxTitle), "sourceUrl": strings.Repeat("u", maxURL*2),
		"symbols": func() []string {
			var s []string
			for i := 0; i < maxSyms*2; i++ {
				s = append(s, "S"+string(rune('A'+i%26))+string(rune('a'+i/26)))
			}
			return s
		}(),
	})
	a, err := parseArticle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "12345" {
		t.Errorf("a numeric id is kept as written: %q", a.ID)
	}
	// The title and URL are cut by the recorder AFTER the key scrub (a cut here
	// could land inside an echoed key); parseArticle hands them over whole.
	if len(a.Title) != 2*maxTitle || len(a.URL) != 2*maxURL {
		t.Errorf("title is %d bytes and url %d: parseArticle must not cut them", len(a.Title), len(a.URL))
	}
	if len(a.Syms) != maxSyms {
		t.Errorf("syms = %d, want the cap %d", len(a.Syms), maxSyms)
	}
	// The cut itself is rune-safe and applied by Stream.capped.
	s := New(Config{Token: "k"})
	if got := s.capped(a.Title, maxTitle); len(got) > maxTitle || !utf8.ValidString(got) {
		t.Errorf("capped title is %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got := s.capped(a.URL, maxURL); len(got) != maxURL {
		t.Errorf("capped url is %d bytes", len(got))
	}
}

func TestParseArticleRefusesOverlongIDsAndSymbols(t *testing.T) {
	parse := func(id any, syms ...string) article {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"id": id, "symbols": syms, "title": "t"})
		a, err := parseArticle(raw)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	// The seen-set holds ids for the life of the process: nothing past maxID goes in.
	if a := parse(strings.Repeat("i", maxID)); len(a.ID) != maxID || a.badFields != 0 {
		t.Errorf("an id of exactly maxID bytes is fine: %d bytes, %d bad fields", len(a.ID), a.badFields)
	}
	long := parse(strings.Repeat("i", maxID+1))
	if long.ID != "" || long.badFields != 1 {
		t.Errorf("an id of maxID+1 bytes is refused: id %d bytes, bad fields %d", len(long.ID), long.badFields)
	}
	if a := parse(json.Number(strings.Repeat("9", maxID+1))); a.ID != "" || a.badFields != 1 {
		t.Errorf("an overlong numeric id is refused too: %d bytes, bad fields %d", len(a.ID), a.badFields)
	}
	a := parse("ok", strings.Repeat("S", maxSym), strings.Repeat("S", maxSym+1), "  ", "abc")
	if len(a.Syms) != 2 || len(a.Syms[0]) != maxSym || a.Syms[1] != "abc" || a.badFields != 1 {
		t.Errorf("symbols = %q, bad fields %d: one over maxSym is not a ticker, a blank is skipped quietly", a.Syms, a.badFields)
	}
	// An article whose only id is refused counts as having none (and is skipped).
	res, err := parseEvent([]byte(`["articles",[{"id":"` + strings.Repeat("x", maxID+1) + `","title":"t"}]]`))
	if err != nil || len(res.articles) != 0 || res.noID != 1 {
		t.Errorf("overlong id: articles %d, noID %d, err %v", len(res.articles), res.noID, err)
	}
}

func TestClipNeverSplitsARune(t *testing.T) {
	s := "aé€😀z" // 1 + 2 + 3 + 4 + 1 bytes
	for n := 0; n <= len(s)+1; n++ {
		got := clip(s, n)
		if !utf8.ValidString(got) || len(got) > n || !strings.HasPrefix(s, got) {
			t.Errorf("clip(%q, %d) = %q", s, n, got)
		}
	}
	if clip(s, len(s)) != s {
		t.Error("clip to full length must keep everything")
	}
}

func TestValidEventPrefix(t *testing.T) {
	for in, want := range map[string]bool{
		"": true, "0": true, "123": true, "/ns,": true, "/ns,42": true, "/a/b,7": true,
		"/ns": false, "x": false, "1x": false, "/ns,x": false, " ": false,
	} {
		if got := validEventPrefix([]byte(in)); got != want {
			t.Errorf("validEventPrefix(%q) = %v, want %v", in, got, want)
		}
	}
}
