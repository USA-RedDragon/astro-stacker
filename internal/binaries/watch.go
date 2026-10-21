package binaries

import (
	"context"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// Confirmed are every confirmed star, eclipsing or not, without movers.
func (s *Service) Confirmed(ctx context.Context) ([]Star, error) {
	all, err := s.load(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, st := range all {
		if st.Source.Kind != app.DiscoveryKindMover {
			out = append(out, st)
		}
	}
	return out, nil
}

// Identity is how other catalogs name and place a star: its Gaia DR3
// name and id, its 2MASS name when the vet named it so, and its J2000
// position from Gaia, else where the app found it.
func (s Star) Identity() (name string, gaiaID int64, twoMASS string, ra, dec float64) {
	name = s.gaiaName()
	if id, ok := strings.CutPrefix(name, "Gaia DR3 "); ok {
		gaiaID, _ = strconv.ParseInt(id, 10, 64)
	}
	if c := s.catalogName(); strings.HasPrefix(c, "2MASS ") {
		twoMASS = c
	}
	ra, dec = s.Source.RA, s.Source.Dec
	if s.Facts != nil && s.Facts.Gaia != nil {
		ra, dec = s.Facts.Gaia.J2000()
	}
	if name == "" {
		name = s.catalogName()
	}
	return name, gaiaID, twoMASS, ra, dec
}
