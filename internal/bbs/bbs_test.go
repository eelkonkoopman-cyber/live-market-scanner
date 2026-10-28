package bbs

import (
	"testing"
)

func TestAnalyzeAAOIShape(t *testing.T) {
	// touch, bounce 119.9, touch, 117.8, touch, 117.4, touch, 116.8
	S := 115.85
	var hs, ls []float64
	add := func(h, l float64, n int) {
		for i := 0; i < n; i++ {
			hs, ls = append(hs, h), append(ls, l)
		}
	}
	add(121, 120, 5)
	for _, top := range []float64{119.9, 117.8, 117.4, 116.8} {
		add(116.1, S, 1)
		add(top, 116.6, 4)
	}
	pt := Analyze(hs, ls, DefaultParams())
	if !pt.OK || pt.Support != S || pt.Touches != 4 || pt.Highs != 4 || pt.FirstHigh != 119.9 || pt.LastHigh != 116.8 {
		t.Fatalf("%+v", pt)
	}
	if !Breaks(pt, 115.4, DefaultParams()) || Breaks(pt, 115.6, DefaultParams()) {
		t.Fatal("break level")
	}
	// equal highs: no lower-high run
	for i := range hs {
		if hs[i] < 120 && hs[i] > 116.5 {
			hs[i] = 118
		}
	}
	if pt := Analyze(hs, ls, DefaultParams()); pt.OK {
		t.Fatalf("flat highs OK: %+v", pt)
	}
}

func TestAnalyzeAllocatesNothing(t *testing.T) {
	hs := make([]float64, 90)
	ls := make([]float64, 90)
	for i := range hs {
		hs[i], ls[i] = 10+float64(i%7)*0.1, 10
	}
	p := DefaultParams()
	if a := testing.AllocsPerRun(100, func() { Analyze(hs, ls, p) }); a != 0 {
		t.Fatalf("%v allocs", a)
	}
}

func BenchmarkAnalyze90(b *testing.B) {
	hs := make([]float64, 90)
	ls := make([]float64, 90)
	for i := range hs {
		hs[i], ls[i] = 10+float64(i%7)*0.1, 10-float64(i%5)*0.01
	}
	p := DefaultParams()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Analyze(hs, ls, p)
	}
}
