package ownimage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	// stackHalf is half the side of the box cut around a moving object's
	// path in each frame, and stackR the radius of the light fit at each
	// place of it. The fit takes its own sky level inside stackR, which
	// leaves out diffuse light like the glow by Sombrero Galaxy's corner
	// that 67045 sat in.
	stackHalf = 40
	stackSide = 2*stackHalf + 1
	stackR    = 6
	// stackInner and stackOuter bound the places, in px from the path,
	// that set a stack's noise and serve as the random places compared
	// with it.
	stackInner = 10.0
	stackOuter = 30.0
	// stackPeakR is how far from the path the brightest point is looked
	// for, as in the audit of 67203.
	stackPeakR = 1.5
	// stackTrialIn and stackTrialOut bound where the fakes go, and
	// stackTrials how many.
	stackTrialIn  = 12.0
	stackTrialOut = 28.0
	stackTrials   = 32
	// stackUnused is how far in time from the detections' middle a frame
	// the detection did not use may be, as in the audits.
	stackUnused = 90 * time.Minute
	// stackSmearMin is the least smear, in px over half the detections'
	// span, a wrong motion must have to tell it from the fitted one;
	// stackShiftMax the most any frame may move for it.
	stackSmearMin = 3.0
	stackShiftMax = 20.0
	stackWrongN   = 10
	// stackMinFrames is the fewest detection frames a stack needs,
	// stackMaxUnused the most unused frames read, stackShare the least
	// share of a stack's frames that must cover a place to measure it,
	// and stackMinPlaces the fewest places that set a noise.
	stackMinFrames = 3
	stackMaxUnused = 40
	stackShare     = 0.7
	stackMinPlaces = 50
	// stackWide is how far from the path the noise places reach when too
	// few of those within stackOuter are clear of stars.
	stackWide = float64(stackHalf - stackR - 1)
	// stackGlowShare is the least share of the detection frames whose path
	// must lie on stars the master shows for the stack to blame their glow.
	stackGlowShare = 2.0 / 3
)

// TrackStack is a moving object's frames stacked along its fitted motion:
// its detections, the frames of the same night it crossed that the
// detection did not use, and fakes of the light it was found with put in
// the same frames, against the same stack along wrong motions and at
// random places. Values are signal to noise.
type TrackStack struct {
	SourceID int `json:"source_id"`
	Frames   int `json:"frames"`
	// SNR is the stack's light on the path, Peak the brightest within
	// 1.5 px of it.
	SNR  float64 `json:"snr"`
	Peak float64 `json:"peak"`
	// Injected is the median of the fakes, InjectedLo and InjectedHi the
	// middle two thirds of them, and Mag the magnitude they were given,
	// 0 when unknown.
	Injected   float64 `json:"injected"`
	InjectedLo float64 `json:"injected_lo"`
	InjectedHi float64 `json:"injected_hi"`
	Mag        float64 `json:"mag,omitempty"`
	// Wrong counts the wrong motions stacked, Wrong99 and WrongMax their
	// 99th percentile and largest brightest point by the path.
	Wrong    int     `json:"wrong"`
	Wrong99  float64 `json:"wrong_99"`
	WrongMax float64 `json:"wrong_max"`
	// Random counts the random places stacked along the fitted motion,
	// Random99 their 99th percentile and RandomAbove the share as bright
	// as Peak.
	Random      int     `json:"random"`
	Random99    float64 `json:"random_99"`
	RandomAbove float64 `json:"random_above"`
	// Unused counts the frames the detection did not use, UnusedSNR their
	// stack on the path and UnusedInjected the fakes' median in them.
	Unused         int     `json:"unused"`
	UnusedSNR      float64 `json:"unused_snr"`
	UnusedInjected float64 `json:"unused_injected"`
	// OnStars counts the detection frames whose path lies on stars the
	// master shows, and Glow says the path is masked by them in too many
	// detection frames to stack, as with 48654 (Cygnus Loop, 2026-09-07):
	// all six detections sat 2.5 to 3.2 px from a G 14.70 or G 16.49 star,
	// and an offline stack without the star mask showed 0.75 against a
	// fake's 14.1.
	OnStars int  `json:"on_stars,omitempty"`
	Glow    bool `json:"glow,omitempty"`
	// Note says why nothing was stacked.
	Note string `json:"note,omitempty"`
}

