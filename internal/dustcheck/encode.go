package dustcheck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/klauspost/compress/zstd"
)

// Version changes when the check changes in a way that makes stored maps
// and results stale.
const Version = 1

const magic = "DUSTMAP1"

// Encode stores a map as zstd-compressed little-endian float32 behind a
// small header.
func Encode(m Map) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(magic)
	_ = binary.Write(&raw, binary.LittleEndian, [2]uint32{uint32(m.W), uint32(m.H)}) //nolint:gosec
	_ = binary.Write(&raw, binary.LittleEndian, m.Pix)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(raw.Bytes(), nil), nil
}

// Decode reads a map written by Encode.
func Decode(b []byte) (Map, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return Map{}, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		return Map{}, err
	}
	if len(raw) < len(magic)+8 || string(raw[:len(magic)]) != magic {
		return Map{}, errors.New("not a dust map")
	}
	w := int(binary.LittleEndian.Uint32(raw[len(magic):]))
	h := int(binary.LittleEndian.Uint32(raw[len(magic)+4:]))
	body := raw[len(magic)+8:]
	if w <= 0 || h <= 0 || len(body) != w*h*4 {
		return Map{}, fmt.Errorf("dust map of %dx%d holds %d bytes", w, h, len(body))
	}
	m := NewMap(w, h)
	for i := range m.Pix {
		m.Pix[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[i*4:]))
	}
	return m, nil
}

// Stretch renders a map as grey, lo black and hi white.
func Stretch(m Map, lo, hi float64) *image.Gray {
	im := image.NewGray(image.Rect(0, 0, m.W, m.H))
	for i, v := range m.Pix {
		f := (float64(v) - lo) / (hi - lo)
		if math.IsNaN(f) {
			f = 0.5
		}
		im.Pix[i] = uint8(math.Round(math.Max(0, math.Min(1, f)) * 255))
	}
	return im
}

// Crop renders the previous flat, the new one and their difference around
// a sensor position, side by side, each contrast-stretched, with the spot
// ringed in red. half is the crop's half width in sensor pixels.
func Crop(prev, cur Map, x, y, half int) ([]byte, error) {
	if prev.W != cur.W || prev.H != cur.H {
		return nil, errors.New("maps differ in size")
	}
	cx, cy, hb := x/BinFactor, y/BinFactor, half/BinFactor
	x0, x1 := max(0, cx-hb), min(cur.W, cx+hb)
	y0, y1 := max(0, cy-hb), min(cur.H, cy+hb)
	if x1 <= x0 || y1 <= y0 {
		return nil, errors.New("crop is outside the map")
	}
	diff := NewMap(cur.W, cur.H)
	for i := range diff.Pix {
		diff.Pix[i] = cur.Pix[i] - prev.Pix[i]
	}
	tiles := []*image.Gray{
		Stretch(sub(Gaussian(prev, 1.5), x0, y0, x1, y1), -0.02, 0.01),
		Stretch(sub(Gaussian(cur, 1.5), x0, y0, x1, y1), -0.02, 0.01),
		Stretch(sub(Gaussian(diff, 2), x0, y0, x1, y1), -0.008, 0.008),
	}
	const gap, scale = 6, 2
	tw, th := x1-x0, y1-y0
	out := image.NewRGBA(image.Rect(0, 0, (len(tiles)*(tw+gap)-gap)*scale, th*scale))
	red := color.RGBA{R: 255, A: 255}
	for t, tile := range tiles {
		ox := t * (tw + gap) * scale
		for py := range th * scale {
			for px := range tw * scale {
				g := tile.GrayAt(px/scale, py/scale).Y
				c := color.RGBA{R: g, G: g, B: g, A: 255}
				r := math.Hypot(float64(px/scale-(cx-x0)), float64(py/scale-(cy-y0)))
				if math.Abs(r-12) < 0.6 {
					c = red
				}
				out.SetRGBA(ox+px, py, c)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sub(m Map, x0, y0, x1, y1 int) Map {
	out := NewMap(x1-x0, y1-y0)
	for y := y0; y < y1; y++ {
		copy(out.Pix[(y-y0)*out.W:], m.Pix[y*m.W+x0:y*m.W+x1])
	}
	return out
}

const subMagic = "DUSTSUB1"

// subScale is the quantization of a sub map: 1/16384 of the sky, up to
// four times the sky.
const subScale = 16384

// EncodeSub stores a normalized sub map as zstd-compressed 16-bit codes;
// NaN and values at or under zero become 0, read back as NaN.
func EncodeSub(m Map) ([]byte, error) {
	raw := make([]byte, len(subMagic)+8+2*len(m.Pix))
	copy(raw, subMagic)
	binary.LittleEndian.PutUint32(raw[len(subMagic):], uint32(m.W))   //nolint:gosec
	binary.LittleEndian.PutUint32(raw[len(subMagic)+4:], uint32(m.H)) //nolint:gosec
	body := raw[len(subMagic)+8:]
	for i, v := range m.Pix {
		q := 0.0
		if !isNaN(v) && v > 0 {
			q = math.Max(1, math.Min(65535, math.Round(float64(v)*subScale)))
		}
		binary.LittleEndian.PutUint16(body[i*2:], uint16(q))
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil), nil
}

// DecodeSub reads a sub map written by EncodeSub.
func DecodeSub(b []byte) (Map, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return Map{}, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		return Map{}, err
	}
	if len(raw) < len(subMagic)+8 || string(raw[:len(subMagic)]) != subMagic {
		return Map{}, errors.New("not a sub map")
	}
	w := int(binary.LittleEndian.Uint32(raw[len(subMagic):]))
	h := int(binary.LittleEndian.Uint32(raw[len(subMagic)+4:]))
	body := raw[len(subMagic)+8:]
	if w <= 0 || h <= 0 || len(body) != w*h*2 {
		return Map{}, fmt.Errorf("sub map of %dx%d holds %d bytes", w, h, len(body))
	}
	m := NewMap(w, h)
	for i := range m.Pix {
		q := binary.LittleEndian.Uint16(body[i*2:])
		if q == 0 {
			m.Pix[i] = float32(math.NaN())
		} else {
			m.Pix[i] = float32(q) / subScale
		}
	}
	return m, nil
}
