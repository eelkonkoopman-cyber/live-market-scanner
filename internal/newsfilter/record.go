package newsfilter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one first-seen article as it lands in a day file. Ms is the
// moment the frame carrying it came off the socket (before any parsing); Pub
// is Newsfilter's own publishedAt (0 when it was missing or unparseable).
type Record struct {
	Ms      int64    `json:"ms"`
	ID      string   `json:"id"`
	Pub     int64    `json:"pub"`
	Src     string   `json:"src"`
	SrcName string   `json:"srcName"`
	Title   string   `json:"title"`
	Syms    []string `json:"syms"`
	URL     string   `json:"url"`
}

// Event is a connection event in the same day file:
//
//	connected  the stream is up (URL is "wss" or "ws")
//	dropped    a connected stream was lost (Last = when the last frame came
//	           in, so a silently dead socket's gap starts there, not when we
//	           noticed; Err is scrubbed of the key)
//	alive      heartbeat every statsEvery while connected, so an unclean exit
//	           (crash, kill) bounds the unknown stretch to minutes
//	stopped    clean shutdown
type Event struct {
	Ev   string `json:"ev"`
	Ms   int64  `json:"ms"`
	URL  string `json:"url,omitempty"`
	Last int64  `json:"last,omitempty"`
	Err  string `json:"err,omitempty"`
}

// Event names.
const (
	EvConnected = "connected"
	EvDropped   = "dropped"
	EvAlive     = "alive"
	EvStopped   = "stopped"
)

const dayLayout = "2006-01-02"

// fileName is the day file for a UTC day ("2006-01-02").
func fileName(day string) string { return "newsfilter-" + day + ".jsonl" }

// jsonLine renders v as one JSON line without HTML escaping, so titles stay
// readable in the file.
func jsonLine(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return b.Bytes()
}

// recorder appends to one JSONL file per UTC day under dir. It keeps the
// current day's file open and rolls over when the timestamp of what it is
// asked to write crosses UTC midnight. Every write is a single Write call, so
// "flush per frame" holds without a buffer to lose on a crash.
type recorder struct {
	dir string

	mu  sync.Mutex
	day string
	f   *os.File
}

func (r *recorder) write(ms int64, b []byte) error {
	if len(b) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	day := time.UnixMilli(ms).UTC().Format(dayLayout)
	if r.f == nil || r.day != day {
		r.closeLocked()
		if err := os.MkdirAll(r.dir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(r.dir, fileName(day)), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		if endsMidLine(f) {
			// A crash or a full disk left half a line behind. Appending straight
			// after it would glue the next good line (often the connected event)
			// to the fragment and lose it too. This covers the reopen after a
			// failed write as well, which also goes through here.
			b = append([]byte{'\n'}, b...)
		}
		r.f, r.day = f, day
	}
	if _, err := r.f.Write(b); err != nil {
		r.closeLocked() // reopen next time instead of writing into a broken handle
		return err
	}
	return nil
}

// endsMidLine reports whether the file's last byte is not a newline: a torn
// write. An empty or unreadable file is not.
func endsMidLine(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], st.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}

func (r *recorder) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
}

func (r *recorder) closeLocked() {
	if r.f != nil {
		_ = r.f.Close()
		r.f, r.day = nil, ""
	}
}

// Raw-frame capture limits: the first real run exists to check the docs
// against reality, so a couple of hundred frames is plenty.
const (
	rawFrameCap = 200
	rawFrameMax = 64 << 10 // bytes of one frame kept
	rawFileMax  = 16 << 20 // hard ceiling on the file, escapes included
	rawFileName = "raw-frames.jsonl"
)

type rawLine struct {
	Ms    int64  `json:"ms"`
	N     int    `json:"n"`
	Len   int    `json:"len"`
	Trunc bool   `json:"trunc,omitempty"`
	Bin   bool   `json:"bin,omitempty"`
	Frame string `json:"frame,omitempty"`
}

// rawCapture keeps the first rawFrameCap interesting frames (anything that is
// not a bare engine ping/pong) in raw-frames.jsonl, truncated at every process
// start so it always describes the latest run.
type rawCapture struct {
	scrub func(string) string

	mu    sync.Mutex
	f     *os.File
	n     int
	bytes int
}

func (c *rawCapture) open(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	c.n, c.bytes = 0, 0
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, rawFileName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	c.f = f
	return nil
}

// add stores one frame. text is nil for a binary frame (only its size is kept).
func (c *rawCapture) add(ms int64, text []byte, size int, bin bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f == nil || c.n >= rawFrameCap || c.bytes >= rawFileMax {
		return
	}
	c.n++
	l := rawLine{Ms: ms, N: c.n, Len: size, Bin: bin}
	if !bin {
		s := c.scrub(string(text)) // scrub first: truncating first could leave half a key
		if len(s) > rawFrameMax {
			s, l.Trunc = clip(s, rawFrameMax), true
		}
		l.Frame = s
	}
	b := jsonLine(l)
	if n, err := c.f.Write(b); err == nil {
		c.bytes += n
	} else {
		c.closeLocked() // a broken capture file is not worth anything else
	}
}

func (c *rawCapture) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

func (c *rawCapture) closeLocked() {
	if c.f != nil {
		_ = c.f.Close()
		c.f = nil
	}
}

// idSet is a bounded first-seen set: a map for lookup plus a ring that evicts
// the oldest id once cap is reached.
type idSet struct {
	cap  int
	m    map[string]struct{}
	ring []string
	pos  int
}

func newIDSet(cap int) *idSet {
	return &idSet{cap: max(cap, 1), m: make(map[string]struct{})}
}

// add reports whether id was new.
func (s *idSet) add(id string) bool {
	if _, ok := s.m[id]; ok {
		return false
	}
	if len(s.ring) < s.cap {
		s.ring = append(s.ring, id)
	} else {
		delete(s.m, s.ring[s.pos])
		s.ring[s.pos] = id
		s.pos = (s.pos + 1) % s.cap
	}
	s.m[id] = struct{}{}
	return true
}

func (s *idSet) len() int { return len(s.m) }
