package ownimage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// ErrNoStar says the spot has no catalogued star to measure, or lies
// outside the target's frame.
var ErrNoStar = errors.New("no catalogued star at the spot in the target's frame")

// Live measures one star sub by sub as the subs arrive, against the
// comparison stars the full measurement would pick. Each Add reads one
// sub once; Points works from what is kept and reads nothing.
type Live struct {
	sp    *spotPlan
	subs  []Sub
	seen  map[int]bool
	comps int
}

// NewLive places spot on object's frame, matches its Gaia star and picks
// its comparison stars, reading the frame and the Gaia stars once.
func NewLive(ctx context.Context, d Data, object string, spot Spot) (*Live, error) {
	spot.Nights = nil
	w, width, height, err := d.Frame(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("the target's frame: %w", err)
	}
	sp := newSpotPlan(w, width, height, nil, spot, defaultRefSubs)
	if !sp.inFrame {
		return nil, ErrNoStar
	}
	f, err := newField(ctx, d, w, width, height, []*spotPlan{sp})
	if err != nil {
		return nil, fmt.Errorf("the target's Gaia stars: %w", err)
	}
	f.pickComps(sp)
	if sp.star == nil {
		return nil, ErrNoStar
	}
	f.shapes([]*spotPlan{sp})
	return &Live{sp: sp, seen: map[int]bool{}, comps: len(sp.comps)}, nil
}

// Has says whether the sub with frame id was added.
func (l *Live) Has(frameID int) bool { return l.seen[frameID] }

// Add reads one sub and measures the star and its comparison stars in it.
// A sub with no data at the star is kept as measured, with no point.
func (l *Live) Add(ctx context.Context, d Data, sub Sub) error {
	if l.seen[sub.FrameID] {
		return nil
	}
	p, err := d.Load(ctx, sub.Key)
	if err != nil {
		return err
	}
	info := subInfo{fm: discoveries.SensorMapOf(p), sat: planeMax(p)}
	fits := &subFits{p: p, shapes: map[*tile]shapeFit{}, stars: map[fitKey]fitOut{}}
	r, err := measureSpot(p, l.sp, info, len(l.subs), sub, fits)
	if err != nil && !errors.Is(err, errOffData) {
		return err
	}
	l.seen[sub.FrameID] = true
	l.subs = append(l.subs, sub)
	l.sp.recs = append(l.sp.recs, r)
	return nil
}

// LivePoint is one sub's measurement: the star's magnitude against the
// kept comparison stars, on an arbitrary zero per filter, and how much
// fainter the comparison stars together were than their typical level.
type LivePoint struct {
	FrameID int
	Time    time.Time
	Filter  string
	Mag     float64
	Err     float64
	Comp    float64
}

// Points are the subs measured so far, oldest first, with how many
// comparison stars were kept of those picked.
func (l *Live) Points() ([]LivePoint, int, int) {
	if len(l.subs) == 0 {
		return nil, 0, l.comps
	}
	e := buildEnsemble(l.subs, l.sp)
	var out []LivePoint
	for i, r := range l.sp.recs {
		t, ok := e.transp[i]
		if r == nil || !ok || !(t > 0) || !r.fluxOK || !(r.flux > 0) {
			continue
		}
		s := l.subs[i]
		out = append(out, LivePoint{FrameID: s.FrameID, Time: midTime(s), Filter: s.Filter,
			Mag: -2.5 * math.Log10(r.flux/t), Err: magPerFlux * r.ferr / r.flux, Comp: -2.5 * math.Log10(t)})
	}
	slices.SortStableFunc(out, func(a, b LivePoint) int { return a.Time.Compare(b.Time) })
	return out, len(e.kept), l.comps
}
