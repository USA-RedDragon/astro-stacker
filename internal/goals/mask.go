package goals

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"github.com/klauspost/compress/zstd"
)

const (
	MaskCovered uint8 = 1 << iota
	MaskBand
	MaskStar
	MaskSky
)

var ErrUnknownLayer = errors.New("unknown mask layer")

func LayerBit(layer string) (uint8, error) {
	switch layer {
	case "", "band":
		return MaskBand, nil
	case "stars":
		return MaskStar, nil
	case "sky":
		return MaskSky, nil
	case "covered":
		return MaskCovered, nil
	}
	return 0, fmt.Errorf("%w %q", ErrUnknownLayer, layer)
}

func (m Masks) Bits() []uint8 {
	out := make([]uint8, len(m.Covered))
	set := func(mask []bool, bit uint8) {
		for i, v := range mask {
			if v {
				out[i] |= bit
			}
		}
	}
	set(m.Covered, MaskCovered)
	set(m.Band, MaskBand)
	set(m.Stars, MaskStar)
	set(m.Background, MaskSky)
	return out
}

func CompressMask(bits []uint8) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(bits, nil), nil
}

func DecompressMask(data []byte, w, h int) ([]uint8, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	bits, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, err
	}
	if len(bits) != w*h {
		return nil, fmt.Errorf("mask has %d pixels, want %dx%d", len(bits), w, h)
	}
	return bits, nil
}

func MaskPNG(bits []uint8, w, h int, bit uint8) ([]byte, error) {
	if len(bits) != w*h {
		return nil, fmt.Errorf("mask has %d pixels, want %dx%d", len(bits), w, h)
	}
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.NRGBA{}, color.NRGBA{R: 255, G: 255, B: 255, A: 255}})
	for i, v := range bits {
		if v&bit != 0 {
			img.Pix[i] = 1
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
