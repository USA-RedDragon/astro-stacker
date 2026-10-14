package discoveries

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"
)

// Tunables of the method, from its trial on IC 3393 Panel 1.
const (
	DetectSigma     = 5.0
	PromoteSigma    = 7.0
	apertureR       = 1.5
	residualR       = 3.0
	edgePixels      = 64
	minAxisRatio    = 0.45
	minPixels       = 5
	maxDipole       = 0.6
	minResidualFrac = 0.05
	residualRefSNR  = 20.0
	presenceSigma   = 1.0
	presenceShare   = 0.5
	meanAgreeSigma  = 2.0
	maxSingleShare  = 0.5
	gaiaMatchPx     = 1.5
	anchorRadius    = 5.0
	anchorThresh    = 20.0
	anchorIsolated  = 25.0
	anchorCount     = 400
	emptyApertures  = 20000
	emptyMargin     = 200
	zpMinG          = 12.0
	zpMaxG          = 17.0
	astroMaxG       = 18.0
	astroMatchPx    = 3.0
	hfrToSigma      = 1.1774
)

// Star is a catalogue star.
type Star struct {
	RA, Dec, G float64
}

// Anchor is a bright isolated star of a filter's master, which every sub of
// that filter is scaled to.
type Anchor struct {
	X, Y, Flux float64
}

// Sub is one registered sub.
type Sub struct {
	FrameID  int
	Filter   string
	Night    time.Time
	Start    time.Time
	Exposure float64
	Weight   float64
	Key      string
}

// Mid is the sub's mid-exposure time.
func (s Sub) Mid() time.Time {
	return s.Start.Add(time.Duration(s.Exposure / 2 * float64(time.Second)))
}

// FilterInput is one filter's subs and its master's anchor stars.
type FilterInput struct {
	Filter  string
	Anchors []Anchor
	Night   []Sub
	Ref     []Sub
}

// Input is everything one night's comparison needs.
type Input struct {
	W, H    int
	WCS     *WCS
	Filters []FilterInput
	Gaia    []Star
	Threads int
	// Load returns a registered sub's pixels, NaN where it has no data.
	Load func(ctx context.Context, s Sub) (*Plane, error)
	// Progress, if set, hears each stage as it starts.
	Progress func(stage string)
}

// Funnel counts the detections left after each cut.
type Funnel struct {
	Raw          int `json:"raw"`
	SNR          int `json:"snr"`
	NotEdge      int `json:"not_edge"`
	NotBright    int `json:"not_bright_star"`
	PSFLike      int `json:"psf_like"`
	PerSub       int `json:"per_sub_consistent"`
	NotDipole    int `json:"not_dipole"`
	NotResidual  int `json:"not_star_residual"`
	Promoted     int `json:"promoted"`
	LowConfident int `json:"low_confidence"`
}

// LightPoint is a candidate's brightness in one sub, measured in the sub
// minus its filter's reference.
type LightPoint struct {
	FrameID  int       `json:"frame_id"`
	Filter   string    `json:"filter"`
	Time     time.Time `json:"time"`
	Flux     float64   `json:"flux"`
	FluxErr  float64   `json:"flux_err"`
	Mag      *float64  `json:"mag,omitempty"`
	MagErr   *float64  `json:"mag_err,omitempty"`
	DeltaMag *float64  `json:"delta_mag,omitempty"`
}

// Candidate is a source that changed between the night and the reference.
type Candidate struct {
	X, Y       float64
	RA, Dec    float64
	Sign       int
	SNR        float64
	DiffFlux   float64
	RefFlux    float64
	Frac       float64
	Dipole     float64
	DeltaMag   *float64
	NightMag   *float64
	RefMag     *float64
	GaiaG      *float64
	GaiaSepPx  float64
	A, B       float64
	NPix       int
	PresentIn  int
	SubsTested int
	TopShare   float64
	LowConf    bool
	Light      []LightPoint
}

// TrackPoint is one detection of a moving object.
type TrackPoint struct {
	FrameID int       `json:"frame_id"`
	Filter  string    `json:"filter"`
	Time    time.Time `json:"time"`
	X       float64   `json:"x"`
	Y       float64   `json:"y"`
	RA      float64   `json:"ra"`
	Dec     float64   `json:"dec"`
	Flux    float64   `json:"flux"`
	SNR     float64   `json:"snr"`
	Mag     *float64  `json:"mag,omitempty"`
	MagErr  *float64  `json:"mag_err,omitempty"`
}