// stackFrame is one frame of a stack: its difference less its master in
// the master's units, the light fitted at every place of it, where the
// path sits in it and when.
type stackFrame struct {
	k        int
	det      bool
	dt       float64
	fx, fy   float64
	sigma    float64
	trail    [2]float64
	d, f     []float32
	w        float64
	flux     float64
	measured bool
	onStar   bool
}

type stackPlan struct {
	tp     *trackPlan
	shape  []*starModel
	frames []*stackFrame
	mbox   map[int][]float32
	msig   map[string]float64
	mflux  map[string][]float64
	light  map[string]float64
	mag    float64
	note   string
}

// StackTracks stacks each moving object's frames of its night along its
// fitted motion, with fakes and controls, reading each frame and master
// once.
func StackTracks(ctx context.Context, d Data, object string, spots []TrackSpot, opts Options) ([]TrackStack, error) {
	out := make([]TrackStack, len(spots))
	for i, s := range spots {
		out[i] = TrackStack{SourceID: s.SourceID}
	}
	if len(spots) == 0 {
		return out, nil
	}
	w, width, height, err := d.Frame(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("the target's frame: %w", err)
	}
	subs, err := d.Subs(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("the target's subs: %w", err)
	}
	masters := map[string]string{}
	if m, ok := d.(Masters); ok {
		if masters, err = m.Masters(ctx, object); err != nil {
			return nil, fmt.Errorf("the target's masters: %w", err)
		}
	}
	plans := make([]*stackPlan, len(spots))
	for i, s := range spots {
		sp, err := newStackPlan(ctx, d, w, width, height, subs, s)
		if err != nil {
			return nil, err
		}
		plans[i] = sp
	}
	if err := streamStacks(ctx, d, subs, masters, plans, opts); err != nil {
		return nil, err
	}
	for i, sp := range plans {
		finishStack(sp, &out[i])
	}
	return out, nil
}

func newStackPlan(ctx context.Context, d Data, w *discoveries.WCS, width, height int, subs []Sub, s TrackSpot) (*stackPlan, error) {
	tp := newTrackPlan(subs, s, width, height)
	sp := &stackPlan{tp: tp, mbox: map[int][]float32{}, msig: map[string]float64{}, mflux: map[string][]float64{}}
	if len(tp.subs) == 0 {
		sp.note = "None of the night's frames cover the object's path."
		return sp, nil
	}
	var dets []time.Time
	for _, r := range tp.rows {
		if r.Detected {
			dets = append(dets, r.Time)
		}
	}
	if len(dets) < stackMinFrames {
		sp.note = fmt.Sprintf("Only %d of its detections fall in frames that could be read; at least %d are needed.", len(dets), stackMinFrames)
		return sp, nil
	}
	var sum float64
	for _, t := range dets {
		sum += float64(t.Sub(dets[0]))
	}
	mid := dets[0].Add(time.Duration(sum / float64(len(dets))))
	type cand struct {
		k  int
		dt time.Duration
	}
	var unused []cand
	for k, r := range tp.rows {
		dt := r.Time.Sub(mid)
		switch {
		case r.Detected:
			sp.addFrame(k, true, dt, r)
		case dt.Abs() <= stackUnused:
			unused = append(unused, cand{k, dt})
		}
	}
	sort.Slice(unused, func(a, b int) bool { return unused[a].dt.Abs() < unused[b].dt.Abs() })
	for _, c := range unused[:min(len(unused), stackMaxUnused)] {
		sp.addFrame(c.k, false, c.dt, tp.rows[c.k])
	}
	mx, my := tp.at(mid)
	ra, dec := w.SkyOf(mx, my)
	f, err := newField(ctx, d, w, width, height, []*spotPlan{{spot: Spot{RA: ra, Dec: dec}, x: mx, y: my, inFrame: true}})
	if err != nil && !errors.Is(err, ErrNoGaia) {
		return nil, fmt.Errorf("the stars near a track: %w", err)
	}
	if f != nil && f.grid != nil {
		sp.shape = f.shapeStars(mx, my, shapeBright)
	}
	sp.light, sp.mag = trackLight(s.Points)
	return sp, nil
}

