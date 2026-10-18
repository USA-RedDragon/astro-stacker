package stacking

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// CoverFrame is where a target's colour cover sits on its masters: each
// cover pixel bins Bin by Bin master pixels, less X and Y cropped off, and
// the cover is W by H pixels.
type CoverFrame struct {
	Bin        int
	X, Y, W, H int
}

// CoverFrameOf works out a target's cover frame from its masters by the
// rules refreshCover and renderCover follow: the palette chosen, the
// channels' common size and their common well-covered crop. ok is false
// for a target whose cover shows comet masters or has no colour palette.
func CoverFrameOf(object string, stacks []app.Stack) (CoverFrame, bool) {
	var with []app.Stack
	for _, s := range stacks {
		if s.LinearKey != nil && s.Width > 0 && s.Height > 0 {
			with = append(with, s)
		}
	}
	if CometCover(with) {
		return CoverFrame{}, false
	}
	linear := map[string]layer{}
	size := map[string][2]int{}
	for _, s := range with {
		linear[s.Filter] = fracCrop(layer{Key: *s.LinearKey, Effective: s.EffectiveSeconds}, s.CropX, s.CropY, s.CropW, s.CropH, s.Width, s.Height)
		size[s.Filter] = [2]int{s.Width, s.Height}
	}
	pal, ok := choosePalette(linear, shoSubject.MatchString(object))
	if !ok {
		return CoverFrame{}, false
	}
	bin := 0
	w, h := math.MaxInt, math.MaxInt
	var layers []layer
	for _, f := range pal.filters() {
		if f == filterLuminance {
			continue
		}
		sz := size[f]
		b := max(1, int(math.Ceil(float64(sz[0])/LinearMaxWidth)))
		if bin == 0 {
			bin = b
		}
		w, h = min(w, sz[0]/b), min(h, sz[1]/b)
		layers = append(layers, linear[f])
	}
	if bin == 0 || w <= 0 || h <= 0 || slices.ContainsFunc(layers, func(l layer) bool { return l.Key == "" }) {
		return CoverFrame{}, false
	}
	r := commonCrop(layers, w, h)
	return CoverFrame{Bin: bin, X: r.X, Y: r.Y, W: r.W, H: r.H}, true
}

// Pixel is where master pixel x, y falls on the cover, both counted from
// the centre of the first pixel.
func (f CoverFrame) Pixel(x, y float64) (float64, float64) {
	b := float64(f.Bin)
	return (x+0.5)/b - 0.5 - float64(f.X), (y+0.5)/b - 0.5 - float64(f.Y)
}