// Track is a moving object linked across subs at a constant rate.
type Track struct {
	Points     []TrackPoint
	T0         time.Time
	X0, Y0     float64
	VX, VY     float64
	RateArcmin float64
	PA         float64
	ResidualPx float64
	Stamps     *Stamps
}

// Position is the track's fitted pixel at t.
func (t Track) Position(at time.Time) (x, y float64) {
	d := at.Sub(t.T0).Hours() / 24
	return t.X0 + t.VX*d, t.Y0 + t.VY*d
}

// Result is one night's comparison.
type Result struct {
	NightSubs, RefSubs int
	NightNights        int
	RefNights          int
	Filters            []string
	HFRNight, HFRRef   float64
	Convolved          string
	KernelTiles        int
	KernelSum          float64
	ApSigma            float64
	NightApSigma       float64
	ZeroPoint          *float64
	Depth              *float64
	SubDepth           *float64
	AstroRMSArcsec     *float64
	AstroStars         int
	PixelScale         float64
	Funnel             map[string]Funnel
	Candidates         []Candidate
	Tracks             []Track
	MidTime            time.Time
	Start, End         time.Time
	SubDetections      int
	Night, Ref, Diff   *Plane
	Valid              []bool
	Edge               []bool
	FieldRA, FieldDec  float64
	FieldRadius        float64
	SubDepthPerFilter  map[string]float64
}

type prep struct {
	mesh  *Mesh
	scale float32
	rms   float64
}

type prepCache struct {
	mu sync.Mutex
	m  map[int]prep
}

// ErrTooLittle says the night or its reference has too few subs.
var ErrTooLittle = errors.New("too few subs to compare")

func (in *Input) stage(s string) {
	if in.Progress != nil {
		in.Progress(s)
	}
}

type subLoader func(ctx context.Context, s Sub) (*Plane, prep, error)

// loader returns sub s background subtracted and scaled to its master,
// measuring both on first use.
func (in *Input) loader(cache *prepCache, f FilterInput) subLoader {
	return func(ctx context.Context, s Sub) (*Plane, prep, error) {
		p, err := in.Load(ctx, s)
		if err != nil {
			return nil, prep{}, err
		}
		if p.W != in.W || p.H != in.H {
			return nil, prep{}, fmt.Errorf("sub %d is %dx%d, field %dx%d", s.FrameID, p.W, p.H, in.W, in.H)
		}
		cache.mu.Lock()
		pp, ok := cache.m[s.FrameID]
		cache.mu.Unlock()
		if !ok {
			pp.mesh = Background(p, 128, nil)
			pp.mesh.Subtract(p)
			var ratios []float64
			for _, a := range f.Anchors {
				fl := Aperture(p, a.X, a.Y, anchorRadius)
				if fl > 0 && a.Flux > 0 {
					ratios = append(ratios, a.Flux/fl)
				}
			}
			if len(ratios) < 10 {
				return nil, prep{}, fmt.Errorf("sub %d: only %d anchor stars measured", s.FrameID, len(ratios))
			}
			pp.scale = float32(median64(ratios))
			pp.rms = pp.mesh.GlobalRMS() * float64(pp.scale)
			cache.mu.Lock()
			cache.m[s.FrameID] = pp
			cache.mu.Unlock()
		} else {
			pp.mesh.Subtract(p)
		}
		for i, v := range p.Data {
			if !isNaN32(v) {
				p.Data[i] = v * pp.scale
			}
		}
		return p, pp, nil
	}
}

func stackLoader(load subLoader, subs []Sub) Loader {
	return func(ctx context.Context, i int) (*Plane, float64, error) {
		p, pp, err := load(ctx, subs[i])
		if err != nil {
			return nil, 0, err
		}
		w := 1.0
		if pp.rms > 0 {
			w = 1 / (pp.rms * pp.rms)
		}
		return p, w, nil
	}
}