func (sp *stackPlan) addFrame(k int, det bool, dt time.Duration, r TrackSub) {
	x, y := sp.tp.at(r.Time)
	h := r.Exposure / 3600
	sp.frames = append(sp.frames, &stackFrame{k: k, det: det, dt: dt.Hours(), fx: x - math.Round(x), fy: y - math.Round(y),
		trail: [2]float64{sp.tp.vx * h, sp.tp.vy * h}})
}

// trackLight is the total light of a point source of the track's median
// magnitude in each filter it was found in, in that filter master's
// units, and that magnitude; filters with no magnitude take the median
// light found. The lights are those of the detections' 1.5 px aperture,
// made whole for each frame's star width later.
func trackLight(pts []TrackPoint) (map[string]float64, float64) {
	var mags []float64
	zps := map[string][]float64{}
	for _, p := range pts {
		if !(p.Flux > 0) {
			continue
		}
		if p.Mag > 0 {
			mags = append(mags, p.Mag)
			zps[p.Filter] = append(zps[p.Filter], p.Mag+2.5*math.Log10(p.Flux))
		}
	}
	out := map[string]float64{}
	if len(mags) == 0 {
		byFilter := map[string][]float64{}
		for _, p := range pts {
			if p.Flux > 0 {
				byFilter[p.Filter] = append(byFilter[p.Filter], p.Flux)
			}
		}
		for f, v := range byFilter {
			out[f] = median(v)
		}
		return out, 0
	}
	m := median(mags)
	for f, z := range zps {
		out[f] = math.Pow(10, -0.4*(m-median(z)))
	}
	return out, m
}

// lightIn is the fake's light in a filter, in its master's units: known
// only in the filters the track was found in, since a filter's light says
// nothing of another's.
func (sp *stackPlan) lightIn(filter string) (float64, bool) {
	v, ok := sp.light[filter]
	return v, ok && v > 0
}

func cutBox(p *discoveries.Plane, x, y float64) []float32 {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	out := make([]float32, stackSide*stackSide)
	for j := range stackSide {
		for i := range stackSide {
			out[j*stackSide+i] = p.At(cx-stackHalf+i, cy-stackHalf+j)
		}
	}
	return out
}

// streamStacks reads the masters, then the frames, cutting a box around
// the path in each and keeping only the light fitted at its places.
func streamStacks(ctx context.Context, d Data, subs []Sub, masters map[string]string, plans []*stackPlan, opts Options) error {
	filters := map[string]bool{}
	bySub := map[int]bool{}
	for _, sp := range plans {
		for _, fr := range sp.frames {
			filters[subs[sp.tp.subs[fr.k]].Filter] = true
			bySub[sp.tp.subs[fr.k]] = true
		}
	}
	var mkeys, mfilters []string
	for f := range filters {
		if k := masters[f]; k != "" {
			mkeys, mfilters = append(mkeys, k), append(mfilters, f)
		}
	}
	sidx := make([]int, 0, len(bySub))
	for i := range bySub {
		sidx = append(sidx, i)
	}
	sort.Ints(sidx)
	skeys := make([]string, len(sidx))
	for j, i := range sidx {
		skeys[j] = subs[i].Key
	}
	total := len(mkeys) + len(skeys)
	var mu sync.Mutex
	err := readImages(ctx, d, mkeys, opts.Threads, progressFrom(opts.Progress, 0, total), func(i int, p *discoveries.Plane) {
		for _, sp := range plans {
			sp.master(p, mfilters[i], subs, &mu)
		}
	})
	if err != nil {
		return fmt.Errorf("stack the tracks: %w", err)
	}
	err = readImages(ctx, d, skeys, opts.Threads, progressFrom(opts.Progress, len(mkeys), total), func(j int, p *discoveries.Plane) {
		for _, sp := range plans {
			sp.sub(p, sidx[j], subs)
		}
	})
	if err != nil {
		return fmt.Errorf("stack the tracks: %w", err)
	}
	return nil
}

