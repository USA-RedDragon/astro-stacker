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
// comparison stars that agree in the sub, on an arbitrary zero per
// filter, and how much fainter those stars together were than their
// typical level. Spread is how far the kept comparison stars scatter
// about each other in the sub, a robust sigma in magnitudes; Agree of
// the Present ones sit within liveClip spreads of the others and set
// the sub's level.
type LivePoint struct {
	FrameID int
	Time    time.Time
	Filter  string
	Mag     float64
	Err     float64
	Comp    float64
	Spread  float64
	Agree   int
	Present int
}

const (
	// liveRecent is how many of the latest subs judge whether a
	// comparison star has left the others.
	liveRecent = 8
	// liveDepart and liveDepartSigma are how far, in magnitudes and in
	// errors of its median, a comparison star's latest subs must sit from
	// its usual place against the others for it to be dropped.
	liveDepart      = 0.1
	liveDepartSigma = 4.0
	// liveClip is how many spreads a comparison star may sit from the
	// others in one sub and still set the sub's level, and liveSpreadMin
	// the least spread used.
	liveClip      = 3.0
	liveSpreadMin = 0.01
)

// Points are the subs measured so far, oldest first, with how many
// comparison stars were kept of those picked. The kept stars are the
// full measurement's, less any whose latest subs left the others.
func (l *Live) Points() ([]LivePoint, int, int) {
	if len(l.subs) == 0 {
		return nil, 0, l.comps
	}
	e := buildEnsemble(l.subs, l.sp)
	order := make([]int, 0, len(l.subs))
	var flux, med [][]float64
	for i, r := range l.sp.recs {
		if r == nil || r.comps == nil || e.medians[l.subs[i].Filter] == nil {
			continue
		}
		order = append(order, i)
	}
	slices.SortStableFunc(order, func(a, b int) int { return midTime(l.subs[a]).Compare(midTime(l.subs[b])) })
	for _, i := range order {
		flux = append(flux, l.sp.recs[i].comps)
		med = append(med, e.medians[l.subs[i].Filter])
	}
	kept := liveKeep(flux, med, e.kept)
	var out []LivePoint
	for k, i := range order {
		r := l.sp.recs[i]
		lv, ok := liveLevel(flux[k], med[k], kept)
		if !ok || !r.fluxOK || !(r.flux > 0) {
			continue
		}
		s := l.subs[i]
		out = append(out, LivePoint{FrameID: s.FrameID, Time: midTime(s), Filter: s.Filter,
			Mag: -2.5 * math.Log10(r.flux/lv.t), Err: math.Hypot(magPerFlux*r.ferr/r.flux, lv.err), Comp: -2.5 * math.Log10(lv.t),
			Spread: lv.spread, Agree: lv.agree, Present: lv.present})
	}
	return out, len(kept), l.comps
}

// subLevel is one sub's comparison stars: their light against their usual
// light, its error, how far they scatter about each other, and how many
// of those present agree.
type subLevel struct {
	t, err, spread float64
	agree, present int
}

// liveLevel is a sub's level from the kept comparison stars, flux and med
// their light in the sub and usually. A star more than liveClip spreads
// from the others' median is left out of the sum, so one star caught by
// a cosmic ray or a satellite does not move the sub.
func liveLevel(flux, med []float64, kept []int) (subLevel, bool) {
	var js []int
	var off []float64
	for _, j := range kept {
		if flux[j] > 0 && med[j] > 0 {
			js = append(js, j)
			off = append(off, -2.5*math.Log10(flux[j]/med[j]))
		}
	}
	lv := subLevel{present: len(js)}
	if len(js) < minPresent {
		return lv, false
	}
	m := median(off)
	lv.spread = robustStd(off)
	lim := liveClip * math.Max(lv.spread, liveSpreadMin)
	var sum, msum, m2 float64
	for k, j := range js {
		if math.Abs(off[k]-m) > lim {
			continue
		}
		lv.agree++
		sum += flux[j]
		msum += med[j]
		m2 += med[j] * med[j]
	}
	if lv.agree < minPresent || !(sum > 0) {
		return lv, false
	}
	lv.t = sum / msum
	lv.err = lv.spread * math.Sqrt(m2) / msum
	return lv, true
}

// liveKeep is kept less the comparison stars whose latest liveRecent subs
// sit apart from the others: a star dropped has its median there more
// than liveDepart mag and liveDepartSigma errors from its median in the
// earlier subs, measured against the other kept stars. The worst goes first,
// up to maxDrops, keeping minComps. flux and med are each sub's light of
// the comparison stars and their usual light, oldest first.
func liveKeep(flux, med [][]float64, kept []int) []int {
	kept = slices.Clone(kept)
	if len(flux) < 2*liveRecent {
		return kept
	}
	for range maxDrops {
		if len(kept) <= minComps {
			break
		}
		worst, wz := -1, 1.0
		for _, j := range kept {
			var before, recent []float64
			for i := range flux {
				if !(flux[i][j] > 0 && med[i][j] > 0) {
					continue
				}
				s, ok := ratioTo(&rec{comps: flux[i]}, med[i], kept, j)
				if !ok {
					continue
				}
				v := -2.5 * math.Log10(flux[i][j]/s)
				if i >= len(flux)-liveRecent {
					recent = append(recent, v)
				} else {
					before = append(before, v)
				}
			}
			if len(recent) < liveRecent/2 || len(before) < liveRecent {
				continue
			}
			sigma := robustStd(before)
			shift := math.Abs(median(recent) - median(before))
			se := 1.2533 * sigma / math.Sqrt(float64(len(recent)))
			if shift <= liveDepart || shift <= liveDepartSigma*se {
				continue
			}
			if z := shift / (liveDepartSigma * se); z > wz {
				worst, wz = j, z
			}
		}
		if worst < 0 {
			break
		}
		kept = removeInt(kept, worst)
	}
	return kept
}