// Anchors picks a master's bright, unsaturated, isolated stars and their
// fluxes, which its subs are scaled to.
func Anchors(master *Plane) []Anchor {
	mesh := Background(master, 128, nil)
	work := &Plane{W: master.W, H: master.H, Data: slices.Clone(master.Data)}
	mesh.Subtract(work)
	stars := Extract(work, mesh.RMSPlane(), nil, anchorThresh, 5)
	var vals []float32
	for i := 0; i < len(master.Data); i += 7 {
		if v := master.Data[i]; !isNaN32(v) {
			vals = append(vals, v)
		}
	}
	slices.Sort(vals)
	sat := math.Inf(1)
	if len(vals) > 0 {
		sat = float64(vals[min(len(vals)-1, int(0.9999*float64(len(vals))))])
	}
	var keep []Source
	for _, s := range stars {
		if !s.Edge && s.Peak < 0.3*sat {
			keep = append(keep, s)
		}
	}
	keep = isolated(keep, anchorIsolated)
	sort.Slice(keep, func(a, b int) bool { return keep[a].Flux > keep[b].Flux })
	if len(keep) > anchorCount {
		keep = keep[:anchorCount]
	}
	out := make([]Anchor, 0, len(keep))
	for _, s := range keep {
		out = append(out, Anchor{X: s.X, Y: s.Y, Flux: Aperture(work, s.X, s.Y, anchorRadius)})
	}
	return out
}

type filterStacks struct {
	f     FilterInput
	load  subLoader
	night *Plane
	ref   *Plane
	noise float64
	zp    *float64
	// zpWide is the zero point for the wider aperture used on bright
	// sources.
	zpWide *float64
}

type nightSub struct {
	fs  *filterStacks
	sub Sub
}

type cand struct {
	Candidate
	fluxes, errs []float64
	// wide measures the subs with the wider aperture: on a bright source
	// the narrow one mostly sees each sub's seeing.
	wide bool
}

// Run compares the night with its reference.
func Run(ctx context.Context, in Input) (*Result, error) {
	threads := max(1, in.Threads)
	res := &Result{Funnel: map[string]Funnel{}, PixelScale: in.WCS.PixelScale(), SubDepthPerFilter: map[string]float64{}}
	stacks := in.usable(res)
	if len(stacks) == 0 || res.NightSubs < 3 {
		return nil, ErrTooLittle
	}
	W, H := in.W, in.H
	for _, fs := range stacks {
		var err error
		in.stage(fmt.Sprintf("stacking %s night (%d subs)", fs.f.Filter, len(fs.f.Night)))
		if fs.night, err = ClippedMean(ctx, W, H, len(fs.f.Night), stackLoader(fs.load, fs.f.Night), threads); err != nil {
			return nil, err
		}
		in.stage(fmt.Sprintf("stacking %s reference (%d subs)", fs.f.Filter, len(fs.f.Ref)))
		if fs.ref, err = ClippedMean(ctx, W, H, len(fs.f.Ref), stackLoader(fs.load, fs.f.Ref), threads); err != nil {
			return nil, err
		}
		fs.noise = emptyApertureSigma(fs.night, nil, apertureR, 4000)
	}
	in.stage("combining filters")
	night, ref, valid := combine(stacks, W, H)
	res.FieldRA, res.FieldDec = in.WCS.SkyOf(float64(W)/2, float64(H)/2)
	res.FieldRadius = math.Hypot(float64(W), float64(H)) / 2 * res.PixelScale / 3600

	in.stage("tying the plate solution to Gaia")
	fitAstrometry(in.WCS, ref, in.Gaia, res)

	in.stage("matching star profiles")
	D, err := difference(night, ref, valid, stacks[0].f.Anchors, threads, res)
	if err != nil {
		return nil, err
	}
	dmesh := Background(D, 64, nil)
	dmesh.Subtract(D)
	edge := edgeMask(valid, W, H, edgePixels)
	res.ApSigma = emptyApertureSigma(D, edge, apertureR, emptyApertures)
	res.NightApSigma = emptyApertureSigma(night, edge, apertureR, emptyApertures)
	if zp, ok := zeroPoint(ref, in.WCS, in.Gaia, edge, apertureR); ok {
		res.ZeroPoint = &zp
		d := zp - 2.5*math.Log10(DetectSigma*res.NightApSigma)
		res.Depth = &d
	}
	for _, fs := range stacks {
		if zp, ok := zeroPoint(fs.ref, in.WCS, in.Gaia, edge, apertureR); ok {
			fs.zp = &zp
		}
		if zp, ok := zeroPoint(fs.ref, in.WCS, in.Gaia, edge, residualR); ok {
			fs.zpWide = &zp
		}
	}
	bmask := brightMask(in.Gaia, in.WCS, W, H)

	in.stage("finding changes")
	cands := in.findChanges(D, dmesh.RMSPlane(), ref, edge, bmask, res)

	in.stage("measuring the night's subs")
	var subs []nightSub
	for _, fs := range stacks {
		for _, s := range fs.f.Night {
			subs = append(subs, nightSub{fs, s})
		}
	}
	sort.Slice(subs, func(a, b int) bool { return subs[a].sub.Start.Before(subs[b].sub.Start) })
	dets := make([][]moverDet, len(subs))
	times := make([]time.Time, len(subs))
	sigs := make([]float64, len(subs))
	var midSum float64
	for j, ns := range subs {
		p, _, err := ns.fs.load(ctx, ns.sub)
		if err != nil {
			return nil, err
		}
		for i, v := range p.Data {
			r := ns.fs.ref.Data[i]
			if isNaN32(v) || isNaN32(r) || !valid[i] {
				p.Data[i] = float32(math.NaN())
			} else {
				p.Data[i] = v - r
			}
		}
		m := Background(p, 64, nil)
		m.Subtract(p)
		sig := emptyApertureSigma(p, edge, apertureR, 4000)
		sigWide := emptyApertureSigma(p, edge, residualR, 4000)
		sigs[j] = sig
		for ci := range cands {
			if cands[ci].wide {
				lightPoint(&cands[ci], p, ns, residualR, sigWide, ns.fs.zpWide)
			} else {
				lightPoint(&cands[ci], p, ns, apertureR, sig, ns.fs.zp)
			}
		}
		if ns.fs.zp != nil && sig > 0 {
			d := *ns.fs.zp - 2.5*math.Log10(DetectSigma*sig)
			res.SubDepthPerFilter[ns.sub.Filter] = math.Max(res.SubDepthPerFilter[ns.sub.Filter], d)
		}
		dets[j] = moverDetections(p, m.RMSPlane(), edge, bmask, sig)
		res.SubDetections += len(dets[j])
		times[j] = ns.sub.Mid()
		midSum += float64(ns.sub.Mid().UnixNano())
		in.stage(fmt.Sprintf("measured sub %d of %d", j+1, len(subs)))
	}
	res.MidTime = time.Unix(0, int64(midSum/float64(len(subs)))).UTC()
	res.Start, res.End = times[0].UTC(), times[len(times)-1].UTC()
	for _, v := range res.SubDepthPerFilter {
		if res.SubDepth == nil || v > *res.SubDepth {
			res.SubDepth = &v
		}
	}
	res.Candidates = in.cut(cands, ref, D, res)

	in.stage("linking moving objects")
	for _, hs := range linkMovers(times, dets, res.PixelScale) {
		res.Tracks = append(res.Tracks, in.track(hs, subs, dets, times))
	}
	for i := range res.Tracks {
		if i >= maxTrackStamps {
			break
		}
		if err := in.trackStamps(ctx, &res.Tracks[i], subs); err != nil {
			return nil, err
		}
	}
	res.Night, res.Ref, res.Diff, res.Valid, res.Edge = night, ref, D, valid, edge
	return res, nil
}

