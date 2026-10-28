package tray

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestTipUTF16Truncates(t *testing.T) {
	if got := tipUTF16("Live Market Scanner"); string(utf16.Decode(got)) != "Live Market Scanner" {
		t.Errorf("short tip mangled: %q", utf16.Decode(got))
	}
	if got := tipUTF16(strings.Repeat("a", 500)); len(got) != maxTipUnits {
		t.Errorf("len = %d, want %d", len(got), maxTipUnits)
	}
	// 126 ASCII units + one astral rune (2 units) would straddle the limit.
	got := tipUTF16(strings.Repeat("a", 126) + "\U0001F600")
	if len(got) != 126 {
		t.Errorf("surrogate pair split: len = %d, want 126", len(got))
	}
}

func TestDebouncer(t *testing.T) {
	d := debouncer{window: 500 * time.Millisecond}
	t0 := time.Unix(1000, 0)
	if !d.allow(t0) {
		t.Fatal("first event must fire")
	}
	if d.allow(t0.Add(100 * time.Millisecond)) {
		t.Error("event inside window must be suppressed")
	}
	if !d.allow(t0.Add(600 * time.Millisecond)) {
		t.Error("event after window must fire")
	}
}

func fakeICO(sizes ...int) []byte {
	hdr := make([]byte, 6+16*len(sizes))
	binary.LittleEndian.PutUint16(hdr[2:], 1)
	binary.LittleEndian.PutUint16(hdr[4:], uint16(len(sizes)))
	var body []byte
	off := len(hdr)
	for i, s := range sizes {
		e := 6 + 16*i
		if s < 256 {
			hdr[e] = byte(s)
		}
		payload := []byte{byte(s), byte(s), byte(s), byte(s)}
		binary.LittleEndian.PutUint32(hdr[e+8:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(hdr[e+12:], uint32(off))
		body = append(body, payload...)
		off += len(payload)
	}
	return append(hdr, body...)
}

func TestPickIconImage(t *testing.T) {
	ico := fakeICO(16, 24, 32, 48, 256)
	for _, tc := range []struct{ want, got int }{
		{16, 16}, {20, 24}, {24, 24}, {31, 32}, {48, 48}, {64, 256}, {1000, 256}, {1, 16},
	} {
		img, ok := pickIconImage(ico, tc.want)
		if !ok || int(img[0]) != tc.got%256 {
			t.Errorf("want %d: got %v ok=%v, expected size %d", tc.want, img, ok, tc.got)
		}
	}
	if _, ok := pickIconImage([]byte("nope"), 16); ok {
		t.Error("garbage must not parse")
	}
}
