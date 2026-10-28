package feed

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"testing"
)

// genSyms: ~11k distinct symbols (A..ZZZZ style).
func genSyms(n int, r *rand.Rand) []string {
	seen := map[string]bool{}
	out := make([]string, 0, n)
	const L = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for len(out) < n {
		k := 1 + r.Intn(5)
		b := make([]byte, k)
		for i := range b {
			b[i] = L[r.Intn(26)]
		}
		s := string(b)
		if r.Intn(200) == 0 {
			s += ".WS"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func fmtPx(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		return strconv.FormatFloat(float64(r.Intn(100000))/10000, 'f', -1, 64) // sub-$10, 4dp
	case 1:
		return strconv.FormatFloat(float64(r.Intn(5000000))/100, 'f', -1, 64)
	case 2:
		return strconv.Itoa(1 + r.Intn(500)) // integral price
	default:
		return strconv.FormatFloat(r.Float64()*300, 'f', 4, 64)
	}
}

// genFrame builds a realistic A.* frame with n aggregates.
func genFrame(n int, syms []string, r *rand.Rand) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	sec := int64(1761312000000)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		sym := syms[r.Intn(len(syms))]
		fmt.Fprintf(&b, `{"ev":"A","sym":"%s","v":%d,"av":%d,"op":%s,"vw":%s,"o":%s,"c":%s,"h":%s,"l":%s,"a":%s,"z":%d,"s":%d,"e":%d`,
			sym, 1+r.Intn(5000), r.Intn(50000000), fmtPx(r), fmtPx(r), fmtPx(r), fmtPx(r), fmtPx(r), fmtPx(r), fmtPx(r),
			1+r.Intn(300), sec, sec+1000)
		if r.Intn(10) == 0 {
			b.WriteString(`,"otc":true`)
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.Bytes()
}

func eqDecode(t testing.TB, data []byte) {
	t.Helper()
	d := newFrameDecoder()
	a1, s1, t1 := decodeFrame(data)
	a2, s2, t2 := d.decode(data)
	// nil vs empty: both paths produce nil when nothing is appended.
	if !reflect.DeepEqual(a1, a2) || !reflect.DeepEqual(s1, s2) || !reflect.DeepEqual(t1, t2) {
		t.Fatalf("mismatch on %q\nslow: %+v %+v %+v\nfast: %+v %+v %+v", data, a1, s1, t1, a2, s2, t2)
	}
}

func TestFastDecodeEdgeCases(t *testing.T) {
	cases := []string{
		`[]`, ` [ ] `, `[`, `]`, `{}`, `null`, `[null]`, `[1]`, `["x"]`, `[{}]`, `[{},{}]`,
		`{"ev":"status","status":"auth_success"}`,
		`[{"ev":"status","status":"success","message":"subscribed to: A.*"}]`,
		`[{"ev":"A","sym":"AAPL","v":100,"av":2000,"op":150.0,"vw":150.5,"o":150.1,"c":150.7,"h":151.0,"l":149.9,"a":150.4,"z":50,"s":1700000000000,"e":1700000001000,"otc":false}]`,
		`[{"ev":"A","sym":"AAPL","v":100.0}]`, `[{"ev":"A","sym":"AAPL","v":1e3}]`, `[{"ev":"A","sym":"AAPL","c":1e3}]`,
		`[{"ev":"A","sym":"AAPL","v":null,"c":null,"otc":null,"sym":null}]`, `[{"ev":"A","sym":null}]`,
		`[{"ev":"A","SYM":"AAPL"}]`, `[{"EV":"A","sym":"AAPL"}]`, `[{"ev":"a","sym":"AAPL"}]`,
		`[{"ev":"A","sym":"AAPL","sym":"MSFT"}]`, `[{"ev":"A","ev":"T"}]`,
		`[{"ev":"A","sym":"AAPL"}]`, `[{"ev":"A","sym":"AAPL\n"}]`, "[{\"ev\":\"A\",\"sym\":\"AA\x01\"}]",
		`[{"ev":"A","sym":123}]`, `[{"ev":1}]`, `[{"ev":"A","otc":1}]`, `[{"ev":"A","otc":"true"}]`,
		`[{"ev":"A","v":99999999999999999999}]`, `[{"ev":"A","c":1e400}]`, `[{"ev":"A","c":-0}]`, `[{"ev":"A","c":-0.0e-0}]`,
		`[{"ev":"A","c":01}]`, `[{"ev":"A","c":.5}]`, `[{"ev":"A","c":5.}]`, `[{"ev":"A","c":+5}]`, `[{"ev":"A","c":-}]`,
		`[{"ev":"A","x":[1,{"y":"\"\\"}],"c":2}]`, `[{"ev":"A","x":{"a":[[[]]]}}]`, `[{"ev":"A","c":2},]`, `[{"ev":"A","c":2}] x`,
		`[{"ev":"A","c":2} {"ev":"A"}]`, `[{"ev":"A","c":2,}]`, `[{"ev":"A" "c":2}]`, `[{"ev":"A","sym":"é"}]`,
		`[{"ev":"Q","sym":"ABCD","bx":4,"bp":4.50,"bs":12,"ax":7,"ap":4.52,"as":30,"t":1700000000000,"q":50000},{"ev":"T","sym":"ABCD","x":4,"i":"77","z":3,"p":4.52,"s":1500,"c":[0,12],"t":1700000000100,"q":3663},{"ev":"A","sym":"ABCD","v":1500,"c":4.52,"s":1700000000000,"e":1700000001000}]`,
		`[{"ev":"T","sym":"X","c":"bad"},{"ev":"A","sym":"Y","c":1}]`,
		`[{"ev":"A","sym":"Y","c":1,"x":"\u00zz"}]`, `[{"ev":"A","sym":"Y","x":tru}]`, `[{"ev":"A","sym":"Y","x":nul}]`,
		"\t[ {\"ev\" : \"A\" , \"sym\" : \"Y\" , \"c\" : 1 } ]\r\n",
	}
	for _, c := range cases {
		eqDecode(t, []byte(c))
	}
}

// TestFastDecodeRandomMutations compares the fast path with decodeFrame on
// randomly corrupted frames: 10k by default (a few seconds), more with
// FASTDECODE_ITERS (200000 were run when the decoder was introduced).
func TestFastDecodeRandomMutations(t *testing.T) {
	iters := 10000
	if v, err := strconv.Atoi(os.Getenv("FASTDECODE_ITERS")); err == nil && v > 0 {
		iters = v
	}
	r := rand.New(rand.NewSource(1))
	syms := genSyms(500, r)
	alphabet := []byte(`{}[],:"\ .-+eE0123456789aAnultrfsvx` + "\t\n\x01\xc3")
	for it := 0; it < iters; it++ {
		f := genFrame(1+r.Intn(4), syms, r)
		// mutate a few bytes
		for k := r.Intn(4); k > 0; k-- {
			p := r.Intn(len(f))
			switch r.Intn(3) {
			case 0:
				f[p] = alphabet[r.Intn(len(alphabet))]
			case 1:
				f = append(f[:p], f[p+1:]...)
			case 2:
				f = append(f[:p], append([]byte{alphabet[r.Intn(len(alphabet))]}, f[p:]...)...)
			}
			if len(f) == 0 {
				break
			}
		}
		eqDecode(t, f)
	}
}

func FuzzFastDecode(f *testing.F) {
	r := rand.New(rand.NewSource(2))
	syms := genSyms(50, r)
	f.Add(genFrame(3, syms, r))
	f.Add([]byte(`[{"ev":"T","sym":"ABCD","x":4,"i":"77","z":3,"p":4.52,"s":1500,"c":[0,12],"t":1700000000100,"q":3663}]`))
	f.Fuzz(func(t *testing.T, data []byte) { eqDecode(t, data) })
}

func TestFastDecodeLargeFrames(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	syms := genSyms(11000, r)
	for _, n := range []int{1, 10, 100, 1000, 5000} {
		eqDecode(t, genFrame(n, syms, r))
	}
}

var sink int

func benchFrames(n int) [][]byte {
	r := rand.New(rand.NewSource(4))
	syms := genSyms(11000, r)
	out := make([][]byte, 16)
	for i := range out {
		out[i] = genFrame(n, syms, r)
	}
	return out
}

func BenchmarkDecodeSlow(b *testing.B) {
	for _, n := range []int{50, 500, 2000} {
		frames := benchFrames(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			var bytesTot int
			for i := 0; i < b.N; i++ {
				f := frames[i%len(frames)]
				bytesTot += len(f)
				a, _, _ := decodeFrame(f)
				sink += len(a)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/agg")
			b.SetBytes(int64(bytesTot / b.N))
		})
	}
}

func BenchmarkDecodeFast(b *testing.B) {
	for _, n := range []int{50, 500, 2000} {
		frames := benchFrames(n)
		d := newFrameDecoder()
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			var bytesTot int
			for i := 0; i < b.N; i++ {
				f := frames[i%len(frames)]
				bytesTot += len(f)
				a, _, _ := d.decode(f)
				sink += len(a)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/agg")
			b.SetBytes(int64(bytesTot / b.N))
		})
	}
}

// ReadAll (what websocket.Conn.Read does) vs reading into a reused buffer.
func BenchmarkReadAll(b *testing.B) {
	for _, n := range []int{50, 500, 2000} {
		f := benchFrames(n)[0]
		b.Run("ioReadAll/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buf, _ := io.ReadAll(bytes.NewReader(f))
				sink += len(buf)
			}
		})
		b.Run("reusedBuffer/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			var bb bytes.Buffer
			for i := 0; i < b.N; i++ {
				bb.Reset()
				_, _ = bb.ReadFrom(bytes.NewReader(f))
				sink += bb.Len()
			}
		})
	}
}