// usable keeps the filters with enough subs on the night and in the
// reference.
func (in *Input) usable(res *Result) []*filterStacks {
	var stacks []*filterStacks
	nightNights, refNights := map[time.Time]bool{}, map[time.Time]bool{}
	for _, f := range in.Filters {
		if len(f.Night) < 2 || len(f.Ref) < 2 || len(f.Anchors) < 10 {
			continue
		}
		stacks = append(stacks, &filterStacks{f: f, load: in.loader(&prepCache{m: map[int]prep{}}, f)})
		res.NightSubs += len(f.Night)
		res.RefSubs += len(f.Ref)
		res.Filters = append(res.Filters, f.Filter)
		for _, s := range f.Night {
			nightNights[s.Night] = true
		}
		for _, s := range f.Ref {
			refNights[s.Night] = true
		}
	}
	res.NightNights, res.RefNights = len(nightNights), len(refNights)
	return stacks
}

// combine weights each filter by its night stack's noise, the same weights
// for the night and the reference, so star colours cancel.
func combine(stacks []*filterStacks, w, h int) (night, ref *Plane, valid []bool) {
	night, ref = NewPlane(w, h), NewPlane(w, h)
	wsum := make([]float32, w*h)
	for _, fs := range stacks {
		if !(fs.noise > 0) {
			continue
		}
		wf := float32(1 / (fs.noise * fs.noise))
		for i := range night.Data {
			a, b := fs.night.Data[i], fs.ref.Data[i]
			if isNaN32(a) || isNaN32(b) {
				continue
			}
			night.Data[i] += wf * a
			ref.Data[i] += wf * b
			wsum[i] += wf
		}
		fs.night = nil
	}
	valid = make([]bool, w*h)
	for i := range wsum {
		if wsum[i] > 0 {
			night.Data[i] /= wsum[i]
			ref.Data[i] /= wsum[i]
			valid[i] = true
		} else {
			night.Data[i] = float32(math.NaN())
			ref.Data[i] = float32(math.NaN())
		}
	}
	return night, ref, valid
}