func (sp *stackPlan) master(p *discoveries.Plane, filter string, subs []Sub, mu *sync.Mutex) {
	var boxes map[int][]float32
	for _, fr := range sp.frames {
		if subs[sp.tp.subs[fr.k]].Filter != filter {
			continue
		}
		if boxes == nil {
			boxes = map[int][]float32{}
		}
		r := sp.tp.rows[fr.k]
		x, y := sp.tp.at(r.Time)
		boxes[fr.k] = cutBox(p, x, y)
	}
	if boxes == nil {
		return
	}
	sig, fl := starLight(p, sp.shape)
	mu.Lock()
	defer mu.Unlock()
	sp.msig[filter], sp.mflux[filter] = sig, fl
	for k, b := range boxes {
		sp.mbox[k] = b
	}
}

// starLight fits the star width in an image and each shape star's light,
// NaN where a star could not be fitted.
func starLight(p *discoveries.Plane, shape []*starModel) (float64, []float64) {
	if len(shape) == 0 {
		return 0, nil
	}
	sig, sx, sy, ok := fitShape(p, shape)
	if !ok {
		return 0, nil
	}
	out := make([]float64, len(shape))
	for i, m := range shape {
		out[i] = math.NaN()
		if o := fitStar(p, m, sig, sx, sy, true); o.ok && o.flux > 0 {
			out[i] = o.flux
		}
	}
	return sig, out
}

// sub makes each of the plan's frames that sub si is: the box less its
// master, put on the master's scale by the shape stars' light, and the
// light fitted at its every place.
func (sp *stackPlan) sub(p *discoveries.Plane, si int, subs []Sub) {
	var sig float64
	var fl []float64
	done := false
	for _, fr := range sp.frames {
		if sp.tp.subs[fr.k] != si {
			continue
		}
		filter := subs[si].Filter
		mb := sp.mbox[fr.k]
		if mb == nil {
			continue
		}
		if !done {
			sig, fl = starLight(p, sp.shape)
			done = true
		}
		r := sp.tp.rows[fr.k]
		x, y := sp.tp.at(r.Time)
		sb := cutBox(p, x, y)
		sigRef := sp.msig[filter]
		s := make([]float64, len(sb))
		m := make([]float64, len(mb))
		for k := range sb {
			s[k], m[k] = float64(sb[k]), float64(mb[k])
		}
		own, width := sig, sig
		if !(own > 0) {
			own = sigRef
		}
		if !(own > 0) {
			own = startSigma
		}
		if sig > 0 && sigRef > sig {
			s = blur(s, stackSide, math.Sqrt(sigRef*sigRef-sig*sig))
			width = sigRef
		}
		dv, a := subtractRefN(s, blurTo(m, stackSide, width, sigRef), stackSide, true)
		if dv == nil {
			continue
		}
		if t := transparency(fl, sp.mflux[filter]); t > 0 {
			a = t
		}
		if !(width > 0) {
			width = sigRef
		}
		if !(width > 0) {
			width = startSigma
		}
		fr.d = make([]float32, len(dv))
		for k, v := range dv {
			fr.d[k] = float32(v / a)
		}
		fr.sigma = width
		fr.f = fitPlaces(fr.d, width)
		light, ok := sp.lightIn(filter)
		fr.flux = light / apertureShare(own, fr.trail)
		fr.w = placeWeight(fr.f)
		fr.measured = ok && fr.w > 0
		fr.onStar = maskedOnFrame(sb, fr.d)
	}
}

// maskedOnFrame says the star mask, not the frame's edge, leaves out
// pixels within stackR of the box's middle: the sub covers them all, and
// the difference leaves some out.
func maskedOnFrame(sb []float32, d []float32) bool {
	masked := false
	for dy := -stackR; dy <= stackR; dy++ {
		for dx := -stackR; dx <= stackR; dx++ {
			if dx*dx+dy*dy > stackR*stackR {
				continue
			}
			k := (stackHalf+dy)*stackSide + stackHalf + dx
			if isNaN32(sb[k]) {
				return false
			}
			if isNaN32(d[k]) {
				masked = true
			}
		}
	}
	return masked
}

