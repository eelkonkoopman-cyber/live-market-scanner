package newsfilter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Capture is what the recorder left on disk for a time window.
type Capture struct {
	Records []Record // first-seen articles with fromMs <= Ms <= toMs
	Events  []Event  // every connection event from the day before the window on
	Files   int      // day files read
	Bad     int      // lines that were not valid JSON or had neither an event nor an id
	Dupes   int      // repeat sightings of an id dropped from Records
}

// diskLine is the union of a Record and an Event, for reading either.
type diskLine struct {
	Record
	Ev   string `json:"ev"`
	Last int64  `json:"last"`
	Err  string `json:"err"`
}

// LoadDir reads the recorder's day files under dir. Articles are limited to the
// window; events are read from one day earlier as well, because whether the
// stream was connected at the start of the window depends on events before it.
// A missing directory is an error (the recorder never ran); a directory with
// no files in range is an empty Capture.
func LoadDir(dir string, fromMs, toMs int64) (Capture, error) {
	var c Capture
	if st, err := os.Stat(dir); err != nil {
		return c, fmt.Errorf("no Newsfilter recordings at %s: %w", dir, err)
	} else if !st.IsDir() {
		return c, fmt.Errorf("%s is not a directory", dir)
	}
	day := time.UnixMilli(fromMs).UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	last := time.UnixMilli(toMs).UTC().Truncate(24 * time.Hour)
	for ; !day.After(last); day = day.AddDate(0, 0, 1) {
		path := filepath.Join(dir, fileName(day.Format(dayLayout)))
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return c, err
		}
		c.Files++
		err = c.readFile(f, fromMs, toMs)
		f.Close()
		if err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	c.dedupe()
	return c, nil
}

// dedupe keeps each id's earliest sighting. The recorder's seen-set lasts one
// process, so a server that replays recent articles on connect would otherwise
// count a restart's replays as new, late arrivals.
func (c *Capture) dedupe() {
	first := make(map[string]int, len(c.Records))
	kept := c.Records[:0]
	for _, r := range c.Records {
		if i, ok := first[r.ID]; ok {
			c.Dupes++
			if r.Ms < kept[i].Ms {
				kept[i] = r
			}
			continue
		}
		first[r.ID] = len(kept)
		kept = append(kept, r)
	}
	c.Records = kept
}

func (c *Capture) readFile(r io.Reader, fromMs, toMs int64) error {
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			c.add(line, fromMs, toMs)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// add files one line; a torn or foreign line is counted and skipped.
func (c *Capture) add(line []byte, fromMs, toMs int64) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var d diskLine
	if err := json.Unmarshal(line, &d); err != nil {
		c.Bad++
		return
	}
	switch {
	case d.Ev != "":
		c.Events = append(c.Events, Event{Ev: d.Ev, Ms: d.Ms, URL: d.Record.URL, Last: d.Last, Err: d.Err})
	case d.ID != "":
		if d.Ms >= fromMs && d.Ms <= toMs {
			c.Records = append(c.Records, d.Record)
		}
	default:
		c.Bad++
	}
}