// difference blurs the sharper of night and reference to the other's star
// profile and subtracts.
func difference(night, ref *Plane, valid []bool, anchors []Anchor, threads int, res *Result) (*Plane, error) {
	res.HFRNight = medianHFR(night, anchors)
	res.HFRRef = medianHFR(ref, anchors)
	var nz, rz *Plane
	var mr MatchResult
	var ok bool
	if res.HFRNight > res.HFRRef {
		rz, mr, ok = MatchPSF(ref, night, threads)
		nz = night
		res.Convolved = "reference"
	} else {
		nz, mr, ok = MatchPSF(night, ref, threads)
		rz = ref
		res.Convolved = "night"
	}
	if !ok {
		return nil, errors.New("too few stars to match the star profiles")
	}
	res.KernelTiles, res.KernelSum = mr.Tiles, mr.KernelSum
	diff := NewPlane(night.W, night.H)
	for i := range diff.Data {
		if valid[i] && !isNaN32(nz.Data[i]) && !isNaN32(rz.Data[i]) {
			diff.Data[i] = nz.Data[i] - rz.Data[i]
		} else {
			diff.Data[i] = float32(math.NaN())
			valid[i] = false
		}
	}
	return diff, nil
}

// findChanges detects sources brighter and fainter in the difference and
// applies the cuts that need only the stacks.
func (in *Input) findChanges(diff *Plane, drms []float32, ref *Plane, edge, bmask []bool, res *Result) []cand {
	W, H := diff.W, diff.H
	var gaiaPts [][2]float64
	var gaiaIdx []int
	for i, s := range in.Gaia {
		if x, y, ok := in.WCS.PixelOf(s.RA, s.Dec); ok && x > -10 && y > -10 && x < float64(W)+10 && y < float64(H)+10 {
			gaiaPts = append(gaiaPts, [2]float64{x, y})
			gaiaIdx = append(gaiaIdx, i)
		}
	}
	gaiaGrid := NewGrid(gaiaPts, 8)
	sigmaNight := res.HFRNight / hfrToSigma
	var cands []cand
	for _, sign := range []int{1, -1} {
		name := signName(sign)
		sd := &Plane{W: W, H: H, Data: make([]float32, W*H)}
		for i, v := range diff.Data {
			sd.Data[i] = float32(sign) * v
		}
		objs := Extract(sd, drms, nil, DetectSigma, 3)
		fn := Funnel{Raw: len(objs)}
		for _, o := range objs {
			snr := Aperture(sd, o.X, o.Y, apertureR) / res.ApSigma
			if snr < DetectSigma {
				continue
			}
			fn.SNR++
			xi, yi := pixelIndex(o.X, W), pixelIndex(o.Y, H)
			if edge[yi*W+xi] {
				continue
			}
			fn.NotEdge++
			if bmask[yi*W+xi] {
				continue
			}
			fn.NotBright++
			if o.B/math.Max(o.A, 1e-6) <= minAxisRatio || o.NPix < minPixels || o.A >= 4*sigmaNight {
				continue
			}
			fn.PSFLike++
			var c cand
			c.X, c.Y, c.Sign, c.SNR, c.A, c.B, c.NPix = o.X, o.Y, sign, snr, o.A, o.B, o.NPix
			c.RA, c.Dec = in.WCS.SkyOf(o.X, o.Y)
			c.DiffFlux = Aperture(diff, o.X, o.Y, residualR)
			c.RefFlux = Aperture(ref, o.X, o.Y, residualR)
			c.Frac = math.Abs(c.DiffFlux) / math.Max(c.RefFlux, 1e-12)
			c.Dipole = dipole(sd, o.X, o.Y)
			c.wide = c.RefFlux > residualRefSNR*res.ApSigma
			if gi, d := gaiaGrid.Nearest(o.X, o.Y, gaiaMatchPx); gi >= 0 {
				g := in.Gaia[gaiaIdx[gi]].G
				c.GaiaG, c.GaiaSepPx = &g, d
			}
			cands = append(cands, c)
		}
		res.Funnel[name] = fn
	}
	return cands
}

func signName(sign int) string {
	if sign > 0 {
		return "brighter"
	}
	return "fainter"
}

