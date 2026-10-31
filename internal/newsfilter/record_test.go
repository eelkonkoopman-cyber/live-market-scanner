package newsfilter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRecorderWritesOneFilePerUTCDayAndAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newsrace", "nested")
	ms := func(d, h, m int) int64 { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC).UnixMilli() }

	r := &recorder{dir: dir}
	for _, w := range []struct {
		ms   int64
		line string
	}{
		{ms(30, 23, 59), "a\n"},
		{ms(31, 0, 0), "b\n"}, // midnight UTC itself belongs to the new day
		{ms(31, 0, 1), "c\n"},
		{ms(31, 23, 59), "d\n"},
		{ms(30, 23, 59) + 30_000, "e\n"}, // an older stamp goes back to its own day's file
	} {
		if err := r.write(w.ms, []byte(w.line)); err != nil {
			t.Fatal(err)
		}
	}
	r.close()

	read := func(day string) string {
		b, err := os.ReadFile(filepath.Join(dir, "newsfilter-"+day+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("2026-10-30"); got != "a\ne\n" {
		t.Errorf("10-30 = %q", got)
	}
	if got := read("2026-10-31"); got != "b\nc\nd\n" {
		t.Errorf("10-31 = %q", got)
	}

	// A new process appends to the day's file instead of replacing it.
	r2 := &recorder{dir: dir}
	if err := r2.write(ms(31, 12, 0), []byte("f\n")); err != nil {
		t.Fatal(err)
	}
	r2.close()
	if got := read("2026-10-31"); got != "b\nc\nd\nf\n" {
		t.Errorf("after restart 10-31 = %q", got)
	}

	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Join(dir, "newsfilter-2026-10-31.jsonl")); st.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", st.Mode().Perm())
		}
	}
	if err := (&recorder{dir: dir}).write(ms(30, 1, 1), nil); err != nil {
		t.Errorf("an empty write is a no-op, got %v", err)
	}
}

func TestRecorderReportsAnUnwritableDirAndRecovers(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{dir: filepath.Join(blocker, "sub")}
	if err := r.write(time.Now().UnixMilli(), []byte("x\n")); err == nil {
		t.Fatal("expected an error")
	}
	// The same recorder works once the obstacle is gone.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := r.write(time.Now().UnixMilli(), []byte("x\n")); err != nil {
		t.Fatalf("recorder must retry the open on the next write: %v", err)
	}
	r.close()
}

// A crash or a full disk leaves the day file ending mid-line. The next write
// must start a fresh line instead of gluing itself to the fragment, which
// would lose a good line (often the connected event) together with the bad one.
func TestRecorderStartsAFreshLineAfterATornWrite(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).UnixMilli()
	path := filepath.Join(dir, fileName("2026-10-30"))
	read := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	frag := `{"ms":1,"id":"torn","ti`
	if err := os.WriteFile(path, []byte(frag), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. A new process finds the fragment already on disk.
	r := &recorder{dir: dir}
	defer r.close()
	connected := string(jsonLine(Event{Ev: EvConnected, Ms: at, URL: "wss"}))
	alive := string(jsonLine(Event{Ev: EvAlive, Ms: at + 1}))
	if err := r.write(at, []byte(connected)); err != nil {
		t.Fatal(err)
	}
	if err := r.write(at+1, []byte(alive)); err != nil {
		t.Fatal(err)
	}
	if want := frag + "\n" + connected + alive; read() != want {
		t.Fatalf("file = %q, want %q (one separator after the fragment, none between good lines)", read(), want)
	}
	c, err := LoadDir(dir, at-1000, at+1000)
	if err != nil || c.Bad != 1 || len(c.Events) != 2 || c.Events[0].Ev != EvConnected {
		t.Fatalf("the fragment is one bad line and the connected event survives: bad %d, events %+v, err %v", c.Bad, c.Events, err)
	}

	// 2. A write that fails halfway: the handle is dropped, a fragment is left,
	// and the reopen on the next write has to repair it.
	if err := r.f.Close(); err != nil { // the next write now fails like a dead handle
		t.Fatal(err)
	}
	if err := r.write(at+2, []byte("lost\n")); err == nil || r.f != nil {
		t.Fatalf("writing through a closed handle should fail and drop the handle, got %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("half a li"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := r.write(at+3, []byte("third\n")); err != nil {
		t.Fatal(err)
	}
	r.close()
	if got := read(); !strings.HasSuffix(got, alive+"half a li\nthird\n") {
		t.Fatalf("file ends %q: the line after a failed write was glued to the fragment", got[max(len(got)-40, 0):])
	}
}

func TestJSONLineIsCompactUnescapedAndNewlineTerminated(t *testing.T) {
	b := jsonLine(Record{Ms: 5, ID: "i", Title: "A & <B> \"q\"", Syms: []string{"X"}})
	want := `{"ms":5,"id":"i","pub":0,"src":"","srcName":"","title":"A & <B> \"q\"","syms":["X"],"url":""}` + "\n"
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	if e := string(jsonLine(Event{Ev: EvConnected, Ms: 7, URL: "wss"})); e != `{"ev":"connected","ms":7,"url":"wss"}`+"\n" {
		t.Errorf("event = %s", e)
	}
	if e := string(jsonLine(Event{Ev: EvDropped, Ms: 9, Last: 8, Err: "boom"})); !strings.HasPrefix(e, `{"ev":"dropped","ms":9,`) || !strings.Contains(e, `"err":"boom"`) {
		t.Errorf("event = %s", e)
	}
}

func TestRawCaptureOpenTruncatesAndCaps(t *testing.T) {
	dir := t.TempDir()
	c := &rawCapture{scrub: func(s string) string { return strings.ReplaceAll(s, "SECRET", "***") }}
	if err := c.open(dir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rawFrameCap+50; i++ {
		c.add(int64(i), []byte("frame SECRET"), 12, false)
	}
	c.close()
	lines := readFileLines(filepath.Join(dir, rawFileName), t)
	if len(lines) != rawFrameCap {
		t.Fatalf("lines = %d, want %d", len(lines), rawFrameCap)
	}
	if lines[0]["frame"] != "frame ***" || lines[0]["n"].(float64) != 1 {
		t.Errorf("first = %v", lines[0])
	}
	// Reopening starts a fresh capture (a new process start).
	if err := c.open(dir); err != nil {
		t.Fatal(err)
	}
	c.add(1, []byte("only"), 4, false)
	c.close()
	if lines := readFileLines(filepath.Join(dir, rawFileName), t); len(lines) != 1 {
		t.Fatalf("after reopen: %d lines", len(lines))
	}
	// Adding after close, or without open, is harmless.
	c.add(2, []byte("late"), 4, false)
	(&rawCapture{scrub: func(s string) string { return s }}).add(1, []byte("x"), 1, false)
}
