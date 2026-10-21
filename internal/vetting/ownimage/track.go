package ownimage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// Masters is Data that can name each filter's master of a target, the
// deep stack every sub is registered to.
type Masters interface {
	Masters(ctx context.Context, object string) (map[string]string, error)
}

// TrackPoint is one detection of a moving object at its frame pixel,
// with its light in the detection's aperture, in its filter master's
// units, and the magnitude that light was given, 0 when none.
type TrackPoint struct {
	FrameID int
	Filter  string
	Time    time.Time
	X, Y    float64
	SNR     float64
	Flux    float64
	Mag     float64
}

// TrackSpot is a moving object's detections on one night and filter
// group.
type TrackSpot struct {
	SourceID int
	Night    time.Time
	Group    string
	Points   []TrackPoint
}

// TrackSub is one sub of the night measured where the object was: at its
// detection, or where its fitted motion put it in a sub it was not
// detected in.
type TrackSub struct {
	FrameID  int       `json:"frame_id"`
	Filter   string    `json:"filter"`
	Time     time.Time `json:"time"`
	Exposure float64   `json:"exposure"`
	Detected bool      `json:"detected"`
	X        float64   `json:"x"`
	Y        float64   `json:"y"`
	// SX and SY are the sensor pixel under X and Y, and U and V how far the
	// registration moved that pixel, when the sub's map to the sensor is known.
	SX     float64 `json:"sx"`
	SY     float64 `json:"sy"`
	U      float64 `json:"u"`
	V      float64 `json:"v"`
	Sensor bool    `json:"sensor"`
	// Sigma is the star width in the sub, in pixels.
	Sigma float64 `json:"sigma"`
	// Flux and Err are the object's light in the sub less its filter's
	// master, fitted with the star shape at X and Y.
	Flux     float64 `json:"flux"`
	Err      float64 `json:"err"`
	Measured bool    `json:"measured"`
}

// Ellipse is a fitted Gaussian's widths along and across Angle, its long
// axis in degrees from frame +x toward +y.
type Ellipse struct {
	Major float64 `json:"major"`
	Minor float64 `json:"minor"`
	Angle float64 `json:"angle"`
}

// TrackShape is the object's shape in the sum of its detections less the
// masters, each scaled to the same light, against Star, the bright stars
// near it summed over the same frames the same way.
type TrackShape struct {
	Frames int     `json:"frames"`
	SNR    float64 `json:"snr"`
	Object Ellipse `json:"object"`
	Star   Ellipse `json:"star"`
	Stars  int     `json:"stars"`
}

// TrackResult is one moving object's measurements on its night.
type TrackResult struct {
	SourceID int         `json:"source_id"`
	Subs     []TrackSub  `json:"subs"`
	Shape    *TrackShape `json:"shape,omitempty"`
	// ShapeNote says why the shape could not be fitted.
	ShapeNote string `json:"shape_note,omitempty"`
	// Dark counts the detections whose sensor pixel, or one next to it,
	// is hot in the master dark the sub was calibrated with, of DarkN
	// looked at; Near counts the same at places 6 px away on the sensor,
	// of NearN, for how often that happens by chance.
	Dark  int `json:"dark"`
	DarkN int `json:"dark_n"`
	Near  int `json:"near"`
	NearN int `json:"near_n"`
	// Note says why nothing could be measured.
	Note string `json:"note,omitempty"`
}

const (
	trackObjectR = 6.0
	// psfAnnulusR and psfAnnulusMin are the outer radius of the annulus
	// the local background is read from and the fewest pixels it needs.
	psfAnnulusR   = 12.0
	psfAnnulusMin = 30
	trackRefMin   = 30
	trackShapeSN  = 3.0
	// trackStarMask leaves out the pixels where the master shows a star
	// this many times the difference's noise, whose residuals would spoil
	// the fit.
	trackStarMask = 5.0
	// trackWidest is the widest Gaussian the shape fit tries; a fit there
	// found no clear object.
	trackWidest = 7.9
)

type trackPlan struct {
	spot   TrackSpot
	t0     time.Time
	x0, y0 float64
	vx, vy float64
	subs   []int
	rows   []TrackSub
	shape  []*starModel
	stamps [][]float32
	refs   map[int][]float32
	refSig map[string]float64
	refAt  map[string]bool
	dets   map[int]TrackPoint
	stars  []starStack
}