// lightPoint measures a candidate in one sub minus its filter's reference,
// in an aperture of radius r with zero point zp.
func lightPoint(c *cand, d *Plane, ns nightSub, r, sig float64, zp *float64) {
	fl := Aperture(d, c.X, c.Y, r)
	c.fluxes = append(c.fluxes, float64(c.Sign)*fl)
	c.errs = append(c.errs, sig)
	pt := LightPoint{FrameID: ns.sub.FrameID, Filter: ns.sub.Filter, Time: ns.sub.Mid().UTC(), Flux: fl, FluxErr: sig}
	if zp != nil {
		refAp := Aperture(ns.fs.ref, c.X, c.Y, r)
		if tot := refAp + fl; tot > 0 {
			mg := *zp - 2.5*math.Log10(tot)
			me := 1.0857 * sig / tot
			pt.Mag, pt.MagErr = &mg, &me
			if refAp > 3*sig {
				dm := -2.5 * math.Log10(tot/refAp)
				pt.DeltaMag = &dm
			}
		}
	}
	c.Light = append(c.Light, pt)
}

// cut applies the per-sub, dipole and star residual cuts and grades what
// is left.
func (in *Input) cut(cands []cand, ref, diff *Plane, res *Result) []Candidate {
	var out []Candidate
	for _, sign := range []int{1, -1} {
		name := signName(sign)
		fn := res.Funnel[name]
		for i := range cands {
			c := &cands[i]
			if c.Sign != sign {
				continue
			}
			present := 0
			var top, pos, sum, varSum float64
			for k, v := range c.fluxes {
				sum += v
				varSum += c.errs[k] * c.errs[k]
				if v > presenceSigma*c.errs[k] {
					present++
				}
				if v > 0 {
					pos += v
					top = math.Max(top, v)
				}
			}
			c.PresentIn, c.SubsTested = present, len(c.fluxes)
			c.TopShare = top / math.Max(pos, 1e-12)
			if float64(present) < presenceShare*float64(c.SubsTested) || c.TopShare >= maxSingleShare || sum < meanAgreeSigma*math.Sqrt(varSum) {
				continue
			}
			fn.PerSub++
			if c.Dipole > maxDipole {
				continue
			}
			fn.NotDipole++
			if c.RefFlux > residualRefSNR*res.ApSigma && c.Frac < minResidualFrac {
				continue
			}
			fn.NotResidual++
			cc := c.Candidate
			cc.LowConf = cc.SNR < PromoteSigma
			if cc.LowConf {
				fn.LowConfident++
			} else {
				fn.Promoted++
			}
			if c.RefFlux > 0 {
				if tot := c.RefFlux + c.DiffFlux; tot > 0 {
					dm := -2.5 * math.Log10(tot/c.RefFlux)
					cc.DeltaMag = &dm
				}
			}
			if res.ZeroPoint != nil {
				ra := Aperture(ref, c.X, c.Y, apertureR)
				na := ra + Aperture(diff, c.X, c.Y, apertureR)
				if ra > 3*res.NightApSigma {
					m := *res.ZeroPoint - 2.5*math.Log10(ra)
					cc.RefMag = &m
				}
				if na > 3*res.NightApSigma {
					m := *res.ZeroPoint - 2.5*math.Log10(na)
					cc.NightMag = &m
				}
			}
			out = append(out, cc)
		}
		res.Funnel[name] = fn
	}
	return out
}

// track turns linked detections into a fitted track with sky positions
// and magnitudes.
func (in *Input) track(hs []hit, subs []nightSub, dets [][]moverDet, times []time.Time) Track {
	ts := make([]time.Time, 0, len(hs))
	xs := make([]float64, 0, len(hs))
	ys := make([]float64, 0, len(hs))
	var tr Track
	for _, h := range hs {
		d := dets[h.sub][h.idx]
		ns := subs[h.sub]
		pt := TrackPoint{FrameID: ns.sub.FrameID, Filter: ns.sub.Filter, Time: times[h.sub].UTC(), X: d.X, Y: d.Y, Flux: d.Flux, SNR: d.SNR}
		pt.RA, pt.Dec = in.WCS.SkyOf(d.X, d.Y)
		if zp := ns.fs.zp; zp != nil && d.Flux > 0 {
			m := *zp - 2.5*math.Log10(d.Flux)
			me := 1.0857 / math.Max(d.SNR, 1e-6)
			pt.Mag, pt.MagErr = &m, &me
		}
		tr.Points = append(tr.Points, pt)
		ts = append(ts, times[h.sub])
		xs = append(xs, d.X)
		ys = append(ys, d.Y)
	}
	tr.T0, tr.X0, tr.Y0, tr.VX, tr.VY, tr.ResidualPx = fitTrack(ts, xs, ys)
	scale := in.WCS.PixelScale()
	tr.RateArcmin = math.Hypot(tr.VX, tr.VY) * scale / 1440
	ra0, dec0 := in.WCS.SkyOf(tr.X0, tr.Y0)
	ra1, dec1 := in.WCS.SkyOf(tr.X0+tr.VX/24, tr.Y0+tr.VY/24)
	dra := (ra1 - ra0) * math.Cos(dec0*deg)
	tr.PA = math.Mod(math.Atan2(dra, dec1-dec0)/deg+360, 360)
	return tr
}

