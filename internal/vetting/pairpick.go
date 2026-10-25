package vetting

import (
	"context"
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// Pair picks: when a Gaia star within ZTF's blend radius gives a tenth
// or more of a periodic variable's light, ZTF measures the two as one
// object at their light-weighted centre, and in the variable's dips that
// centre moves away from whichever of the two dims. 70878 was matched to
// Gaia DR3 1858173345273104256 (G 15.46), but in its dips the ZTF r
// position moves away from Gaia DR3 1858173349582316032 (G 15.88) 0.85"
// off by 0.49 +- 0.04" per unit of q, about 0.13" at the deeper dip, as
// that star dimming does (0.51") and the G 15.46 star dimming, which
// moves it 0.34" toward that star, cannot. The eclipses are the G 15.88 star's, and its
// undiluted r depths are about 0.8 and 0.6 mag, not 0.46 and 0.38.
const (
	// pairMinShare is the least share of the pair's light the neighbour
	// gives for the test to run.
	pairMinShare = 0.1
	// pairMinDip is how many ZTF points the model puts in a dip of more
	// than pairDipQ.
	pairMinDip = 5
	pairDipQ   = 0.02
)

// pairPick is how ZTF's position of a periodic star and its one close
// Gaia neighbour moves with the light the model takes away: move is the
// fitted move along the line toward the neighbour, in arcsec per unit of
// q = 1 - 1/R for the pair's light R relative to normal; mine and theirs
// are that move when the star or the neighbour varies.
type pairPick struct {
	n               surveys.Neighbour
	move, err       float64
	mine, theirs    float64
	deep            float64
	points, inDip   int
	bands           []string
	targetG, nbourG float64
}

// pairPickOf fits the move over the ZTF bands with positions, or false
// when e has no period, the star has no single neighbour close and bright
// enough, or too few points carry a position or fall in a dip.
func pairPickOf(t surveys.Target, ss []lc.Series, e *period.Ephemeris) (pairPick, bool) {
	var p pairPick
	if !hasPeriod(e) {
		return p, false
	}
	n, ok := surveys.Pair(t)
	if !ok || n.Sep > surveys.ZTFDilutionRadius {
		return p, false
	}
	cosd := math.Cos(t.Dec * math.Pi / 180)
	ax, ay := (n.RA-t.RA)*cosd*3600, (n.Dec-t.Dec)*3600
	sep := math.Hypot(ax, ay)
	if !(sep > 0) {
		return p, false
	}
	ux, uy := ax/sep, ay/sep
	theirs := 1 / (1 + math.Pow(10, -0.4*(t.G-n.G)))
	if theirs < pairMinShare {
		return p, false
	}
	p.n, p.targetG, p.nbourG = n, t.G, n.G
	p.mine, p.theirs = -sep*theirs, sep*(1-theirs)
	var wsum, wmove float64
	for _, s := range ss {
		if s.Survey != lc.ZTF || s.Relative {
			continue
		}
		dil := s.Dilution
		if !(dil > 0 && dil <= 1) {
			dil = 1
		}
		key := period.BandKey(lc.ZTF, s.Band)
		q := func(bjd float64) float64 {
			r := 1 - dil*(1-math.Pow(10, -0.4*e.Predict(bjd, key)))
			return 1 - 1/r
		}
		var pts []lc.Point
		var xs []float64
		dips := 0
		for _, pt := range s.Points {
			if pt.RA == 0 && pt.Dec == 0 {
				continue
			}
			v := q(pt.BJD)
			if v < -pairDipQ {
				dips++
			}
			mp := pt
			mp.Mag = v
			pts = append(pts, mp)
			xs = append(xs, (pt.RA-t.RA)*cosd*3600*ux+(pt.Dec-t.Dec)*3600*uy)
		}
		if len(pts) < pairMinPoints || dips < pairMinDip {
			continue
		}
		move, se, used := shiftFit(pts, xs)
		if used < pairMinPoints || !(se > 0) {
			continue
		}
		w := 1 / (se * se)
		wsum += w
		wmove += w * move
		p.points += used
		p.inDip += dips
		p.bands = append(p.bands, s.Band)
		if len(p.bands) == 1 || s.Band == "r" {
			p.deep = q(e.T0)
		}
	}
	if wsum == 0 {
		return p, false
	}
	p.move, p.err = wmove/wsum, 1/math.Sqrt(wsum)
	return p, true
}

// theirsVaries says the position moves as the neighbour varying moves it
// and not as the star varying would.
func (p pairPick) theirsVaries() bool {
	return math.Abs(p.move-p.theirs) <= pairMatch*p.err && math.Abs(p.move-p.mine) >= pairRuleOut*p.err
}

// mineVaries says the position moves as the star varying moves it and
// not as the neighbour varying would.
func (p pairPick) mineVaries() bool {
	return math.Abs(p.move-p.mine) <= pairMatch*p.err && math.Abs(p.move-p.theirs) >= pairRuleOut*p.err
}

// shift words a move along the line toward the neighbour at the deeper
// dip, positive toward it.
func (p pairPick) shift(move float64) string {
	v := move * p.deep
	if v >= 0 {
		return fmt.Sprintf("%.2f\" toward the G %.2f star", v, p.nbourG)
	}
	return fmt.Sprintf("%.2f\" toward the G %.2f star", -v, p.targetG)
}

func (p pairPick) words() string {
	head := fmt.Sprintf("ZTF measures the G %.2f and G %.2f stars %.2f\" apart as one object, and at the deeper dip in %d %s points (%d in dips) it moves %s, where the G %.2f star dimming moves it %s and the G %.2f star dimming %s.",
		p.targetG, p.nbourG, p.n.Sep, p.points, joinAnd(p.bands), p.inDip, p.shift(p.move), p.nbourG, p.shift(p.theirs), p.targetG, p.shift(p.mine))
	switch {
	case p.theirsVaries():
		return head + fmt.Sprintf(" The dips are the G %.2f star's, Gaia DR3 %d, and the depths count the G %.2f star's light as a blend.", p.nbourG, p.n.GaiaID, p.targetG)
	case p.mineVaries():
		return head + fmt.Sprintf(" The dips are the G %.2f star's own.", p.targetG)
	}
	return ""
}

func joinAnd(bands []string) string {
	switch len(bands) {
	case 0:
		return ""
	case 1:
		return "ZTF " + bands[0]
	}
	out := "ZTF " + bands[0]
	for _, b := range bands[1 : len(bands)-1] {
		out += ", " + b
	}
	return out + " and " + bands[len(bands)-1]
}

// starByID is the Gaia star id near the spot as the star to vet, with
// the others in the spot's cone as its neighbours.
func (s *Service) starByID(ctx context.Context, ra, dec float64, id int64) (star, bool) {
	if s.Gaia == nil {
		return star{}, false
	}
	stars, err := s.Gaia.Cone(ctx, ra, dec, neighbourArcsec/3600, nil)
	if err != nil {
		return star{}, false
	}
	for i, g := range stars {
		if g.SourceID == id {
			st := starOf(stars, i)
			nra, ndec := g.At(epochYear(s.now()))
			st.sep = arcsec(ra, dec, nra, ndec)
			return st, true
		}
	}
	return star{}, false
}

// pairPicked moves the vet to the neighbour whose dips ZTF's position
// shows: the ZTF, ATLAS and NEOWISE light curves, which measure the pair
// together, are diluted again for it, and TESS, which fits each Gaia star
// on its own, is read for it. False when p does not name the neighbour or
// Gaia does not have it.
func (s *Service) pairPicked(ctx context.Context, f surveys.Field, ra, dec float64, st star, p pairPick, fx *fetches) (star, bool) {
	if !p.theirsVaries() {
		return st, false
	}
	nst, ok := s.starByID(ctx, ra, dec, p.n.GaiaID)
	if !ok {
		return st, false
	}
	for _, x := range []*surveys.Fetch{&fx.ztf, &fx.atlas, &fx.neowise} {
		ser := make([]lc.Series, len(x.Series))
		for i, sr := range x.Series {
			ser[i] = surveys.Rediluted(sr, st.target, nst.target)
		}
		x.Series = ser
	}
	if fx.tess.State != "" && s.Surveys != nil {
		fx.tess = s.Surveys.TESS(ctx, f, nst.target)
	}
	return nst, true
}

// pairPick weighs which star of a close pair has pr's period: the words
// saying so, and the star to vet in place of st when it is the neighbour.
func (s *Service) pairPick(ctx context.Context, f surveys.Field, ra, dec float64, st star, pr *period.Result, fx *fetches) (string, star, bool) {
	if pr == nil {
		return "", st, false
	}
	p, ok := pairPickOf(st.target, fx.ztf.Series, pr.Ephemeris)
	if !ok {
		return "", st, false
	}
	nst, moved := s.pairPicked(ctx, f, ra, dec, st, p, fx)
	return p.words(), nst, moved
}
