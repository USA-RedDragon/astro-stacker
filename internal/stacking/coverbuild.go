package stacking

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"maps"
	"slices"
)

// coverResult is a subject's cover as composeSubject made it. A colour
// cover has Palette set, a single-filter one Filter; with neither, no
// channel was fit to show and the cover waits for more data.
type coverResult struct {
	JPEG    []byte
	Palette string
	Filter  string
	W, H    int
	Flat    map[string]flatReport
	LRGB    lrgbReport
	Lum     bool
}

// MonoFilterOrder ranks filters by how well one shows a target on its own:
// nebulae are faint in blue and green but bright in H-a.
func MonoFilterOrder() []string {
	return []string{filterHa, filterOIII, filterSII, filterLuminance, filterRed, filterGreen, filterBlue}
}

// monoCandidates orders a subject's filters for a single-filter cover: for
// a mosaic project those with the most panels first; then by
// MonoFilterOrder, among filters with at least a quarter of the best
// one's effective exposure, so a thin H-a master doesn't beat a deep one
// in another filter; the thinner ones last.
func monoCandidates(have map[string]layer) []string {
	best := 0.0
	for _, l := range have {
		if l.Key != "" {
			best = max(best, l.Effective)
		}
	}
	order := MonoFilterOrder()
	rank := func(f string) int {
		if i := slices.Index(order, f); i >= 0 {
			return i
		}
		return len(order)
	}
	var out []string
	for f, l := range have {
		if l.Key != "" {
			out = append(out, f)
		}
	}
	key := func(f string) (int, bool, int, float64) {
		l := have[f]
		return -l.Panels, l.Effective < best/4, rank(f), -l.Effective
	}
	slices.SortFunc(out, func(a, b string) int {
		pa, ta, ra, ea := key(a)
		pb, tb, rb, eb := key(b)
		switch {
		case pa != pb:
			return pa - pb
		case ta != tb:
			if ta {
				return 1
			}
			return -1
		case ra != rb:
			return ra - rb
		case ea != eb:
			if ea < eb {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}

// composeSubject renders subject's cover from its filters' linear previews
// (have), read by read. Each channel's sky background is taken off first
// by fit (flattenPlanes); a channel whose sky still shows structure past
// FlatStructureMax is left out and the palette chosen again without it.
// Without a colour palette the best single filter that comes out flat is
// the cover; without one of those, there is none yet. whole renders the
// colour channels over the whole of their previews, not only their
// common well-covered crop.
func composeSubject(ctx context.Context, fit backgroundFit, subject string, have map[string]layer, read func(string) (*linearImage, error), whole bool) (coverResult, error) {
	have = maps.Clone(have)
	cache := map[string]*linearImage{}
	load := func(f string) (*linearImage, error) {
		if img, ok := cache[f]; ok {
			return &linearImage{W: img.W, H: img.H, Data: slices.Clone(img.Data)}, nil
		}
		img, err := read(have[f].Key)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", subject, f, err)
		}
		cache[f] = img
		return &linearImage{W: img.W, H: img.H, Data: slices.Clone(img.Data)}, nil
	}
	sho := shoSubject.MatchString(subject)
	dropped := map[string]flatReport{}
	for {
		pal, ok := choosePalette(have, sho)
		if !ok {
			break
		}
		planes := map[string]*linearImage{}
		framed := map[string]layer{}
		for _, f := range pal.filters() {
			img, err := load(f)
			if err != nil {
				return coverResult{}, err
			}
			planes[f] = img
			l := have[f]
			if whole && f != filterLuminance {
				l.CropX, l.CropY, l.CropW, l.CropH = 0, 0, 0, 0
			}
			framed[f] = l
		}
		pal, planes, w, h, ok := framePlanes(subject, pal, framed, planes)
		if !ok {
			break
		}
		flat, reps, err := flattenPlanes(ctx, fit, planes, [][]string{{pal.R, pal.G, pal.B}}, w, h)
		if err != nil {
			return coverResult{}, err
		}
		bad := false
		for f, r := range reps {
			if r.Usable() {
				continue
			}
			slog.Info("Cover channel too uneven to show", "subject", subject, "filter", f, "structure", r.After, "before", r.Before, "object", r.Object)
			dropped[f] = r
			l := have[f]
			l.Key = ""
			have[f] = l
			bad = true
		}
		if bad {
			continue
		}
		jpg, lrgb, err := composeCover(pal, flat, w, h)
		if err != nil {
			return coverResult{}, err
		}
		maps.Copy(reps, dropped)
		name := pal.Name
		if lrgb.Used {
			name = lrgbPrefix + name
		}
		return coverResult{JPEG: jpg, Palette: name, W: w, H: h, Flat: reps, LRGB: lrgb, Lum: pal.Lum}, nil
	}
	if whole {
		return coverResult{Flat: dropped}, nil
	}
	return composeMono(ctx, fit, subject, have, load, dropped)
}

// composeMono renders the best single-filter cover whose sky comes out
// flat once its background is off; the subject's other filters of the
// same size help mask the target where its sky is measured.
func composeMono(ctx context.Context, fit backgroundFit, subject string, have map[string]layer, load func(string) (*linearImage, error), dropped map[string]flatReport) (coverResult, error) {
	for _, f := range monoCandidates(have) {
		img, err := load(f)
		if err != nil {
			return coverResult{}, err
		}
		l := have[f]
		r := commonCrop([]layer{l}, img.W, img.H)
		w, h := r.W, r.H
		planes := map[string]*linearImage{f: {W: w, H: h, Data: crop(img.Data, img.W, r)}}
		for g, o := range have {
			if g == f || o.Key == "" {
				continue
			}
			other, err := load(g)
			if err != nil {
				return coverResult{}, err
			}
			if other.W == img.W && other.H == img.H {
				planes[g] = &linearImage{W: w, H: h, Data: crop(other.Data, other.W, r)}
			}
		}
		flat, reps, err := flattenPlanes(ctx, fit, planes, nil, w, h)
		if err != nil {
			return coverResult{}, err
		}
		if !reps[f].Usable() {
			slog.Info("Cover channel too uneven to show", "subject", subject, "filter", f, "structure", reps[f].After, "before", reps[f].Before)
			dropped[f] = reps[f]
			continue
		}
		gray := stretchNonZero(flat[f].Data)
		out := image.NewGray(image.Rect(0, 0, w, h))
		for i, v := range gray {
			out.Pix[i] = to8(v)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85}); err != nil {
			return coverResult{}, err
		}
		dropped[f] = reps[f]
		return coverResult{JPEG: buf.Bytes(), Filter: f, W: w, H: h, Flat: dropped}, nil
	}
	return coverResult{Flat: dropped}, nil
}
