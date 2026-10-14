package discoveries

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// StampSize is the side of a cutout in pixels (about 94" at 1.9"/px).
const StampSize = 49

// Stamps are the night, reference and difference around one position.
type Stamps struct {
	X0, Y0 int
	Night  []float32
	Ref    []float32
	Diff   []float32
}

// Cut takes StampSize squares centred on (x, y) from night, ref and diff;
// pixels off the image are NaN.
func Cut(night, ref, diff *Plane, x, y float64) Stamps {
	x0 := int(math.Round(x)) - StampSize/2
	y0 := int(math.Round(y)) - StampSize/2
	s := Stamps{X0: x0, Y0: y0}
	take := func(p *Plane) []float32 {
		out := make([]float32, StampSize*StampSize)
		for j := range StampSize {
			for i := range StampSize {
				out[j*StampSize+i] = p.At(x0+i, y0+j)
			}
		}
		return out
	}
	s.Night, s.Ref, s.Diff = take(night), take(ref), take(diff)
	return s
}

func finiteSorted(vs ...[]float32) []float64 {
	var out []float64
	for _, v := range vs {
		for _, x := range v {
			if !isNaN32(x) {
				out = append(out, float64(x))
			}
		}
	}
	slices.Sort(out)
	return out
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted))))]
}

// PNGs renders the stamps as 8-bit greyscale PNGs: night and reference on
// one asinh stretch from the reference's sky, the difference linear and
// symmetric about zero.
func (s Stamps) PNGs() (night, ref, diff []byte, err error) {
	rs := finiteSorted(s.Ref)
	bg := quantile(rs, 0.5)
	noise := 1.4826 * mad(rs, bg)
	if !(noise > 0) {
		noise = 1e-6
	}
	top := quantile(finiteSorted(s.Night, s.Ref), 0.995) - bg
	soft := 3 * noise
	norm := math.Asinh(math.Max(top, soft) / soft)
	stretch := func(v float32) uint8 {
		if isNaN32(v) {
			return 0
		}
		t := math.Asinh(math.Max(float64(v)-bg, 0)/soft) / norm
		return uint8(math.Round(255 * math.Max(0, math.Min(1, t))))
	}
	ds := finiteSorted(s.Diff)
	dn := 1.4826 * mad(ds, quantile(ds, 0.5))
	if !(dn > 0) {
		dn = 1e-6
	}
	linear := func(v float32) uint8 {
		if isNaN32(v) {
			return 128
		}
		t := float64(v) / (8 * dn)
		return uint8(math.Round(127.5 + 127.5*math.Max(-1, math.Min(1, t))))
	}
	if night, err = grey(s.Night, stretch); err != nil {
		return
	}
	if ref, err = grey(s.Ref, stretch); err != nil {
		return
	}
	diff, err = grey(s.Diff, linear)
	return
}

func mad(sorted []float64, centre float64) float64 {
	d := make([]float64, len(sorted))
	for i, v := range sorted {
		d[i] = math.Abs(v - centre)
	}
	return median64(d)
}

func grey(v []float32, f func(float32) uint8) ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, StampSize, StampSize))
	for j := range StampSize {
		for i := range StampSize {
			img.SetGray(i, j, color.Gray{Y: f(v[j*StampSize+i])})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// FITS writes the stamps as one 3-plane FITS image (night, reference,
// difference) with the cutout's plate solution and the night's mid time.
func (s Stamps) FITS(wcs *WCS, extra []imagedata.Card) ([]byte, error) {
	data := make([]float32, 0, 3*StampSize*StampSize)
	data = append(data, s.Night...)
	data = append(data, s.Ref...)
	data = append(data, s.Diff...)
	var cards []imagedata.Card
	for _, c := range wcs.Shifted(s.X0, s.Y0, StampSize).Cards() {
		if len(c[1]) > 0 && c[1][0] == '\'' {
			cards = append(cards, imagedata.StringCard(c[0], c[1][1:len(c[1])-1], ""))
		} else {
			cards = append(cards, imagedata.Card{Key: c[0], Value: c[1]})
		}
	}
	cards = append(cards, imagedata.StringCard("PLANES", "night,reference,difference", "Planes of NAXIS3"))
	cards = append(cards, extra...)
	var b bytes.Buffer
	if err := imagedata.WriteFITS(&b, StampSize, StampSize, 3, data, cards); err != nil {
		return nil, fmt.Errorf("stamp FITS: %w", err)
	}
	return b.Bytes(), nil
}