const maxTrackStamps = 40

// trackStamps cuts the sub at a track's middle detection, its filter's
// reference and their difference.
func (in *Input) trackStamps(ctx context.Context, tr *Track, subs []nightSub) error {
	mid := tr.Points[len(tr.Points)/2]
	for _, ns := range subs {
		if ns.sub.FrameID != mid.FrameID {
			continue
		}
		p, _, err := ns.fs.load(ctx, ns.sub)
		if err != nil {
			return err
		}
		d := NewPlane(p.W, p.H)
		for i, v := range p.Data {
			d.Data[i] = v - ns.fs.ref.Data[i]
		}
		st := Cut(p, ns.fs.ref, d, mid.X, mid.Y)
		tr.Stamps = &st
		return nil
	}
	return nil
}

// pixelIndex rounds a coordinate to a pixel index inside [0, n).
func pixelIndex(v float64, n int) int { return max(0, min(int(math.Round(v)), n-1)) }

// dipole is the deepest negative of a 9x9 patch over its highest positive.
func dipole(p *Plane, x, y float64) float64 {
	xi, yi := int(math.Round(x)), int(math.Round(y))
	mn, mx := math.Inf(1), math.Inf(-1)
	for yy := yi - 4; yy <= yi+4; yy++ {
		for xx := xi - 4; xx <= xi+4; xx++ {
			v := float64(p.At(xx, yy))
			if math.IsNaN(v) {
				continue
			}
			mn, mx = math.Min(mn, v), math.Max(mx, v)
		}
	}
	return -mn / math.Max(mx, 1e-12)
}

// emptyApertureSigma is the clipped scatter of aperture sums at n
// positions spread evenly (a Halton sequence) over the image, which counts
// the noise correlation stacking and resampling leave.
func emptyApertureSigma(p *Plane, exclude []bool, r float64, n int) float64 {
	margin := float64(emptyMargin)
	if float64(p.W) < 3*margin || float64(p.H) < 3*margin {
		margin = 10
	}
	v := make([]float64, 0, n)
	for i := 1; i <= n; i++ {
		x := margin + halton(i, 2)*(float64(p.W)-2*margin)
		y := margin + halton(i, 3)*(float64(p.H)-2*margin)
		xi, yi := int(x), int(y)
		if exclude != nil && exclude[yi*p.W+xi] {
			continue
		}
		if isNaN32(p.Data[yi*p.W+xi]) {
			continue
		}
		v = append(v, Aperture(p, x, y, r))
	}
	_, sd := clippedStd(v)
	return sd
}

func halton(i, base int) float64 {
	f, r := 1.0, 0.0
	for i > 0 {
		f /= float64(base)
		r += f * float64(i%base)
		i /= base
	}
	return r
}

func medianHFR(p *Plane, anchors []Anchor) float64 {
	var v []float64
	for _, a := range anchors {
		if h := HalfFluxRadius(p, a.X, a.Y, 8); h > 0 && !math.IsNaN(h) {
			v = append(v, h)
		}
	}
	return median64(v)
}

