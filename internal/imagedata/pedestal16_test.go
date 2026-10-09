package imagedata_test

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

func TestPedestal16RoundTrip(t *testing.T) {
	t.Parallel()
	const w, h = 64, 48
	vals := make([]float32, w*h)
	for i := range vals {
		switch i % 7 {
		case 0:
			vals[i] = 0
		case 1:
			vals[i] = 1e-7
		case 2:
			vals[i] = -3e-6
		case 3:
			vals[i] = 1.2
		default:
			vals[i] = float32(i%1000)/1000 - 0.015
		}
	}
	codes := make([]uint16, len(vals))
	for i, v := range vals {
		codes[i] = imagedata.Quantize16(v)
	}
	var buf bytes.Buffer
	if err := imagedata.WriteXISF16(&buf, w, h, codes, []imagedata.Card{imagedata.StringCard("FILTER", "Ha", "")}, imagedata.Pedestal16Properties()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes()[:4096], []byte(`compression="zstd+sh:6144:2"`)) || !bytes.Contains(buf.Bytes()[:4096], []byte(`sampleFormat="UInt16"`)) {
		t.Fatalf("header %q", strings.TrimRight(string(buf.Bytes()[16:4096]), "\x00"))
	}
	im, err := imagedata.Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if im.W != w || im.H != h || im.C != 1 {
		t.Fatalf("geometry %dx%dx%d", im.W, im.H, im.C)
	}
	for i, v := range vals {
		got := im.Data[i]
		if got != imagedata.Dequantize16(codes[i]) {
			t.Fatalf("pixel %d decoded %v, codec says %v", i, got, imagedata.Dequantize16(codes[i]))
		}
		switch {
		case v == 0:
			if got != 0 {
				t.Errorf("empty pixel %d decoded %v", i, got)
			}
		case v > 1:
			if got < 0.9999 {
				t.Errorf("pixel %d over 1 decoded %v, want clipped near 1", i, got)
			}
		default:
			if got == 0 || math.Abs(float64(got-v)) > imagedata.Pedestal16Step/2+1e-7 {
				t.Errorf("pixel %d = %v decoded %v", i, v, got)
			}
		}
	}
}

func TestUInt16WithoutMappingStaysNormalized(t *testing.T) {
	t.Parallel()
	codes := pixels()
	var buf bytes.Buffer
	if err := imagedata.WriteXISF16(&buf, 3, 2, codes, nil, nil); err != nil {
		t.Fatal(err)
	}
	im, err := imagedata.Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	check(t, im)
}