// starStack is the bright stars near a track in one sub, each scaled to
// unit light, summed, with the sum of their centres' offsets from the
// stamp's middle pixel.
type starStack struct {
	sum    []float64
	n      int
	fx, fy float64
}

func (tp *trackPlan) at(t time.Time) (float64, float64) {
	d := t.Sub(tp.t0).Hours()
	return tp.x0 + tp.vx*d, tp.y0 + tp.vy*d
}

func fitTrackPoints(pts []TrackPoint) (t0 time.Time, x0, y0, vx, vy float64, ok bool) {
	if len(pts) < 2 {
		return time.Time{}, 0, 0, 0, 0, false
	}
	t0 = pts[0].Time
	n := float64(len(pts))
	var st, stt, sx, sy, stx, sty float64
	for _, p := range pts {
		d := p.Time.Sub(t0).Hours()
		st += d
		stt += d * d
		sx += p.X
		sy += p.Y
		stx += d * p.X
		sty += d * p.Y
	}
	den := n*stt - st*st
	if !(math.Abs(den) > 1e-12) {
		return time.Time{}, 0, 0, 0, 0, false
	}
	vx = (n*stx - st*sx) / den
	vy = (n*sty - st*sy) / den
	return t0, (sx - vx*st) / n, (sy - vy*st) / n, vx, vy, true
}

// MeasureTracks measures each moving object in every sub of its night
// and filter group: where it fell on the sensor, its light against the
// filter's master, and its shape, reading each sub and master once.
func MeasureTracks(ctx context.Context, d Data, object string, spots []TrackSpot, opts Options) ([]TrackResult, error) {
	out := make([]TrackResult, len(spots))
	for i, s := range spots {
		out[i] = TrackResult{SourceID: s.SourceID}
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
	plans := make([]*trackPlan, len(spots))
	for i, s := range spots {
		plans[i] = newTrackPlan(subs, s, width, height)
		if len(plans[i].subs) == 0 {
			out[i].Note = "None of the night's frames cover the object's path."
			continue
		}
		mx, my := plans[i].at(plans[i].t0)
		if len(plans[i].spot.Points) > 0 {
			mid := plans[i].spot.Points[len(plans[i].spot.Points)/2]
			mx, my = mid.X, mid.Y
		}
		ra, dec := w.SkyOf(mx, my)
		sp := &spotPlan{spot: Spot{RA: ra, Dec: dec}, x: mx, y: my, inFrame: true}
		f, err := newField(ctx, d, w, width, height, []*spotPlan{sp})
		if err != nil && !errors.Is(err, ErrNoGaia) {
			return nil, fmt.Errorf("the stars near a track: %w", err)
		}
		if f != nil && f.grid != nil {
			plans[i].shape = f.shapeStars(mx, my, shapeBright)
		}
	}
	if err := streamTracks(ctx, d, subs, masters, plans, opts); err != nil {
		return nil, err
	}
	need := map[darkPos]bool{}
	darks := make([][]darkSpot, len(plans))
	for i, tp := range plans {
		if len(tp.subs) == 0 {
			continue
		}
		darks[i] = trackDarks(subs, tp, need)
	}
	levels, tails := darkLevels(ctx, d, need)
	for i, tp := range plans {
		if len(tp.subs) == 0 {
			continue
		}
		finishTrack(tp, &out[i])
		countDarks(darks[i], levels, tails, &out[i])
	}
	return out, nil
}

func newTrackPlan(subs []Sub, s TrackSpot, width, height int) *trackPlan {
	tp := &trackPlan{spot: s, refs: map[int][]float32{}, refSig: map[string]float64{}, refAt: map[string]bool{}, dets: map[int]TrackPoint{}}
	pts := append([]TrackPoint(nil), s.Points...)
	sort.Slice(pts, func(a, b int) bool { return pts[a].Time.Before(pts[b].Time) })
	tp.spot.Points = pts
	var ok bool
	tp.t0, tp.x0, tp.y0, tp.vx, tp.vy, ok = fitTrackPoints(pts)
	if !ok {
		return tp
	}
	for _, p := range pts {
		tp.dets[p.FrameID] = p
	}
	nk := nightKey(s.Night)
	margin := float64(stampHalf + 1)
	for i, sub := range subs {
		if nightKey(sub.Night) != nk || discoveries.GroupOf(sub.Filter) != s.Group {
			continue
		}
		row := TrackSub{FrameID: sub.FrameID, Filter: sub.Filter, Time: midTime(sub), Exposure: sub.Exposure}
		if p, ok := tp.dets[sub.FrameID]; ok {
			row.Detected, row.X, row.Y = true, p.X, p.Y
		} else {
			row.X, row.Y = tp.at(row.Time)
		}
		if row.X < margin || row.Y < margin || row.X > float64(width)-margin || row.Y > float64(height)-margin {
			continue
		}
		tp.subs = append(tp.subs, i)
		tp.rows = append(tp.rows, row)
		tp.refAt[sub.Filter] = true
	}
	tp.stamps = make([][]float32, len(tp.subs))
	tp.stars = make([]starStack, len(tp.subs))
	return tp
}

func cutStamp(p *discoveries.Plane, x, y float64) []float32 {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	st := make([]float32, stampSide*stampSide)
	for j := range stampSide {
		for i := range stampSide {
			st[j*stampSide+i] = p.At(cx-stampHalf+i, cy-stampHalf+j)
		}
	}
	return st
}

type trackJob struct {
	sub    int
	master string
	filter string
}

func streamTracks(ctx context.Context, d Data, subs []Sub, masters map[string]string, plans []*trackPlan, opts Options) error {
	var jobs []trackJob
	seen := map[int]bool{}
	filters := map[string]bool{}
	for _, tp := range plans {
		for _, i := range tp.subs {
			if !seen[i] {
				seen[i] = true
				jobs = append(jobs, trackJob{sub: i})
			}
		}
		for f := range tp.refAt {
			filters[f] = true
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].sub < jobs[b].sub })
	fl := make([]string, 0, len(filters))
	for f := range filters {
		fl = append(fl, f)
	}
	sort.Strings(fl)
	for _, f := range fl {
		if k := masters[f]; k != "" {
			jobs = append(jobs, trackJob{sub: -1, master: k, filter: f})
		}
	}
	keys := make([]string, len(jobs))
	for i, j := range jobs {
		keys[i] = j.master
		if j.sub >= 0 {
			keys[i] = subs[j.sub].Key
		}
	}
	var mu sync.Mutex
	err := readImages(ctx, d, keys, opts.Threads, progressFrom(opts.Progress, 0, len(keys)), func(i int, p *discoveries.Plane) {
		if j := jobs[i]; j.sub >= 0 {
			measureTrackSub(p, j.sub, plans, &mu)
		} else {
			measureTrackMaster(p, j.filter, plans, &mu)
		}
	})
	if err != nil {
		return fmt.Errorf("measure the tracks: %w", err)
	}
	return nil
}