// zeroPoint is the median of Gaia G + 2.5 log10(flux) over isolated Gaia
// stars of G 12 to 17, with the detection aperture.
func zeroPoint(p *Plane, wcs *WCS, gaia []Star, edge []bool, r float64) (float64, bool) {
	type pos struct{ x, y, g float64 }
	var ps [][2]float64
	var all []pos
	for _, s := range gaia {
		if s.G > zpMaxG+3 {
			continue
		}
		x, y, ok := wcs.PixelOf(s.RA, s.Dec)
		if !ok || x < 0 || y < 0 || x >= float64(p.W) || y >= float64(p.H) {
			continue
		}
		ps = append(ps, [2]float64{x, y})
		all = append(all, pos{x, y, s.G})
	}
	g := NewGrid(ps, 10)
	var zps []float64
	for _, s := range all {
		if s.g < zpMinG || s.g > zpMaxG {
			continue
		}
		xi, yi := int(s.x), int(s.y)
		if edge != nil && edge[yi*p.W+xi] {
			continue
		}
		if len(g.Within(s.x, s.y, 10)) > 1 {
			continue
		}
		fl := Aperture(p, s.x, s.y, r)
		if fl > 0 {
			zps = append(zps, s.g+2.5*math.Log10(fl))
		}
	}
	if len(zps) < 10 {
		return 0, false
	}
	return median64(zps), true
}

// fitAstrometry ties the plate solution to Gaia: it matches the
// reference's stars to Gaia stars and fits an affine pixel correction.
func fitAstrometry(wcs *WCS, ref *Plane, gaia []Star, res *Result) {
	mesh := Background(ref, 128, nil)
	work := &Plane{W: ref.W, H: ref.H, Data: slices.Clone(ref.Data)}
	mesh.Subtract(work)
	stars := isolated(Extract(work, mesh.RMSPlane(), nil, 20, 5), 15)
	var gp [][2]float64
	for _, s := range gaia {
		if s.G > astroMaxG {
			continue
		}
		if x, y, ok := wcs.PixelOf(s.RA, s.Dec); ok {
			gp = append(gp, [2]float64{x, y})
		}
	}
	if len(gp) == 0 {
		return
	}
	grid := NewGrid(gp, 8)
	type pair struct{ gx, gy, mx, my float64 }
	var pairs []pair
	for _, s := range stars {
		if s.Edge {
			continue
		}
		if i, _ := grid.Nearest(s.X, s.Y, astroMatchPx); i >= 0 {
			if len(grid.Within(s.X, s.Y, 3*astroMatchPx)) == 1 {
				pairs = append(pairs, pair{gp[i][0], gp[i][1], s.X, s.Y})
			}
		}
	}
	if len(pairs) < 20 {
		return
	}
	fit := func(ps []pair, comp func(p pair) float64) ([3]float64, bool) {
		var ata [3][3]float64
		var atb [3]float64
		for _, p := range ps {
			r := [3]float64{1, p.gx, p.gy}
			b := comp(p)
			for i := range 3 {
				atb[i] += r[i] * b
				for j := range 3 {
					ata[i][j] += r[i] * r[j]
				}
			}
		}
		return solve3(ata, atb)
	}
	use := pairs
	var cx, cy [3]float64
	for range 3 {
		var ok1, ok2 bool
		cx, ok1 = fit(use, func(p pair) float64 { return p.mx - p.gx })
		cy, ok2 = fit(use, func(p pair) float64 { return p.my - p.gy })
		if !ok1 || !ok2 {
			return
		}
		var rs []float64
		for _, p := range use {
			dx := p.mx - p.gx - (cx[0] + cx[1]*p.gx + cx[2]*p.gy)
			dy := p.my - p.gy - (cy[0] + cy[1]*p.gx + cy[2]*p.gy)
			rs = append(rs, math.Hypot(dx, dy))
		}
		lim := 3 * 1.4826 * median64(rs)
		var next []pair
		for i, p := range use {
			if rs[i] <= math.Max(lim, 0.3) {
				next = append(next, p)
			}
		}
		if len(next) < 20 {
			break
		}
		use = next
	}
	var sx, sy float64
	for _, p := range use {
		dx := p.mx - p.gx - (cx[0] + cx[1]*p.gx + cx[2]*p.gy)
		dy := p.my - p.gy - (cy[0] + cy[1]*p.gx + cy[2]*p.gy)
		sx += dx * dx
		sy += dy * dy
	}
	n := float64(len(use))
	rms := math.Sqrt((sx+sy)/(2*n)) * wcs.PixelScale()
	wcs.Fix = [6]float64{cx[0], cx[1], cx[2], cy[0], cy[1], cy[2]}
	res.AstroRMSArcsec = &rms
	res.AstroStars = len(use)
}

func solve3(a [3][3]float64, b [3]float64) ([3]float64, bool) {
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	if math.Abs(det) < 1e-300 {
		return [3]float64{}, false
	}
	var x [3]float64
	for c := range 3 {
		m := a
		for r := range 3 {
			m[r][c] = b[r]
		}
		x[c] = (m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])) / det
	}
	return x, true
}
