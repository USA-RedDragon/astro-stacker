package discoveries

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// gaiaMirror answers Gaia from the local mirror with VizieR's query: stars
// brighter than G 21.5 in the cone (its centre and radius rounded as the
// query sends them), brightest first, at most 400000. ok is
// false when the mirror cannot answer and VizieR should.
func (c *Catalogs) gaiaMirror(ctx context.Context, ra, dec, radius float64) (stars []Star, ok bool, err error) {
	if c.GaiaMirror == nil {
		return nil, false, nil
	}
	got, err := c.GaiaMirror.Cone(ctx, starcat.Round(ra, 6), starcat.Round(dec, 6), starcat.Round(radius, 4), func(s *starcat.GaiaStar) bool { return starcat.Decimal(s.G) < gaiaFaintG })
	if err != nil {
		if errors.Is(err, starcat.ErrUnavailable) {
			slog.Debug("Gaia mirror unavailable; asking VizieR", "error", err)
			return nil, false, nil
		}
		return nil, false, err
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].G < got[j].G })
	if len(got) > gaiaMaxRows {
		got = got[:gaiaMaxRows]
	}
	out := make([]Star, len(got))
	for i, s := range got {
		out[i] = Star{RA: s.RA, Dec: s.Dec, G: starcat.Decimal(s.G), PMRA: zeroNaN(s.PMRA), PMDec: zeroNaN(s.PMDec)}
	}
	return out, true, nil
}

// vsxMirror answers VSX from the local mirror; ok is false when the mirror
// cannot answer and VizieR should.
func (c *Catalogs) vsxMirror(ctx context.Context, ra, dec, radius float64) (vars []Variable, ok bool, err error) {
	if c.VSXMirror == nil {
		return nil, false, nil
	}
	got, err := c.VSXMirror.Cone(ctx, starcat.Round(ra, 6), starcat.Round(dec, 6), starcat.Round(radius, 4), nil)
	if err != nil {
		if errors.Is(err, starcat.ErrUnavailable) {
			slog.Debug("VSX mirror unavailable; asking VizieR", "error", err)
			return nil, false, nil
		}
		return nil, false, err
	}
	out := make([]Variable, len(got))
	for i, s := range got {
		out[i] = Variable{Name: s.Name, Type: s.Type, RA: s.RA, Dec: s.Dec, Band: s.NMax, Amplitude: s.FMin == "(",
			Max: zeroNaN(s.Max), Min: zeroNaN(s.Min), Period: zeroNaN(s.Period)}
	}
	return out, true, nil
}

func zeroNaN(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}