// progressFrom tells progress how many of total images are read, the
// first offset of them read before.
func progressFrom(progress func(done, total int), offset, total int) func(done int) {
	if progress == nil {
		return nil
	}
	progress(offset, total)
	return func(done int) { progress(offset+done, total) }
}

// readImages reads each key once, threads at a time, and hands each image
// read to use with its key's index. An image that cannot be read is left
// out with a warning; only the context's end stops the reading.
func readImages(parent context.Context, d Data, keys []string, threads int, progress func(done int), use func(i int, p *discoveries.Plane)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var read atomic.Int64
	ch := make(chan int)
	var wg sync.WaitGroup
	for range max(threads, 1) {
		wg.Go(func() {
			for i := range ch {
				p, err := d.Load(ctx, keys[i])
				if progress != nil {
					progress(int(read.Add(1)))
				}
				if err != nil {
					if ctx.Err() == nil {
						slog.Warn("Could not read an image for the moving object checks", "key", keys[i], "error", err)
					}
					continue
				}
				use(i, p)
			}
		})
	}
	for i := range keys {
		select {
		case ch <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(ch)
	wg.Wait()
	return parent.Err()
}

func measureTrackSub(p *discoveries.Plane, si int, plans []*trackPlan, mu *sync.Mutex) {
	fm := discoveries.SensorMapOf(p)
	for _, tp := range plans {
		for k, i := range tp.subs {
			if i != si {
				continue
			}
			r := tp.rows[k]
			st := cutStamp(p, r.X, r.Y)
			var sig float64
			var stars starStack
			if len(tp.shape) > 0 {
				if s, sx, sy, ok := fitShape(p, tp.shape); ok {
					sig = s
					stars = stackStars(p, tp.shape, sx, sy)
				}
			}
			mu.Lock()
			tp.stamps[k] = st
			tp.stars[k] = stars
			tp.rows[k].Sigma = sig
			if fm.OK {
				tp.rows[k].SX, tp.rows[k].SY = fm.At(r.X, r.Y)
				tp.rows[k].U, tp.rows[k].V = r.X-tp.rows[k].SX, r.Y-tp.rows[k].SY
				tp.rows[k].Sensor = true
			}
			mu.Unlock()
		}
	}
}

func measureTrackMaster(p *discoveries.Plane, filter string, plans []*trackPlan, mu *sync.Mutex) {
	for _, tp := range plans {
		if !tp.refAt[filter] {
			continue
		}
		var sig float64
		if len(tp.shape) > 0 {
			if s, _, _, ok := fitShape(p, tp.shape); ok {
				sig = s
			}
		}
		refs := map[int][]float32{}
		for k := range tp.rows {
			if r := &tp.rows[k]; r.Filter == filter {
				refs[k] = cutStamp(p, r.X, r.Y)
			}
		}
		mu.Lock()
		tp.refSig[filter] = sig
		for k, v := range refs {
			tp.refs[k] = v
		}
		mu.Unlock()
	}
}

// diffOf is a sub's stamp less its master's, the master scaled and
// offset to fit the stamp away from the object and blurred to the sub's
// star width when the sub's stars are wider; with mask, pixels on stars
// the master shows are left out. It is nil when either is missing.
func diffOf(st, ref []float32, sigSub, sigRef float64, mask bool) []float64 {
	if st == nil || ref == nil {
		return nil
	}
	s := make([]float64, len(st))
	r := make([]float64, len(ref))
	for k := range st {
		s[k], r[k] = float64(st[k]), float64(ref[k])
	}
	return subtractRef(s, blurTo(r, stampSide, sigSub, sigRef), mask)
}

// blurTo blurs a master of side n to a sub's star width when the sub's
// stars are wider.
func blurTo(r []float64, n int, sigSub, sigRef float64) []float64 {
	if sigSub > 0 && sigRef > 0 && sigSub > sigRef {
		return blur(r, n, math.Sqrt(sigSub*sigSub-sigRef*sigRef))
	}
	return r
}

// subtractRef is a sub's stamp less its master's, the master already
// blurred to the sub's star width, scaled and offset to fit the stamp
// away from its middle; with mask, pixels on stars the master shows are
// left out.
func subtractRef(s, r []float64, mask bool) []float64 {
	out, _ := subtractRefN(s, r, stampSide, mask)
	return out
}

// subtractRefN is subtractRef for a square of side n, with the scale the
// master was fitted with.
func subtractRefN(s, r []float64, n int, mask bool) ([]float64, float64) {
	half := n / 2
	var sr, ss, srr, sv, cnt float64
	for k := range s {
		x, y := float64(k%n-half), float64(k/n-half)
		if math.Hypot(x, y) <= trackObjectR || !finite(s[k]) || !finite(r[k]) {
			continue
		}
		sr += r[k]
		ss += s[k]
		srr += r[k] * r[k]
		sv += r[k] * s[k]
		cnt++
	}
	if cnt < trackRefMin {
		return nil, 0
	}
	den := cnt*srr - sr*sr
	a := 1.0
	if den > 0 {
		a = (cnt*sv - sr*ss) / den
	}
	if !(a > 0) || !finite(a) {
		a = 1
	}
	b := (ss - a*sr) / cnt
	out := make([]float64, len(s))
	var ring, lvl []float64
	for k := range s {
		out[k] = s[k] - a*r[k] - b
		x, y := float64(k%n-half), float64(k/n-half)
		if math.Hypot(x, y) > trackObjectR && finite(out[k]) {
			ring = append(ring, out[k])
			lvl = append(lvl, a*r[k])
		}
	}
	noise, base := robustStd(ring), median(lvl)
	if mask && noise > 0 {
		for k := range out {
			if a*r[k]-base > trackStarMask*noise {
				out[k] = math.NaN()
			}
		}
	}
	return out, a
}

// psfFlux fits a round Gaussian of width sigma at (cx, cy) of a stamp,
// less the median of the annulus just outside the object, and gives its
// flux and error from the scatter outside the object and the annulus
// median's own error. The stamp's level as a whole leaves diffuse light
// at the spot: 67045 on Sombrero Galaxy's 2025-12-29, a glow near a
// corner of the frame, measured at signal to noise 27 without the
// annulus and 3 with it, as at random places nearby.
func psfFlux(v []float64, cx, cy, sigma float64) (flux, ferr float64, ok bool) {
	if v == nil || !(sigma > 0) {
		return 0, 0, false
	}
	ps := psfSumsOf(v, cx, cy, sigma)
	noise := robustStd(ps.ring)
	if !(ps.sw2 > 0) || !(noise > 0) || len(ps.annulus) < psfAnnulusMin {
		return 0, 0, false
	}
	bg := median(ps.annulus)
	bgErr := 1.2533 * noise / math.Sqrt(float64(len(ps.annulus)))
	return (ps.swv - bg*ps.sw) / ps.sw2, math.Hypot(noise/math.Sqrt(ps.sw2), bgErr*ps.sw/ps.sw2), true
}

// psfSums are psfFlux's sums over a stamp: the Gaussian's weights against
// the pixels within trackObjectR, the pixels beyond it, and those of the
// annulus.
type psfSums struct {
	sw2, swv, sw  float64
	ring, annulus []float64
}

func psfSumsOf(v []float64, cx, cy, sigma float64) psfSums {
	n := stampSide
	var ps psfSums
	for k, x := range v {
		if !finite(x) {
			continue
		}
		dx, dy := float64(k%n)-cx, float64(k/n)-cy
		r := math.Hypot(dx, dy)
		if r > trackObjectR {
			ps.ring = append(ps.ring, x)
			if r <= psfAnnulusR {
				ps.annulus = append(ps.annulus, x)
			}
			continue
		}
		g := math.Exp(-r*r/(2*sigma*sigma)) / (2 * math.Pi * sigma * sigma)
		ps.sw2 += g * g
		ps.swv += g * x
		ps.sw += g
	}
	return ps
}

func finishTrack(tp *trackPlan, res *TrackResult) {
	obj, star := newStack(), newStack()
	for k := range tp.rows {
		r := &tp.rows[k]
		sigRef := tp.refSig[r.Filter]
		dv := diffOf(tp.stamps[k], tp.refs[k], r.Sigma, sigRef, true)
		width := r.Sigma
		if !(width > 0) {
			width = sigRef
		}
		cx := stampHalf + r.X - math.Round(r.X)
		cy := stampHalf + r.Y - math.Round(r.Y)
		if f, e, ok := psfFlux(dv, cx, cy, width); ok {
			r.Flux, r.Err, r.Measured = f, e, true
		}
		if !r.Detected || !r.Measured || r.Flux < trackShapeSN*r.Err || tp.stars[k].n == 0 {
			continue
		}
		obj.add(dv, 1/r.Flux, cx-stampHalf, cy-stampHalf)
		st := tp.stars[k]
		star.add(st.sum, 1/float64(st.n), st.fx/float64(st.n), st.fy/float64(st.n))
	}
	res.Subs = tp.rows
	tp.stamps, tp.refs, tp.stars = nil, nil, nil
	if obj.used == 0 {
		return
	}
	og, noise := obj.fit()
	sg, _ := star.fit()
	if !og.ok || !sg.ok || !(noise > 0) {
		return
	}
	oe, se := ellipseOf(og), ellipseOf(sg)
	if oe.Major >= trackWidest {
		res.ShapeNote = "The fit found no clear shape: the light is spread out or mixed with what is left of nearby stars."
		return
	}
	res.Shape = &TrackShape{Frames: obj.used, SNR: og.amp / noise, Object: oe, Star: se, Stars: len(tp.shape)}
}

// stack averages stamps, each scaled, with the mean offset of their
// centres from the middle pixel.
type stack struct {
	sum, cnt []float64
	fx, fy   float64
	used     int
}

func newStack() *stack {
	return &stack{sum: make([]float64, stampSide*stampSide), cnt: make([]float64, stampSide*stampSide)}
}

func (s *stack) add(v []float64, scale, fx, fy float64) {
	for i, x := range v {
		if finite(x) {
			s.sum[i] += x * scale
			s.cnt[i]++
		}
	}
	s.fx += fx
	s.fy += fy
	s.used++
}

func (s *stack) mean() []float64 {
	out := make([]float64, len(s.sum))
	for i := range s.sum {
		out[i] = math.NaN()
		if s.cnt[i] > 0 {
			out[i] = s.sum[i] / s.cnt[i]
		}
	}
	return out
}

// fit fits an elliptical Gaussian at the stack's centre and gives the
// scatter around it.
func (s *stack) fit() (gauss2D, float64) {
	if s.used == 0 {
		return gauss2D{}, 0
	}
	m := s.mean()
	cx, cy := stampHalf+s.fx/float64(s.used), stampHalf+s.fy/float64(s.used)
	var ring []float64
	for k, v := range m {
		dx, dy := float64(k%stampSide)-cx, float64(k/stampSide)-cy
		if r := math.Hypot(dx, dy); r > psfFitR+3 && r < psfFitR+9 && finite(v) {
			ring = append(ring, v)
		}
	}
	return fitGauss2D(m, stampSide, cx, cy, psfFitR), robustStd(ring)
}

func ellipseOf(g gauss2D) Ellipse {
	major, minor, th := g.sa, g.sb, g.th
	if minor > major {
		major, minor, th = minor, major, th+math.Pi/2
	}
	ang := math.Mod(th*180/math.Pi, 180)
	if ang < 0 {
		ang += 180
	}
	return Ellipse{Major: major, Minor: minor, Angle: ang}
}

// stackStars sums the bright stars near a track in one sub, each less its
// sky and scaled to unit light, at their catalogue places moved by the
// sub's fitted shift.
func stackStars(p *discoveries.Plane, shape []*starModel, sx, sy float64) starStack {
	out := starStack{sum: make([]float64, stampSide*stampSide)}
	for _, m := range shape {
		x, y := m.x+sx, m.y+sy
		raw := cutStamp(p, x, y)
		cx, cy := stampHalf+x-math.Round(x), stampHalf+y-math.Round(y)
		var sky []float64
		bad := false
		for k, v := range raw {
			r := math.Hypot(float64(k%stampSide)-cx, float64(k/stampSide)-cy)
			switch {
			case isNaN32(v) && r <= trackObjectR:
				bad = true
			case r > psfFitR+3 && !isNaN32(v):
				sky = append(sky, float64(v))
			}
		}
		bg := median(sky)
		if bad || !finite(bg) {
			continue
		}
		var light float64
		for k, v := range raw {
			if math.Hypot(float64(k%stampSide)-cx, float64(k/stampSide)-cy) <= trackObjectR {
				light += float64(v) - bg
			}
		}
		if !(light > 0) {
			continue
		}
		for k, v := range raw {
			if !isNaN32(v) {
				out.sum[k] += (float64(v) - bg) / light
			}
		}
		out.fx += cx - stampHalf
		out.fy += cy - stampHalf
		out.n++
	}
	return out
}

// darkSpot is the master dark pixels around one sensor place: a
// detection's, or a place near it to compare with.
type darkSpot struct {
	near bool
	ps   []darkPos
}

// darkNear are the offsets of the places compared with each detection.
func darkNear() [][2]int { return [][2]int{{6, 0}, {-6, 0}, {0, 6}, {0, -6}} }

func trackDarks(subs []Sub, tp *trackPlan, need map[darkPos]bool) []darkSpot {
	var out []darkSpot
	for k, r := range tp.rows {
		i := tp.subs[k]
		if !r.Detected || !r.Sensor || subs[i].DarkKey == nil {
			continue
		}
		x, y := int(math.Round(r.SX)), int(math.Round(r.SY))
		spot := func(cx, cy int, near bool) darkSpot {
			d := darkSpot{near: near}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					q := darkPos{*subs[i].DarkKey, cx + dx, cy + dy}
					need[q] = true
					d.ps = append(d.ps, q)
				}
			}
			return d
		}
		out = append(out, spot(x, y, false))
		for _, o := range darkNear() {
			out = append(out, spot(x+o[0], y+o[1], true))
		}
	}
	return out
}

// countDarks counts the detections, and the places near them, where the
// master dark is hot: one of the pixels around is among the dark's
// hottest thousandth, and at least darkHot spreads over its typical level.
func countDarks(spots []darkSpot, levels map[darkPos]float64, tails map[string]float64, res *TrackResult) {
	for _, d := range spots {
		best, seen := math.Inf(-1), false
		limit := darkHot
		if len(d.ps) > 0 {
			limit = math.Max(limit, tails[d.ps[0].key])
		}
		for _, q := range d.ps {
			if v, ok := levels[q]; ok {
				best, seen = math.Max(best, v), true
			}
		}
		if !seen {
			continue
		}
		hot := best >= limit
		if d.near {
			res.NearN++
			if hot {
				res.Near++
			}
			continue
		}
		res.DarkN++
		if hot {
			res.Dark++
		}
	}
}
