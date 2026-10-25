package stacking

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// CoverFrame is where a target's colour cover sits on its masters: each
// cover pixel bins Bin by Bin master pixels, less X and Y cropped off, and
// the cover is W by H pixels.
type CoverFrame struct {
	Bin        int
	X, Y, W, H int
	// Filters are the colour filters the frame was worked out from, sorted
	// and joined with commas.
	Filters string
}

// colourFilters are a palette's filters without the luminance, sorted and
// joined with commas; the luminance doesn't change a cover's frame.
func colourFilters(fs []string) string {
	out := slices.DeleteFunc(slices.Clone(fs), func(f string) bool { return f == filterLuminance })
	slices.Sort(out)
	return strings.Join(out, ",")
}

// Shows reports whether a cover of the named palette was composed from the
// filters f was worked out from, so f places points on it.
func (f CoverFrame) Shows(palette string) bool {
	fs := PaletteFilters(palette)
	return fs != nil && colourFilters(fs) == f.Filters
}

// coverPlan is a target's cover palette, its filters' linear previews and
// the colour channels' bin and common size on the previews.
type coverPlan struct {
	pal    palette
	linear map[string]layer
	bin    int
	w, h   int
	layers []layer
}

func planCover(object string, stacks []app.Stack) (coverPlan, bool) {
	var with []app.Stack
	for _, s := range stacks {
		if s.LinearKey != nil && s.Width > 0 && s.Height > 0 {
			with = append(with, s)
		}
	}
	if CometCover(with) {
		return coverPlan{}, false
	}
	linear := map[string]layer{}
	size := map[string][2]int{}
	for _, s := range with {
		linear[s.Filter] = fracCrop(layer{Key: *s.LinearKey, Effective: s.EffectiveSeconds}, s.CropX, s.CropY, s.CropW, s.CropH, s.Width, s.Height)
		size[s.Filter] = [2]int{s.Width, s.Height}
	}
	pal, ok := choosePalette(linear, shoSubject.MatchString(object))
	if !ok {
		return coverPlan{}, false
	}
	p := coverPlan{pal: pal, linear: linear, w: math.MaxInt, h: math.MaxInt}
	for _, f := range pal.filters() {
		if f == filterLuminance {
			continue
		}
		sz := size[f]
		b := max(1, int(math.Ceil(float64(sz[0])/LinearMaxWidth)))
		if p.bin == 0 {
			p.bin = b
		}
		p.w, p.h = min(p.w, sz[0]/b), min(p.h, sz[1]/b)
		p.layers = append(p.layers, linear[f])
	}
	if p.bin == 0 || p.w <= 0 || p.h <= 0 || slices.ContainsFunc(p.layers, func(l layer) bool { return l.Key == "" }) {
		return coverPlan{}, false
	}
	return p, true
}

// CoverFrameOf works out a target's cover frame from its masters by the
// rules refreshCover and renderCover follow: the palette chosen, the
// channels' common size and their common well-covered crop. ok is false
// for a target whose cover shows comet masters or has no colour palette.
func CoverFrameOf(object string, stacks []app.Stack) (CoverFrame, bool) {
	p, ok := planCover(object, stacks)
	if !ok {
		return CoverFrame{}, false
	}
	r := commonCrop(p.layers, p.w, p.h)
	return CoverFrame{Bin: p.bin, X: r.X, Y: r.Y, W: r.W, H: r.H, Filters: colourFilters(p.pal.filters())}, true
}

// WholeCover renders a target's colour picture as its cover is rendered,
// but over the whole of its masters. The cover keeps only the part that
// every colour filter covered well, so a star at the masters' edge can be
// cut off it. Its sky's background is taken off as the cover's is where
// the model is installed. get reads an object of the processed bucket.
func WholeCover(ctx context.Context, get func(context.Context, string) ([]byte, error), object string, stacks []app.Stack) (image.Image, CoverFrame, error) {
	p, ok := planCover(object, stacks)
	if !ok {
		return nil, CoverFrame{}, errors.New("no colour palette from the masters")
	}
	fit := CoverBackground().Fit
	if err := CoverBackground().Ready(); err != nil {
		fit = flatBackground
	}
	res, err := composeSubject(ctx, fit, object, p.linear, func(key string) (*linearImage, error) {
		raw, err := get(ctx, key)
		if err != nil {
			return nil, err
		}
		return linearOf(raw)
	}, true)
	if err != nil {
		return nil, CoverFrame{}, err
	}
	if res.Palette == "" {
		return nil, CoverFrame{}, errors.New("no colour channel came out flat enough")
	}
	frame := CoverFrame{Bin: p.bin, W: res.W, H: res.H, Filters: colourFilters(p.pal.filters())}
	if !frame.Shows(res.Palette) {
		return nil, CoverFrame{}, fmt.Errorf("a channel was left out of the %s picture, so its frame differs from the masters'", res.Palette)
	}
	b := res.JPEG
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, CoverFrame{}, err
	}
	return img, frame, nil
}

// Pixel is where master pixel x, y falls on the cover, both counted from
// the centre of the first pixel.
func (f CoverFrame) Pixel(x, y float64) (float64, float64) {
	b := float64(f.Bin)
	return (x+0.5)/b - 0.5 - float64(f.X), (y+0.5)/b - 0.5 - float64(f.Y)
}
