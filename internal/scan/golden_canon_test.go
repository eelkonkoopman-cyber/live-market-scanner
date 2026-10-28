package scan

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// Canonical encoding for the golden-equivalence test (golden_test.go).
//
// Every value the scanner emits is rendered to one canonical JSON text: object
// keys sorted, numbers in the shortest form that round-trips (so two floats
// print alike exactly when their bits do, except NaN, whose payload nothing can
// observe: IsNull is the only test), NaN / +-Inf as the strings "$NaN" /
// "$+Inf" / "$-Inf" (encoding/json refuses them), nil pointers as null. int and
// float64 print the same for the same number on purpose: they are the same
// token on the wire and the one in-process reader of an alert row (daylog.jnum)
// accepts both, so an optimisation may change a value's Go type without that
// counting as a behaviour change.
//
// Structs go through reflection over ALL their fields (declaration order), so a
// field added to Row / ScanTick / AlertMsg / ScanEntry later changes the golden
// instead of being silently ignored.

// goldNum appends a float64.
func goldNum(b []byte, v float64) []byte {
	switch {
	case math.IsNaN(v):
		return append(b, `"$NaN"`...)
	case math.IsInf(v, 1):
		return append(b, `"$+Inf"`...)
	case math.IsInf(v, -1):
		return append(b, `"$-Inf"`...)
	}
	f := byte('f')
	if a := math.Abs(v); a != 0 && (a < 1e-6 || a >= 1e21) {
		f = 'e'
	}
	return strconv.AppendFloat(b, v, f, -1, 64)
}

// goldStr appends a JSON string.
func goldStr(b []byte, s string) []byte {
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		b = append(b, '"')
		b = append(b, s...)
		return append(b, '"')
	}
	q, _ := json.Marshal(s)
	return append(b, q...)
}

// goldCanon appends the canonical text of v.
func goldCanon(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		return strconv.AppendBool(b, x)
	case float64:
		return goldNum(b, x)
	case int:
		return strconv.AppendInt(b, int64(x), 10)
	case int64:
		return strconv.AppendInt(b, x, 10)
	case string:
		return goldStr(b, x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = goldStr(b, k)
			b = append(b, ':')
			b = goldCanon(b, x[k])
		}
		return append(b, '}')
	case map[string]map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = goldStr(b, k)
			b = append(b, ':')
			b = goldCanon(b, x[k])
		}
		return append(b, '}')
	}
	return goldReflect(b, reflect.ValueOf(v))
}

func goldReflect(b []byte, v reflect.Value) []byte {
	switch v.Kind() {
	case reflect.Invalid:
		return append(b, "null"...)
	case reflect.Bool:
		return strconv.AppendBool(b, v.Bool())
	case reflect.Float32, reflect.Float64:
		return goldNum(b, v.Float())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(b, v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.AppendUint(b, v.Uint(), 10)
	case reflect.String:
		return goldStr(b, v.String())
	case reflect.Interface:
		if v.IsNil() {
			return append(b, "null"...)
		}
		return goldCanon(b, v.Interface())
	case reflect.Pointer:
		if v.IsNil() {
			return append(b, "null"...)
		}
		return goldReflect(b, v.Elem())
	case reflect.Slice, reflect.Array:
		b = append(b, '[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = goldReflect(b, v.Index(i))
		}
		return append(b, ']')
	case reflect.Map:
		switch m := v.Interface().(type) {
		case map[string]any:
			return goldCanon(b, m)
		case map[string]map[string]any:
			return goldCanon(b, m)
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = goldStr(b, k.String())
			b = append(b, ':')
			b = goldReflect(b, v.MapIndex(k))
		}
		return append(b, '}')
	case reflect.Struct:
		t := v.Type()
		b = append(b, '{')
		for i := 0; i < v.NumField(); i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = goldStr(b, t.Field(i).Name)
			b = append(b, ':')
			b = goldReflect(b, v.Field(i))
		}
		return append(b, '}')
	}
	panic(fmt.Sprintf("golden: no canonical form for %s: extend goldReflect", v.Type()))
}

// goldArr joins canonical items into one JSON array.
func goldArr(items ...[]byte) []byte {
	b := []byte{'['}
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	return append(b, ']')
}

// goldRowText is a core.Row canonicalised over every field. Hits is zeroed: it
// is a per-scan overlay stamped onto the shared snapshot row by whichever scan
// evaluated LAST, and scans run in map order, so on the snapshot's own rows it
// is random (and not part of the symbol's market data).
func goldRowText(b []byte, r core.Row) []byte {
	r.Hits = 0
	return goldReflect(b, reflect.ValueOf(r))
}

// goldRec is one canonical item of a step.
type goldRec struct {
	cat  string // tick | alert | entry | counts | misc
	key  string // the scan id / symbol / probe the item belongs to
	text []byte
}

// goldCats is the order categories are hashed and printed in.
var goldCats = [...]string{"tick", "alert", "entry", "counts", "misc"}

type goldStepSum struct {
	n    [len(goldCats)]int
	cat  [len(goldCats)][32]byte
	all  [32]byte
	rows int // rows carried by the step's ticks
}

// goldSummarise hashes a step's records (already in canonical order) per
// category and overall.
func goldSummarise(recs []goldRec) goldStepSum {
	var s goldStepSum
	whole := sha256.New()
	for ci, cat := range goldCats {
		h := sha256.New()
		for i := range recs {
			if recs[i].cat != cat {
				continue
			}
			s.n[ci]++
			for _, w := range [...][]byte{[]byte(recs[i].cat), {0}, []byte(recs[i].key), {0}, recs[i].text, {'\n'}} {
				h.Write(w)
				whole.Write(w)
			}
		}
		h.Sum(s.cat[ci][:0])
	}
	whole.Sum(s.all[:0])
	return s
}

// goldLine is the step's line in golden_scan.txt.
func goldLine(step int, asOf int64, s goldStepSum) string {
	var cat string
	for ci, name := range goldCats {
		cat += fmt.Sprintf(" %s=%d:%s", name, s.n[ci], hex.EncodeToString(s.cat[ci][:4]))
	}
	return fmt.Sprintf("%04d asOf=%d rows=%d%s all=%s", step, asOf, s.rows, cat, hex.EncodeToString(s.all[:]))
}

// goldDump writes every canonical item as a JSON line when GOLDEN_DUMP names a
// directory: {"step":N,"cat":...,"key":...,"v":<canonical>}. Run the test on
// the old and the new code with different directories and diff the two files
// to see what changed at the step the golden reports.
type goldDump struct {
	f *os.File
	w *bufio.Writer
}

func goldOpenDump() *goldDump {
	dir := os.Getenv("GOLDEN_DUMP")
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create(filepath.Join(dir, "scan_steps.jsonl"))
	if err != nil {
		panic(err)
	}
	return &goldDump{f: f, w: bufio.NewWriterSize(f, 1<<20)}
}

func (d *goldDump) step(step int, recs []goldRec) {
	if d == nil {
		return
	}
	var line []byte
	for i := range recs {
		line = append(line[:0], `{"step":`...)
		line = strconv.AppendInt(line, int64(step), 10)
		line = append(line, `,"cat":"`...)
		line = append(line, recs[i].cat...)
		line = append(line, `","key":`...)
		line = goldStr(line, recs[i].key)
		line = append(line, `,"v":`...)
		line = append(line, recs[i].text...)
		line = append(line, "}\n"...)
		d.w.Write(line)
	}
}

func (d *goldDump) close() {
	if d == nil {
		return
	}
	d.w.Flush()
	d.f.Close()
}