// transparency is a frame's light against its master's, the median over
// the stars both fitted.
func transparency(sub, master []float64) float64 {
	if len(sub) == 0 || len(sub) != len(master) {
		return 0
	}
	var r []float64
	for i := range sub {
		if finite(sub[i]) && finite(master[i]) && master[i] > 0 {
			r = append(r, sub[i]/master[i])
		}
	}
	if len(r) == 0 {
		return 0
	}
	return median(r)
}

// apertureShare is the share of a star of width sigma, trailed by trail
// px over the exposure, the detection's 1.5 px aperture holds, averaged
// over where in its pixel the star falls.
func apertureShare(sigma float64, trail [2]float64) float64 {
	n := 15 + 2*int(math.Ceil(math.Hypot(trail[0], trail[1])/2))
	mid := float64(n / 2)
	var sum float64
	offs := [][2]float64{{0, 0}, {0.5, 0}, {0, 0.5}, {0.5, 0.5}}
	for _, o := range offs {
		p := discoveries.NewPlane(n, n)
		for _, q := range trailPoints(trail) {
			gaussianAt(p.Data, n, mid+o[0]+q[0], mid+o[1]+q[1], sigma, q[2])
		}
		sum += discoveries.Aperture(p, mid+o[0], mid+o[1], 1.5)
	}
	return sum / float64(len(offs))
}

// trailPoints spread a source's light along its trail: offsets from its
// middle and each one's share.
func trailPoints(trail [2]float64) [][3]float64 {
	k := min(max(1, int(math.Ceil(2*math.Hypot(trail[0], trail[1])))), 31)
	out := make([][3]float64, k)
	for i := range k {
		t := 0.0
		if k > 1 {
			t = float64(i)/float64(k-1) - 0.5
		}
		out[i] = [3]float64{t * trail[0], t * trail[1], 1 / float64(k)}
	}
	return out
}

