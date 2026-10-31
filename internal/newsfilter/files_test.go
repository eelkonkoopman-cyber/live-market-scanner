package newsfilter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDay(t *testing.T, dir, day string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(dir, fileName(day)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirReadsWindowAndKeepsEarlierEvents(t *testing.T) {
	dir := t.TempDir()
	ms := func(d, h int) int64 { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC).UnixMilli() }
	rec := func(id string, at int64) string {
		return string(jsonLine(Record{Ms: at, ID: id, Title: id, Syms: []string{"X"}}))
	}
	ev := func(e Event) string { return string(jsonLine(e)) }

	writeDay(t, dir, "2026-10-26", ev(Event{Ev: EvConnected, Ms: ms(26, 9)}))
	writeDay(t, dir, "2026-10-27", ev(Event{Ev: EvConnected, Ms: ms(27, 9)})+rec("too-old", ms(27, 10)))
	writeDay(t, dir, "2026-10-28",
		rec("before-window", ms(28, 8)),
		strings.TrimRight(ev(Event{Ev: EvDropped, Ms: ms(28, 20), Last: ms(28, 19)}), "\n"),
		"this line is not json",
		`{"neither":"event nor article"}`,
		rec("in-window", ms(28, 22)),
	)
	// No file for the 29th. The last day is partial and ends in a torn write.
	writeDay(t, dir, "2026-10-30",
		strings.TrimRight(ev(Event{Ev: EvConnected, Ms: ms(30, 3)}), "\n"),
		strings.TrimRight(rec("today", ms(30, 4)), "\n"),
		strings.TrimRight(rec("after-window", ms(30, 15)), "\n"),
		`{"ms":123,"id":"torn`,
	)

	// Window: 2026-10-28 12:00 to 2026-10-30 12:00.
	c, err := LoadDir(dir, ms(28, 12), ms(30, 12))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range c.Records {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "in-window,today" {
		t.Errorf("records = %v", ids)
	}
	// Events are read from the day before the window's first day as well (the
	// 27th): whether the stream was up when the window opened depends on them.
	// The 26th is out of range, and so are the 27th's articles.
	if len(c.Events) != 3 || c.Events[0].Ms != ms(27, 9) || c.Events[1].Ev != EvDropped || c.Events[1].Last != ms(28, 19) || c.Events[2].Ev != EvConnected {
		t.Errorf("events = %+v", c.Events)
	}
	if c.Files != 3 {
		t.Errorf("files read = %d, want 3 (27th, 28th, 30th)", c.Files)
	}
	if c.Bad != 3 { // not json, neither, torn
		t.Errorf("bad = %d, want 3", c.Bad)
	}
}

func TestLoadDirErrors(t *testing.T) {
	if _, err := LoadDir(filepath.Join(t.TempDir(), "missing"), 0, 1); err == nil || !strings.Contains(err.Error(), "no Newsfilter recordings") {
		t.Errorf("missing dir: %v", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(file, 0, 1); err == nil {
		t.Error("a file is not a directory")
	}
	c, err := LoadDir(t.TempDir(), time.Now().Add(-48*time.Hour).UnixMilli(), time.Now().UnixMilli())
	if err != nil || len(c.Records) != 0 || len(c.Events) != 0 || c.Files != 0 {
		t.Errorf("an empty dir is an empty capture: %+v %v", c, err)
	}
}

func TestLoadDirReadsWhatTheRecorderWrites(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{dir: dir}
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	if err := r.write(at, jsonLine(Event{Ev: EvConnected, Ms: at, URL: "wss"})); err != nil {
		t.Fatal(err)
	}
	if err := r.write(at+1000, jsonLine(Record{Ms: at + 1000, ID: "a", Pub: at, Src: "businesswire", SrcName: "Business Wire", Title: "T", Syms: []string{"A", "B"}, URL: "https://x"})); err != nil {
		t.Fatal(err)
	}
	r.close()
	c, err := LoadDir(dir, at-1000, at+60_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Events) != 1 || c.Events[0].URL != "wss" {
		t.Errorf("events = %+v", c.Events)
	}
	want := Record{Ms: at + 1000, ID: "a", Pub: at, Src: "businesswire", SrcName: "Business Wire", Title: "T", Syms: []string{"A", "B"}, URL: "https://x"}
	if len(c.Records) != 1 || c.Records[0].ID != want.ID || c.Records[0].URL != want.URL || len(c.Records[0].Syms) != 2 || c.Records[0].Pub != at {
		t.Errorf("records = %+v", c.Records)
	}
}

// A replay after a restart must not count as a second, later arrival.
func TestLoadDirKeepsTheEarliestSightingOfAnID(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{dir: dir}
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	for _, x := range []Record{
		{Ms: at + 5000, ID: "a", Title: "replayed first in the file"},
		{Ms: at + 1000, ID: "a", Title: "the real arrival"},
		{Ms: at + 2000, ID: "b", Title: "only once"},
		{Ms: at + 9000, ID: "a", Title: "replayed again"},
	} {
		if err := r.write(x.Ms, jsonLine(x)); err != nil {
			t.Fatal(err)
		}
	}
	r.close()
	c, err := LoadDir(dir, at-1000, at+60_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Records) != 2 || c.Dupes != 2 {
		t.Fatalf("records = %+v, dupes = %d", c.Records, c.Dupes)
	}
	if c.Records[0].ID != "a" || c.Records[0].Ms != at+1000 || c.Records[0].Title != "the real arrival" {
		t.Errorf("kept %+v, want the earliest sighting", c.Records[0])
	}
}
