package feed

// frameDecoder is decodeFrame's fast path for the market-hours firehose: a
// single pass over the frame for the fixed A.* schema (encoding/json costs a
// dozen allocations and several passes per aggregate). Contract: when fast()
// returns ok, its output is identical (reflect.DeepEqual) to decodeFrame(data);
// whenever it meets anything it does not fully understand it returns ok=false
// and decode runs decodeFrame on the WHOLE frame. One per connection: owned by
// its read goroutine.

import (
	"encoding/json"
	"strconv"
	"unsafe"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

type frameDecoder struct {
	syms map[string]string // intern table; owned by the single read goroutine
}

const maxInterned = 1 << 16

func newFrameDecoder() *frameDecoder { return &frameDecoder{syms: make(map[string]string, 16384)} }

func (d *frameDecoder) intern(b []byte) string {
	if s, ok := d.syms[string(b)]; ok {
		return s
	}
	s := string(b)
	if len(d.syms) < maxInterned {
		d.syms[s] = s
	}
	return s
}

func (d *frameDecoder) decode(data []byte) ([]core.Agg, []statusMsg, []core.TapeEvent) {
	if aggs, sts, tape, ok := d.fast(data); ok {
		return aggs, sts, tape
	}
	return decodeFrame(data)
}

// span is a value's byte range in the frame; kind is its first byte.
type span struct{ s, e int32 }

// A-schema key slots.
const (
	kEv = iota
	kSym
	kV
	kAV
	kOP
	kVW
	kO
	kC
	kH
	kL
	kA
	kZ
	kS
	kE
	kOTC
	nKeys
)

func keySlot(k []byte) int {
	switch string(k) { // no allocation: compiler special-cases switch on string(bytes)
	case "ev":
		return kEv
	case "sym":
		return kSym
	case "v":
		return kV
	case "av":
		return kAV
	case "op":
		return kOP
	case "vw":
		return kVW
	case "o":
		return kO
	case "c":
		return kC
	case "h":
		return kH
	case "l":
		return kL
	case "a":
		return kA
	case "z":
		return kZ
	case "s":
		return kS
	case "e":
		return kE
	case "otc":
		return kOTC
	}
	return -1
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipWS(b []byte, i int) int {
	for i < len(b) && isWS(b[i]) {
		i++
	}
	return i
}

func bstr(b []byte) string { return unsafe.String(unsafe.SliceData(b), len(b)) }

func (d *frameDecoder) fast(b []byte) (aggs []core.Agg, sts []statusMsg, tape []core.TapeEvent, ok bool) {
	if len(b) > 1<<30 {
		return nil, nil, nil, false
	}
	i := skipWS(b, 0)
	if i >= len(b) || b[i] != '[' {
		return nil, nil, nil, false
	}
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == ']' {
		return nil, nil, nil, skipWS(b, i+1) == len(b)
	}
	for {
		if i >= len(b) || b[i] != '{' {
			return nil, nil, nil, false // non-object element: let the slow path decide
		}
		start := i
		var sp [nKeys]span
		var seen uint32
		var good bool
		i, seen, good = scanTopObject(b, i, &sp)
		if !good {
			return nil, nil, nil, false
		}
		elem := b[start:i]
		if seen&(1<<kEv) != 0 {
			evb := b[sp[kEv].s:sp[kEv].e]
			// ev must be a plain string to be classified here; null / other
			// types / escapes go to the slow path for exactness.
			if len(evb) < 2 || evb[0] != '"' || !plainString(evb) {
				return nil, nil, nil, false
			}
			switch bstr(evb[1 : len(evb)-1]) {
			case "A":
				a, ok2 := d.aggFrom(b, &sp, seen)
				if !ok2 {
					return nil, nil, nil, false
				}
				if aggs == nil {
					aggs = make([]core.Agg, 0, len(b)/150+1)
				}
				aggs = append(aggs, a)
			case "T":
				var t tradeMsg
				if json.Unmarshal(elem, &t) == nil {
					tape = append(tape, core.TapeEvent{Trade: &core.Trade{
						Sym: t.Sym, Price: t.Price, Size: t.Size, TS: t.TS, Conds: t.Conds,
					}})
				}
			case "Q":
				var q quoteMsg
				if json.Unmarshal(elem, &q) == nil {
					tape = append(tape, core.TapeEvent{Quote: &core.Quote{
						Sym: q.Sym, Bid: q.Bid, Ask: q.Ask, TS: q.TS,
						BidSz: q.BidLots * roundLot, AskSz: q.AskLots * roundLot,
					}})
				}
			case "status":
				var s statusMsg
				if json.Unmarshal(elem, &s) == nil {
					sts = append(sts, s)
				}
			}
		}
		i = skipWS(b, i)
		if i >= len(b) {
			return nil, nil, nil, false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
			continue
		case ']':
			if skipWS(b, i+1) != len(b) {
				return nil, nil, nil, false
			}
			return aggs, sts, tape, true
		default:
			return nil, nil, nil, false
		}
	}
}

// plainString: a quoted token with no escapes and only printable ASCII.
func plainString(tok []byte) bool {
	for _, c := range tok[1 : len(tok)-1] {
		if c == '\\' || c >= 0x80 || c < 0x20 {
			return false
		}
	}
	return true
}

// scanTopObject validates one top-level object, recording the value spans of
// the A-schema keys. Any key that is not plain lowercase ASCII (encoding/json
// matches keys case-insensitively) or any duplicate known key bails.
func scanTopObject(b []byte, i int, sp *[nKeys]span) (int, uint32, bool) {
	var seen uint32
	i = skipWS(b, i+1) // past '{'
	if i < len(b) && b[i] == '}' {
		return i + 1, 0, true
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return 0, 0, false
		}
		ks := i + 1
		j := ks
		for j < len(b) && b[j] != '"' {
			c := b[j]
			if c == '\\' || c < 0x20 || c >= 0x80 || (c >= 'A' && c <= 'Z') {
				return 0, 0, false
			}
			j++
		}
		if j >= len(b) {
			return 0, 0, false
		}
		slot := keySlot(b[ks:j])
		i = skipWS(b, j+1)
		if i >= len(b) || b[i] != ':' {
			return 0, 0, false
		}
		i = skipWS(b, i+1)
		vs := i
		var good bool
		i, good = skipValue(b, i, 0)
		if !good {
			return 0, 0, false
		}
		if slot >= 0 {
			if seen&(1<<slot) != 0 {
				return 0, 0, false // duplicate: last-wins semantics, leave to slow path
			}
			seen |= 1 << slot
			sp[slot] = span{int32(vs), int32(i)}
		}
		i = skipWS(b, i)
		if i >= len(b) {
			return 0, 0, false
		}
		if b[i] == ',' {
			i = skipWS(b, i+1)
			continue
		}
		if b[i] == '}' {
			return i + 1, seen, true
		}
		return 0, 0, false
	}
}

const maxDepth = 64

// skipValue validates and skips one JSON value (RFC 8259 grammar, as strict as
// encoding/json's checkValid; deeper nesting than maxDepth bails).
func skipValue(b []byte, i, depth int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch c := b[i]; {
	case c == '"':
		i++
		for i < len(b) {
			c := b[i]
			switch {
			case c == '"':
				return i + 1, true
			case c == '\\':
				i++
				if i >= len(b) {
					return 0, false
				}
				switch b[i] {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					i++
				case 'u':
					if i+4 >= len(b) {
						return 0, false
					}
					for k := 1; k <= 4; k++ {
						if !isHex(b[i+k]) {
							return 0, false
						}
					}
					i += 5
				default:
					return 0, false
				}
			case c < 0x20:
				return 0, false
			default:
				i++
			}
		}
		return 0, false
	case c == '-' || (c >= '0' && c <= '9'):
		return skipNumber(b, i)
	case c == '{':
		if depth >= maxDepth {
			return 0, false
		}
		i = skipWS(b, i+1)
		if i < len(b) && b[i] == '}' {
			return i + 1, true
		}
		for {
			if i >= len(b) || b[i] != '"' {
				return 0, false
			}
			var good bool
			if i, good = skipValue(b, i, depth+1); !good { // key: a string
				return 0, false
			}
			i = skipWS(b, i)
			if i >= len(b) || b[i] != ':' {
				return 0, false
			}
			if i, good = skipValue(b, skipWS(b, i+1), depth+1); !good {
				return 0, false
			}
			i = skipWS(b, i)
			if i >= len(b) {
				return 0, false
			}
			if b[i] == ',' {
				i = skipWS(b, i+1)
				continue
			}
			if b[i] == '}' {
				return i + 1, true
			}
			return 0, false
		}
	case c == '[':
		if depth >= maxDepth {
			return 0, false
		}
		i = skipWS(b, i+1)
		if i < len(b) && b[i] == ']' {
			return i + 1, true
		}
		for {
			var good bool
			if i, good = skipValue(b, i, depth+1); !good {
				return 0, false
			}
			i = skipWS(b, i)
			if i >= len(b) {
				return 0, false
			}
			if b[i] == ',' {
				i = skipWS(b, i+1)
				continue
			}
			if b[i] == ']' {
				return i + 1, true
			}
			return 0, false
		}
	case c == 't':
		return lit(b, i, "true")
	case c == 'f':
		return lit(b, i, "false")
	case c == 'n':
		return lit(b, i, "null")
	}
	return 0, false
}

func lit(b []byte, i int, w string) (int, bool) {
	if len(b)-i >= len(w) && string(b[i:i+len(w)]) == w {
		return i + len(w), true
	}
	return 0, false
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// skipNumber: -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func skipNumber(b []byte, i int) (int, bool) {
	if b[i] == '-' {
		i++
		if i >= len(b) {
			return 0, false
		}
	}
	switch {
	case b[i] == '0':
		i++
	case b[i] >= '1' && b[i] <= '9':
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	default:
		return 0, false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || !isDigit(b[i]) {
			return 0, false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || !isDigit(b[i]) {
			return 0, false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	return i, true
}

// aggFrom interprets the recorded spans with encoding/json's typing rules for
// aggMsg; any case where encoding/json would error (or behave subtly) bails.
func (d *frameDecoder) aggFrom(b []byte, sp *[nKeys]span, seen uint32) (core.Agg, bool) {
	var a core.Agg
	tok := func(k int) []byte { return b[sp[k].s:sp[k].e] }
	isNull := func(t []byte) bool { return len(t) == 4 && t[0] == 'n' }
	if seen&(1<<kSym) != 0 {
		t := tok(kSym)
		switch {
		case isNull(t):
		case t[0] == '"' && plainString(t):
			a.Sym = d.intern(t[1 : len(t)-1])
		default:
			return a, false
		}
	}
	ints := [...]struct {
		k   int
		dst *int64
	}{{kV, &a.Vol}, {kAV, &a.AccVol}, {kS, &a.StartMs}, {kE, &a.EndMs}, {kZ, nil}}
	for _, f := range ints {
		if seen&(1<<f.k) == 0 {
			continue
		}
		t := tok(f.k)
		if isNull(t) {
			continue
		}
		if t[0] != '-' && !isDigit(t[0]) {
			return a, false
		}
		for _, c := range t {
			if c == '.' || c == 'e' || c == 'E' {
				return a, false // encoding/json: UnmarshalTypeError -> agg dropped; let slow path do it
			}
		}
		n, err := strconv.ParseInt(bstr(t), 10, 64)
		if err != nil {
			return a, false
		}
		if f.dst != nil {
			*f.dst = n
		}
	}
	floats := [...]struct {
		k   int
		dst *float64
	}{{kOP, &a.DayOpen}, {kVW, &a.VWAP}, {kO, &a.Open}, {kC, &a.Close}, {kH, &a.High}, {kL, &a.Low}, {kA, &a.DayVWAP}}
	for _, f := range floats {
		if seen&(1<<f.k) == 0 {
			continue
		}
		t := tok(f.k)
		if isNull(t) {
			continue
		}
		if t[0] != '-' && !isDigit(t[0]) {
			return a, false
		}
		v, err := strconv.ParseFloat(bstr(t), 64)
		if err != nil {
			return a, false
		}
		*f.dst = v
	}
	if seen&(1<<kOTC) != 0 {
		switch t := tok(kOTC); bstr(t) {
		case "true":
			a.OTC = true
		case "false", "null":
		default:
			return a, false
		}
	}
	return a, true
}