// gaussianAt adds a round Gaussian of total light flux at (x, y) to an n
// wide image.
func gaussianAt(v []float32, n int, x, y, sigma, flux float64) {
	r := int(math.Ceil(5 * sigma))
	norm := flux / (2 * math.Pi * sigma * sigma)
	for j := int(math.Floor(y)) - r; j <= int(math.Ceil(y))+r; j++ {
		for i := int(math.Floor(x)) - r; i <= int(math.Ceil(x))+r; i++ {
			if i < 0 || j < 0 || i >= n || j*n+i >= len(v) {
				continue
			}
			dx, dy := float64(i)-x, float64(j)-y
			v[j*n+i] += float32(norm * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

// fitWeights are the star shape at each offset of a place within stackR,
// less its mean over the place's valid pixels, and their sum of squares.
func fitWeights(d []float32, n, i, j int, sigma float64, use func(k int, c float64)) (float64, bool) {
	var gs [(2*stackR + 1) * (2*stackR + 1)]float64
	var ks [(2*stackR + 1) * (2*stackR + 1)]int
	m, all := 0, 0
	centre := false
	var sg float64
	for dy := -stackR; dy <= stackR; dy++ {
		for dx := -stackR; dx <= stackR; dx++ {
			r2 := float64(dx*dx + dy*dy)
			if r2 > stackR*stackR {
				continue
			}
			all++
			k := (j+dy)*n + i + dx
			if isNaN32(d[k]) {
				continue
			}
			if r2 == 0 {
				centre = true
			}
			g := math.Exp(-r2 / (2 * sigma * sigma))
			gs[m], ks[m] = g, k
			sg += g
			m++
		}
	}
	if !centre || float64(m) < stackShare*float64(all) {
		return 0, false
	}
	mean := sg / float64(m)
	var ss float64
	for q := range m {
		c := gs[q] - mean
		ss += c * c
		if use != nil {
			use(ks[q], c)
		}
	}
	return ss, ss > 0
}

// fitPlaces fits the star shape, with its own sky level, at every place of
// a box far enough from its edge; NaN where too much is masked.
func fitPlaces(d []float32, sigma float64) []float32 {
	out := make([]float32, len(d))
	for k := range out {
		out[k] = float32(math.NaN())
	}
	for j := stackR; j < stackSide-stackR; j++ {
		for i := stackR; i < stackSide-stackR; i++ {
			var num float64
			ss, ok := fitWeights(d, stackSide, i, j, sigma, func(k int, c float64) { num += c * float64(d[k]) })
			if ok {
				out[j*stackSide+i] = float32(num / ss)
			}
		}
	}
	return out
}

// placeResponse is what the fit at place (i, j) gives for a star of unit
// light and width sigma at (x, y), trailed along trail.
func placeResponse(d []float32, i, j int, x, y, sigma float64, trail [2]float64) float64 {
	norm := 1 / (2 * math.Pi * sigma * sigma)
	pts := trailPoints(trail)
	var num float64
	ss, ok := fitWeights(d, stackSide, i, j, sigma, func(k int, c float64) {
		for _, q := range pts {
			dx, dy := float64(k%stackSide)-x-q[0], float64(k/stackSide)-y-q[1]
			num += c * q[2] * norm * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
		}
	})
	if !ok {
		return math.NaN()
	}
	return num / ss
}

// placeWeight is a frame's weight in a stack, one over the variance of its
// fitted light at places away from the path.
func placeWeight(f []float32) float64 {
	var v []float64
	for j := range stackSide {
		for i := range stackSide {
			r := math.Hypot(float64(i-stackHalf), float64(j-stackHalf))
			if r < stackInner || r > stackOuter {
				continue
			}
			if x := f[j*stackSide+i]; !isNaN32(x) {
				v = append(v, float64(x))
			}
		}
	}
	if len(v) < stackMinPlaces {
		return 0
	}
	s := robustStd(v)
	if !(s > 0) {
		return 0
	}
	return 1 / (s * s)
}

// bilinear reads an image between its pixels; NaN when any pixel around is.
func bilinear(v []float32, x, y float64) float64 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	if x0 < 0 || y0 < 0 || x0+1 >= stackSide || y0+1 >= stackSide {
		return math.NaN()
	}
	a, b := v[y0*stackSide+x0], v[y0*stackSide+x0+1]
	c, e := v[(y0+1)*stackSide+x0], v[(y0+1)*stackSide+x0+1]
	if isNaN32(a) || isNaN32(b) || isNaN32(c) || isNaN32(e) {
		return math.NaN()
	}
	tx, ty := x-float64(x0), y-float64(y0)
	return (1-ty)*((1-tx)*float64(a)+tx*float64(b)) + ty*((1-tx)*float64(c)+tx*float64(e))
}

// stackSet is a set of frames stacked with a motion added to the fitted
// one, in px an hour.
type stackSet struct {
	frames []*stackFrame
	vx, vy float64
}

func (s stackSet) pos(fr *stackFrame, u, v float64) (float64, float64) {
	return stackHalf + fr.fx + u + s.vx*fr.dt, stackHalf + fr.fy + v + s.vy*fr.dt
}

// at is the stack's light at offset (u, v) from the path, and inj, when
// set, that of a fake of each frame's light at that offset; NaN where too
// few frames cover it.
func (s stackSet) at(u, v float64, inj bool) (float64, float64) {
	var sw, swf, swi float64
	n := 0
	for _, fr := range s.frames {
		x, y := s.pos(fr, u, v)
		f := bilinear(fr.f, x, y)
		if !finite(f) {
			continue
		}
		sw += fr.w
		swf += fr.w * f
		if inj {
			swi += fr.w * fr.flux * respAt(fr, x, y)
		}
		n++
	}
	if n == 0 || float64(n) < stackShare*float64(len(s.frames)) {
		return math.NaN(), math.NaN()
	}
	return swf / sw, swi / sw
}

// respAt is a frame's fitted light, read between its places as the stack
// reads it, of a star of unit light at (x, y).
func respAt(fr *stackFrame, x, y float64) float64 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	tx, ty := x-float64(x0), y-float64(y0)
	var out float64
	for _, c := range [][3]float64{{0, 0, (1 - tx) * (1 - ty)}, {1, 0, tx * (1 - ty)}, {0, 1, (1 - tx) * ty}, {1, 1, tx * ty}} {
		r := placeResponse(fr.d, x0+int(c[0]), y0+int(c[1]), x, y, fr.sigma, fr.trail)
		if !finite(r) {
			return 0
		}
		out += c[2] * r
	}
	return out
}

// peakAt is the brightest of the stack within stackPeakR of an offset.
func (s stackSet) peakAt(u, v float64) float64 {
	best := math.NaN()
	for dy := -stackPeakR; dy <= stackPeakR; dy += 0.5 {
		for dx := -stackPeakR; dx <= stackPeakR; dx += 0.5 {
			if math.Hypot(dx, dy) > stackPeakR {
				continue
			}
			if f, _ := s.at(u+dx, v+dy, false); finite(f) && !(f <= best) {
				best = f
			}
		}
	}
	return best
}

// places are the whole pixel offsets from the path, every step px, between
// stackInner and stackOuter.
func places(step int) [][2]float64 { return placesTo(step, stackOuter) }

// placesTo are places reaching outer px from the path.
func placesTo(step int, outer float64) [][2]float64 {
	var out [][2]float64
	for v := -int(outer); v <= int(outer); v += step {
		for u := -int(outer); u <= int(outer); u += step {
			r := math.Hypot(float64(u), float64(v))
			if r >= stackInner && r <= outer {
				out = append(out, [2]float64{float64(u), float64(v)})
			}
		}
	}
	return out
}

// noise is the median and spread of the stack's light at the places.
func (s stackSet) noise(ps [][2]float64) (float64, float64, bool) {
	var v []float64
	for _, p := range ps {
		if f, _ := s.at(p[0], p[1], false); finite(f) {
			v = append(v, f)
		}
	}
	if len(v) < stackMinPlaces {
		return 0, 0, false
	}
	sd := robustStd(v)
	return median(v), sd, sd > 0
}

// trials are the offsets the fakes go to, spread evenly between
// stackTrialIn and stackTrialOut.
func trials() [][2]float64 {
	var out [][2]float64
	for i := 1; len(out) < stackTrials && i < 100*stackTrials; i++ {
		u, v := math.Mod(float64(i)*plasticA, 1), math.Mod(float64(i)*plasticB, 1)
		x, y := (2*u-1)*stackTrialOut, (2*v-1)*stackTrialOut
		if r := math.Hypot(x, y); r >= stackTrialIn && r <= stackTrialOut {
			out = append(out, [2]float64{x, y})
		}
	}
	return out
}

// injected is the fakes' signal to noise in a stack: each one's light with
// the stack's own at its place, the median and the middle two thirds.
func (s stackSet) injected(med, sd float64) (mid, lo, hi float64, ok bool) {
	var v []float64
	for _, t := range trials() {
		f, a := s.at(t[0], t[1], true)
		if finite(f) && finite(a) {
			v = append(v, (f+a-med)/sd)
		}
	}
	if len(v) < stackTrials/2 {
		return 0, 0, 0, false
	}
	sort.Float64s(v)
	return median(v), percentile(v, 16), percentile(v, 84), true
}

// percentile of sorted values, by linear interpolation.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	x := p / 100 * float64(len(sorted)-1)
	i := int(math.Floor(x))
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[i] + (x-float64(i))*(sorted[i+1]-sorted[i])
}

func finishStack(sp *stackPlan, res *TrackStack) {
	res.Note = sp.note
	if sp.note != "" {
		return
	}
	var det, unused []*stackFrame
	for _, fr := range sp.frames {
		if !fr.measured {
			continue
		}
		if fr.det {
			det = append(det, fr)
		} else {
			unused = append(unused, fr)
		}
	}
	sp.frames = nil
	if len(det) < stackMinFrames {
		res.Note = fmt.Sprintf("Only %d of its detection frames could be measured against their masters; at least %d are needed.", len(det), stackMinFrames)
		return
	}
	res.Frames, res.Mag = len(det), sp.mag
	if f, _ := (stackSet{frames: det}).at(0, 0, false); !finite(f) {
		open, on := pathClear(det)
		res.OnStars = on
		if len(open) >= stackMinFrames {
			det = open
			res.Frames = len(det)
		}
	}
	ds := stackSet{frames: det}
	f, _ := ds.at(0, 0, false)
	ps := places(1)
	med, sd, ok := ds.noise(ps)
	if !ok {
		ps = placesTo(1, stackWide)
		med, sd, ok = ds.noise(ps)
	}
	if !finite(f) && float64(res.OnStars) >= math.Max(stackMinFrames, stackGlowShare*float64(res.Frames)) {
		res.Glow = true
		unusedStack(unused, res)
		return
	}
	if !ok || !finite(f) {
		res.Note = "The stack's noise or its light on the path could not be measured: too much of the box is masked by stars or lies off the frames."
		return
	}
	res.SNR, res.Peak = (f-med)/sd, (ds.peakAt(0, 0)-med)/sd
	if res.Injected, res.InjectedLo, res.InjectedHi, ok = ds.injected(med, sd); !ok {
		res.Note = "Too few places by the path were clear enough to put fakes in."
		return
	}
	var rnd []float64
	for _, p := range places(2) {
		if v := ds.peakAt(p[0], p[1]); finite(v) {
			rnd = append(rnd, (v-med)/sd)
		}
	}
	if len(rnd) > 0 {
		sort.Float64s(rnd)
		res.Random, res.Random99 = len(rnd), percentile(rnd, 99)
		above := 0
		for _, v := range rnd {
			if v >= res.Peak {
				above++
			}
		}
		res.RandomAbove = float64(above) / float64(len(rnd))
	}
	wrongStacks(det, med, sd, res)
	unusedStack(unused, res)
}

// pathClear are the frames whose path can be measured, and the count of
// the others whose path lies on stars the master shows.
func pathClear(frames []*stackFrame) ([]*stackFrame, int) {
	var out []*stackFrame
	on := 0
	for _, fr := range frames {
		if finite(bilinear(fr.f, stackHalf+fr.fx, stackHalf+fr.fy)) {
			out = append(out, fr)
			continue
		}
		if fr.onStar {
			on++
		}
	}
	return out, on
}

// unusedStack stacks the frames the detection did not use along the path,
// with fakes, when enough of them cover it.
func unusedStack(unused []*stackFrame, res *TrackStack) {
	if len(unused) < stackMinFrames {
		return
	}
	us := stackSet{frames: unused}
	um, usd, uok := us.noise(places(1))
	if !uok {
		um, usd, uok = us.noise(placesTo(1, stackWide))
	}
	uf, _ := us.at(0, 0, false)
	if uok && finite(uf) {
		if mid, _, _, iok := us.injected(um, usd); iok {
			res.Unused, res.UnusedSNR, res.UnusedInjected = len(unused), (uf-um)/usd, mid
		}
	}
}

// wrongStacks stacks the detection frames along a grid of wrong motions,
// those that smear the fitted path by stackSmearMin px or more over half
// the detections' span and move no frame more than stackShiftMax px, and
// keeps the brightest point by the path in each.
func wrongStacks(det []*stackFrame, med, sd float64, res *TrackStack) {
	lo, hi, far := math.Inf(1), math.Inf(-1), 0.0
	for _, fr := range det {
		lo, hi = math.Min(lo, fr.dt), math.Max(hi, fr.dt)
		far = math.Max(far, math.Abs(fr.dt))
	}
	span := hi - lo
	if !(span > 0) || !(far > 0) {
		return
	}
	vmax := stackShiftMax / far
	step := vmax / stackWrongN
	ps := places(2)
	var vals []float64
	for j := -stackWrongN; j <= stackWrongN; j++ {
		for i := -stackWrongN; i <= stackWrongN; i++ {
			vx, vy := float64(i)*step, float64(j)*step
			if math.Hypot(vx, vy)*span/2 < stackSmearMin || math.Hypot(vx, vy) > vmax {
				continue
			}
			s := stackSet{frames: det, vx: vx, vy: vy}
			m, d := med, sd
			if wm, wd, ok := s.noise(ps); ok {
				m, d = wm, wd
			}
			if v := s.peakAt(0, 0); finite(v) {
				vals = append(vals, (v-m)/d)
			}
		}
	}
	if len(vals) == 0 {
		return
	}
	sort.Float64s(vals)
	res.Wrong, res.Wrong99, res.WrongMax = len(vals), percentile(vals, 99), vals[len(vals)-1]
}
