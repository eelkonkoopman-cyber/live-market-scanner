// Command genicon procedurally draws the Live Market Scanner application icon
// (a dark rounded square with three rising gold candlesticks) and writes it as
// a multi-size Windows .ico (16, 24, 32, 48 and 256 px, 32-bit BMP/DIB entries).
//
// Usage (from the repository root):
//
//	go run ./tools/genicon            # writes assets/scanner.ico
//	go run ./tools/genicon -o out.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

var sizes = []int{16, 24, 32, 48, 256}

// Palette (AfterBell gold on the dark theme background).
var (
	bgCol   = [3]float64{0x0e, 0x0e, 0x10}
	goldCol = [3]float64{0xd4, 0xa6, 0x53}
)

const (
	cornerRadius = 0.22 // fraction of the icon size
	ss           = 8    // supersampling grid per pixel edge
)

// rect in unit coordinates (0..1, y down).
type rect struct{ x0, y0, x1, y1 float64 }

func (r rect) has(x, y float64) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

// candles returns the gold shapes for a given pixel size.
func candles(size int) []rect {
	const bodyW = 0.17
	centers := []float64{0.25, 0.5, 0.75}
	// Bodies rise left to right and grow taller: short, medium, tall.
	bodies := [][2]float64{{0.58, 0.72}, {0.40, 0.62}, {0.20, 0.52}}
	var out []rect
	for i, cx := range centers {
		b := bodies[i]
		out = append(out, rect{cx - bodyW/2, b[0], cx + bodyW/2, b[1]})
		if size >= 24 { // wicks are noise at 16 px
			w := 0.035
			if size >= 48 {
				w = 0.03
			}
			out = append(out, rect{cx - w/2, b[0] - 0.08, cx + w/2, b[1] + 0.08})
		}
	}
	return out
}

// inRoundedSquare reports whether unit point (x,y) lies inside the rounded square.
func inRoundedSquare(x, y float64) bool {
	r := cornerRadius
	cx, cy := x, y
	switch {
	case x < r:
		cx = r
	case x > 1-r:
		cx = 1 - r
	}
	switch {
	case y < r:
		cy = r
	case y > 1-r:
		cy = 1 - r
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// render draws one size and returns straight-alpha BGRA rows, top-down.
func render(size int) []byte {
	shapes := candles(size)
	pix := make([]byte, size*size*4)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var inside, gold int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if !inRoundedSquare(x, y) {
						continue
					}
					inside++
					for _, s := range shapes {
						if s.has(x, y) {
							gold++
							break
						}
					}
				}
			}
			o := (py*size + px) * 4
			if inside == 0 {
				continue // fully transparent
			}
			g := float64(gold) / float64(inside)
			for c := 0; c < 3; c++ {
				v := bgCol[c]*(1-g) + goldCol[c]*g
				pix[o+2-c] = byte(math.Round(v)) // RGB -> BGR
			}
			pix[o+3] = byte(math.Round(255 * float64(inside) / float64(ss*ss)))
		}
	}
	return pix
}

// dib encodes one ICO image entry: BITMAPINFOHEADER (height = 2*size), bottom-up
// BGRA pixel rows, then a 1-bpp AND mask of zeros padded to 32-bit rows.
func dib(size int) []byte {
	pix := render(size)
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))              // biSize
	le(int32(size))             // biWidth
	le(int32(size * 2))         // biHeight (image + mask)
	le(uint16(1))               // biPlanes
	le(uint16(32))              // biBitCount
	le(uint32(0))               // biCompression = BI_RGB
	le(uint32(size * size * 4)) // biSizeImage
	le(int32(0))                // biXPelsPerMeter
	le(int32(0))                // biYPelsPerMeter
	le(uint32(0))               // biClrUsed
	le(uint32(0))               // biClrImportant
	for y := size - 1; y >= 0; y-- {
		b.Write(pix[y*size*4 : (y+1)*size*4])
	}
	maskRow := ((size + 31) / 32) * 4
	b.Write(make([]byte, maskRow*size))
	return b.Bytes()
}

func buildICO() []byte {
	imgs := make([][]byte, len(sizes))
	for i, s := range sizes {
		imgs[i] = dib(s)
	}
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0))            // reserved
	le(uint16(1))            // type = icon
	le(uint16(len(sizes)))   // count
	off := 6 + 16*len(sizes) // first image offset
	for i, s := range sizes {
		dim := byte(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		b.WriteByte(dim) // width
		b.WriteByte(dim) // height
		b.WriteByte(0)   // palette colours
		b.WriteByte(0)   // reserved
		le(uint16(1))    // planes
		le(uint16(32))   // bit count
		le(uint32(len(imgs[i])))
		le(uint32(off))
		off += len(imgs[i])
	}
	for _, im := range imgs {
		b.Write(im)
	}
	return b.Bytes()
}

func main() {
	out := flag.String("o", filepath.Join("assets", "scanner.ico"), "output .ico path")
	flag.Parse()
	data := buildICO()
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes, sizes %v)\n", *out, len(data), sizes)
}
