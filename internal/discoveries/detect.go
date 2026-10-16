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
	DetectSigma  = 5.0
	PromoteSigma = 7.0
	apertureR    = 1.5
	residualR    = 3.0
	edgePixels   = 64
	minAxisRatio = 0.45
	minPixels    = 5
	maxDipole    = 0.6
	// A catalogued star that moved minStarMotionPx or more between the
	// reference and the night leaves a dipole in the difference: Gaia DR3
	// 381120971054786304 (G 16.2, 168 mas/yr) on Andromeda's 2026-10-11
	// night left one of 0.52, read as a new source.
	minStarMotionPx  = 0.05
	movingStarDipole = 0.3
	minResidualFrac  = 0.05
	residualRefSNR   = 20.0
	presenceSigma    = 1.0
	presenceShare    = 0.5
	meanAgreeSigma   = 2.0
	medianShare      = 0.4
	minPromoteSubs   = 6
	maxSingleShare   = 0.5
	gaiaMatchPx      = 1.5
	anchorRadius     = 5.0
	anchorThresh     = 20.0
	anchorIsolated   = 25.0
	anchorCount      = 400
	emptyApertures   = 20000
	emptyMargin      = 200
	zpMinG           = 12.0
	zpMaxG           = 17.0
	astroMaxG        = 18.0
	astroMatchPx     = 3.0
	hfrToSigma       = 1.1774
)

// Star is a catalogue star: its Gaia DR3 position at epoch 2016.0 and its
// proper motion in mas/yr, PMRA along the sky (µα cos δ).
type Star struct {
	RA, Dec, G  float64
	PMRA, PMDec float64
}

// gaiaEpoch is Gaia DR3's reference epoch, J2016.0.
func gaiaEpoch() time.Time { return time.Date(2016, 1, 1, 12, 0, 0, 0, time.UTC) }

// At is the star's position at t, moved by its proper motion.
func (s Star) At(t time.Time) (ra, dec float64) {
	if s.PMRA == 0 && s.PMDec == 0 {
		return s.RA, s.Dec
	}
	yr := t.Sub(gaiaEpoch()).Hours() / 24 / 365.25
	dec = s.Dec + s.PMDec*yr/3.6e6
	ra = s.RA + s.PMRA*yr/3.6e6/math.Cos(s.Dec*deg)
	return math.Mod(ra+360, 360), dec
}

// starsAt moves every star to its position at t.
func starsAt(stars []Star, t time.Time) []Star {
	out := make([]Star, len(stars))
	for i, s := range stars {
		out[i] = s
		out[i].RA, out[i].Dec = s.At(t)
	}
	return out
}

// meanMid is the mean mid-exposure time of subs.
func meanMid(subs []Sub) time.Time {
	if len(subs) == 0 {
		return time.Time{}
	}
	var sum float64
	for _, s := range subs {
		sum += float64(s.Mid().UnixNano())
	}
	return time.Unix(0, int64(sum/float64(len(subs)))).UTC()
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
	// gaiaRef and gaiaNight are Gaia moved to the reference's and the
	// night's mean epochs.
	gaiaRef, gaiaNight []Star
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
	X, Y      float64
	RA, Dec   float64
	Sign      int
	SNR       float64
	DiffFlux  float64
	RefFlux   float64
	Frac      float64
	Dipole    float64
	DeltaMag  *float64
	NightMag  *float64
	RefMag    *float64
	GaiaG     *float64
	GaiaSepPx float64
	// GaiaMotionPx is how far the matched star moved between the
	// reference's and the night's epochs.
	GaiaMotionPx float64
	A, B         float64
	NPix         int
	PresentIn    int
	SubsTested   int
	TopShare     float64
	LowConf      bool
	// SubSharp is how many times sharper than a star the candidate is in
	// the median of the night subs it is in.
	SubSharp float64
	// SkyFixed says the subs it is in put it on sensor pixels more than
	// skyFixedPx apart: its light moves with the sky across the dithers.
	SkyFixed bool
	// Filter is the filter whose own comparison found the change, when
	// the filters combined did not.
	Filter string
	Light  []LightPoint
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
	// Chi is the rms of the residuals in units of the detections' expected
	// position errors.
	Chi    float64
	Stamps *Stamps
}

// Position is the track's fitted pixel at t.
func (t Track) Position(at time.Time) (x, y float64) {
	d := at.Sub(t.T0).Hours() / 24
	return t.X0 + t.VX*d, t.Y0 + t.VY*d
}

type perSubStats struct {
	present, n                  int
	top, meanSigma, medianShare float64
	reason                      string
}

// minFilterSubs is the fewest subs one filter needs for the per-sub checks
// on its own. A change that fails them over all the night's subs may pass
// on one filter's: a star's amplitude and even its sign of change differ
// by colour (II Com, a dwarf nova on IC 3393, was brighter in L and fainter
// in R, G and B). Off a catalogued star only L, which every source shows
// in, may stand alone, so a feature of one colour's reference is not
// taken for something new.
const (
	minFilterSubs  = 4
	broadbandClear = "Luminance"
)

// perSub checks that the night's subs agree with the stack: the change is
// over 1 sigma in at least half of them, no sub holds half of the summed
// significance, their mean is over 2 sigma and their median significance
// at least 0.4 of the mean. Shares are of significance, not flux, so a few
// noisy subs do not outweigh the rest: AT 2025xtl, a nova in M 31's bulge
// in H-a on 2025-10-15, was in 10 of 12 subs at 16 sigma, but the two
// subs of twilight, five times as noisy, held 0.078 and 0.060 of its flux
// against 0.002 to 0.009 in the others.
func perSub(fluxes, errs []float64) perSubStats {
	st := perSubStats{n: len(fluxes)}
	var pos, sum, varSum, sigSum float64
	sigs := make([]float64, len(fluxes))
	for k, v := range fluxes {
		sum += v
		varSum += errs[k] * errs[k]
		sigs[k] = v / math.Max(errs[k], 1e-12)
		sigSum += sigs[k]
		if v > presenceSigma*errs[k] {
			st.present++
		}
		if sigs[k] > 0 {
			pos += sigs[k]
			st.top = math.Max(st.top, sigs[k])
		}
	}
	st.top /= math.Max(pos, 1e-12)
	st.meanSigma = sum / math.Max(math.Sqrt(varSum), 1e-12)
	mean := sigSum / math.Max(float64(len(fluxes)), 1)
	med := median64(sigs)
	if mean > 0 {
		st.medianShare = med / mean
	}
	switch {
	case float64(st.present) < presenceShare*float64(st.n):
		st.reason = "present in too few subs"
	case st.top >= maxSingleShare:
		st.reason = "one sub holds most of the flux"
	case st.meanSigma < meanAgreeSigma:
		st.reason = "subs' mean disagrees"
	case med < medianShare*mean:
		st.reason = "subs' median disagrees"
	}
	return st
}

// filtersOf lists the filters of a candidate's light curve points.
func filtersOf(pts []LightPoint) []string {
	var out []string
	for _, p := range pts {
		if !slices.Contains(out, p.Filter) {
			out = append(out, p.Filter)
		}
	}
	return out
}

const (
	localMinG       = 13.0
	localMaxG       = 18.0
	localIsolated   = 8.0
	localNeighbours = 25
	localMaxStars   = 30000
	localSigma      = 3.5
)

// localFrac is the change of catalogued stars across the field, as a share
// of each star's reference flux, so a candidate on a star is judged
// against the stars around it, and only one on a star: a nova on M 31's
// bulge (AT 2025xtl, H-a, 2025-10-15) added 8.4% to the bulge's light
// there, which the stars' spread of 2.2% would have hidden: a flat or transparency mismatch shifts a
// whole region alike and is not a change of one star. A star must stand
// 3.5 times the robust spread of its 25 nearest neighbours above their
// median: on Caldwell 14's 2026-10-09 night 867 changes on stars were within
// that (their median excess 0.015 against a spread of 0.021), while IC 3393's
// faintest known variable stood 3.75 spreads out. The neighbours are
// those about as bright as the star or brighter where there are enough of
// them: a faint neighbour's change is mostly its own noise, and on North
// America Nebula Panel 2's five-sub night of 2025-12-17 the G 13 to 18
// neighbours' spread of 0.062 hid the 0.15 eclipse of 2MASS
// J21043883+4319143.
type localFrac struct {
	grid *Grid
	vals []float64
	refs []float64
}

// localResiduals measures the fractional change of isolated Gaia stars of
// G 13 to 18 in the difference.
func (in *Input) localResiduals(ref, diff *Plane, edge []bool, res *Result) localFrac {
	var all [][2]float64
	var gs []float64
	for _, s := range in.gaiaRef {
		if s.G > localMaxG+3 {
			continue
		}
		if x, y, ok := in.WCS.PixelOf(s.RA, s.Dec); ok && x >= 0 && y >= 0 && x < float64(ref.W) && y < float64(ref.H) {
			all = append(all, [2]float64{x, y})
			gs = append(gs, s.G)
		}
	}
	near := NewGrid(all, localIsolated)
	var pts [][2]float64
	var vals, refs []float64
	for i, p := range all {
		if gs[i] < localMinG || gs[i] > localMaxG || len(pts) >= localMaxStars {
			continue
		}
		xi, yi := int(p[0]), int(p[1])
		if edge[yi*ref.W+xi] || len(near.Within(p[0], p[1], localIsolated)) > 1 {
			continue
		}
		r := Aperture(ref, p[0], p[1], residualR)
		if !(r > residualRefSNR*res.ApSigma) {
			continue
		}
		pts = append(pts, p)
		vals = append(vals, Aperture(diff, p[0], p[1], residualR)/r)
		refs = append(refs, r)
	}
	return localFrac{grid: NewGrid(pts, 64), vals: vals, refs: refs}
}

const (
	// localMatchedFrac is the faintest a neighbour may be, as a share of
	// the star's reference flux, to count as about as bright: 1.5 mag.
	localMatchedFrac = 0.25
	// localMatchedPx is the farthest a neighbour about as bright may be.
	localMatchedPx = 800.0
)

// around is the median and robust spread of the fractional change of the
// 25 stars nearest (x, y), leaving out the star at (x, y) itself: the
// nearest within 800 px about as bright as its reference flux ref or
// brighter, or else the nearest of any brightness.
func (l localFrac) around(x, y, ref float64) (median, spread float64, ok bool) {
	if l.grid == nil {
		return 0, 0, false
	}
	if ref > 0 {
		if m, s, ok := l.nearest(x, y, localMatchedFrac*ref, localMatchedPx); ok {
			return m, s, true
		}
	}
	return l.nearest(x, y, 0, 1600)
}

func (l localFrac) nearest(x, y, minRef, maxR float64) (median, spread float64, ok bool) {
	for _, r := range []float64{100, 200, 400, 800, 1600} {
		if r > maxR {
			break
		}
		type nb struct{ d, v float64 }
		var ns []nb
		for _, i := range l.grid.Within(x, y, r) {
			p := l.grid.pts[i]
			if d := math.Hypot(p[0]-x, p[1]-y); d > 3 && l.refs[i] >= minRef {
				ns = append(ns, nb{d, l.vals[i]})
			}
		}
		if len(ns) < localNeighbours {
			continue
		}
		sort.Slice(ns, func(a, b int) bool { return ns[a].d < ns[b].d })
		v := make([]float64, localNeighbours)
		for k := range v {
			v[k] = ns[k].v
		}
		m := median64(v)
		return m, 1.4826 * mad(sortedCopy(v), m), true
	}
	return 0, 0, false
}

func sortedCopy(v []float64) []float64 {
	c := slices.Clone(v)
	slices.Sort(c)
	return c
}

// Rejection is a detection a per-sub, dipole or residual cut left out, and
// the numbers behind it.
type Rejection struct {
	X, Y        float64
	Sign        int
	SNR         float64
	Reason      string
	PresentIn   int
	Subs        int
	TopShare    float64
	MeanSigma   float64
	MedianShare float64
	Dipole      float64
	Frac        float64
}

// MoverSub is what one sub gave the moving-object linker: its detections,
// and the transient ones (seen at that spot in no other sub) the linker
// works from.
type MoverSub struct {
	FrameID    int
	Filter     string
	Time       time.Time
	Detections int
	Transient  int
	Points     [][2]float64
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
	MoverSubs          []MoverSub
	Rejected           []Rejection
	DroppedSubs        int
	Start, End         time.Time
	SubDetections      int
	Night, Ref, Diff   *Plane
	Valid              []bool
	Edge               []bool
	FieldRA, FieldDec  float64
	FieldRadius        float64
	SubDepthPerFilter  map[string]float64
	// MoverNull is the linking null test; a track needs more detections
	// than any of its tracks had.
	MoverNull MoverNull
	// ChangeNull is the change null test; a new variable must be stronger
	// than any change it found.
	ChangeNull ChangeNull
}

type prep struct {
	mesh  *Mesh
	scale float32
	rms   float64
	bad   bool
}

const minAnchors = 10

// errBadSub is a sub too cloudy or too far off to scale to its master; it
// is left out.
var errBadSub = errors.New("too few anchor stars")

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
		if pp.bad {
			return nil, prep{}, fmt.Errorf("%w: sub %d", errBadSub, s.FrameID)
		}
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
			if len(ratios) < minAnchors {
				cache.mu.Lock()
				cache.m[s.FrameID] = prep{bad: true}
				cache.mu.Unlock()
				return nil, prep{}, fmt.Errorf("%w: sub %d has %d anchor stars", errBadSub, s.FrameID, len(ratios))
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
		if errors.Is(err, errBadSub) {
			return nil, 0, nil
		}
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
	cache *prepCache
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
	// sharp is the candidate's sharpness in each night sub, and sensor the
	// sensor pixel under it there (NaN where the sub's footprint was not
	// measured), in the order of fluxes.
	sharp  []float64
	sensor [][2]float64
	// off is the light's centroid in each night sub, from the candidate's
	// position.
	off [][2]float64
	// wide measures the subs with the wider aperture: on a bright source
	// the narrow one mostly sees each sub's seeing.
	wide bool
	// refNights are, per filter, the change's light in each reference
	// night's subs minus the reference.
	refNights map[string][]refNight
	// refLight is, per filter, the reference's light in the aperture the
	// change is measured in.
	refLight map[string]float64
}

// Run compares the night with its reference.
func Run(ctx context.Context, in Input) (*Result, error) {
	threads := max(1, in.Threads)
	res := &Result{Funnel: map[string]Funnel{}, PixelScale: in.WCS.PixelScale(), SubDepthPerFilter: map[string]float64{}}
	stacks := in.usable(res)
	if !slices.ContainsFunc(stacks, func(fs *filterStacks) bool { return len(fs.f.Night) >= 2 }) || res.NightSubs < 3 {
		return nil, ErrTooLittle
	}
	var refSubs, nightSubs []Sub
	for _, fs := range stacks {
		refSubs = append(refSubs, fs.f.Ref...)
		nightSubs = append(nightSubs, fs.f.Night...)
	}
	in.gaiaRef, in.gaiaNight = starsAt(in.Gaia, meanMid(refSubs)), starsAt(in.Gaia, meanMid(nightSubs))
	W, H := in.W, in.H
	for _, fs := range stacks {
		var err error
		in.stage(fmt.Sprintf("stacking %s night (%d subs)", fs.f.Filter, len(fs.f.Night)))
		if fs.night, err = ClippedMean(ctx, W, H, len(fs.f.Night), stackLoader(fs.load, fs.f.Night), threads); err != nil {
			return nil, err
		}
		in.stage(fmt.Sprintf("stacking %s reference (%d subs)", fs.f.Filter, len(fs.f.Ref)))
		if fs.ref, err = clippedMean(ctx, W, H, len(fs.f.Ref), stackLoader(fs.load, fs.f.Ref), threads, nightShares(fs.f.Ref)); err != nil {
			return nil, err
		}
		fs.noise = emptyApertureSigma(fs.night, nil, apertureR, 4000)
	}
	in.stage("combining filters")
	night, ref, valid := combine(stacks, W, H)
	res.FieldRA, res.FieldDec = in.WCS.SkyOf(float64(W)/2, float64(H)/2)
	res.FieldRadius = math.Hypot(float64(W), float64(H)) / 2 * res.PixelScale / 3600

	in.stage("tying the plate solution to Gaia")
	fitAstrometry(in.WCS, ref, in.gaiaRef, res)

	in.stage("matching star profiles")
	D, err := difference(night, ref, valid, stacks[0].f.Anchors, threads, res)
	if err != nil {
		return nil, err
	}
	dmesh := Background(D, 64, nil)
	dmesh.Subtract(D)
	edge := edgeMask(valid, W, H, edgePixels)
	res.ApSigma = emptyApertureSigma(D, edge, apertureR, emptyApertures)
	res.NightApSigma = emptyApertureSigma(night, sourceMask(ref, edge), apertureR, emptyApertures)
	if !(res.ApSigma > 0) || !(res.NightApSigma > 0) {
		return nil, errors.New("could not measure the noise: too little sky away from the edges")
	}
	if zp, ok := zeroPoint(ref, in.WCS, in.gaiaRef, edge, apertureR); ok {
		res.ZeroPoint = &zp
		d := zp - 2.5*math.Log10(DetectSigma*res.NightApSigma)
		res.Depth = &d
	}
	for _, fs := range stacks {
		if zp, ok := zeroPoint(fs.ref, in.WCS, in.gaiaRef, edge, apertureR); ok {
			fs.zp = &zp
		}
		if zp, ok := zeroPoint(fs.ref, in.WCS, in.gaiaRef, edge, residualR); ok {
			fs.zpWide = &zp
		}
	}
	bmask := brightMask(in.gaiaNight, in.WCS, W, H)

	in.stage("finding changes")
	cands := in.findChanges(D, dmesh.RMSPlane(), ref, edge, bmask, res)
	fds := in.filterChanges(stacks, cands, valid, edge, bmask, res, threads, minFilterSubs, nil)
	for _, fs := range stacks {
		fs.night = nil
	}
	cands, own := withFilterChanges(cands, fds)

	subs, dets, times, err := in.measureSubs(ctx, stacks, cands, valid, edge, bmask, res)
	if err != nil {
		return nil, err
	}
	if err := in.measureRefNights(ctx, stacks, cands); err != nil {
		return nil, err
	}
	combined, per := splitByFilter(cands, fds)
	local := in.localResiduals(ref, D, edge, res)
	res.Candidates = in.cut(combined, ref, D, local, res)
	in.cutFilters(fds, per, edge, res)
	if res.ChangeNull, err = in.nullChanges(ctx, stacks, edge, bmask, res, threads, own); err != nil {
		return nil, err
	}

	in.stage("linking moving objects")
	links, transient := linkMovers(times, dets, res.PixelScale, validArea(edge))
	res.MoverNull = nullMovers(times, dets, links, res.PixelScale, validArea(edge))
	bright := in.brightStars()
	astroPx := 0.1
	if res.AstroRMSArcsec != nil {
		astroPx = *res.AstroRMSArcsec / res.PixelScale
	}
	for _, hs := range links {
		if len(hs) <= res.MoverNull.MaxHits {
			continue
		}
		tr := in.track(hs, subs, dets, times)
		tr.Chi = trackChi(tr, res.HFRNight/hfrToSigma, astroPx)
		if !onOneStar(tr, bright) && tr.Chi <= maxTrackChi {
			res.Tracks = append(res.Tracks, tr)
		}
	}
	for j, ns := range subs {
		ms := MoverSub{FrameID: ns.sub.FrameID, Filter: ns.sub.Filter, Time: times[j], Detections: len(dets[j])}
		for k, d := range dets[j] {
			if j < len(transient) && transient[j][k] {
				ms.Transient++
				ms.Points = append(ms.Points, [2]float64{d.X, d.Y})
			}
		}
		res.MoverSubs = append(res.MoverSubs, ms)
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

// cutFilters cuts each filter's own changes in its own comparison, and adds
// them, what it rejected and its funnel to res.
func (in *Input) cutFilters(fds []filterDiff, per [][]cand, edge []bool, res *Result) {
	for i := range fds {
		fd := &fds[i]
		lf := in.localResiduals(fd.fs.ref, fd.diff, edge, fd.res)
		res.Candidates = append(res.Candidates, in.cut(per[i], fd.fs.ref, fd.diff, lf, fd.res)...)
		res.Rejected = append(res.Rejected, fd.res.Rejected...)
		for k, v := range fd.res.Funnel {
			res.Funnel[k+" in "+fd.fs.f.Filter] = v
		}
		fd.diff = nil
	}
}

// measureSubs measures every candidate in each of the night's subs minus
// its filter's reference, and finds the point sources moving objects are
// linked from.
func (in *Input) measureSubs(ctx context.Context, stacks []*filterStacks, cands []cand, valid, edge, bmask []bool, res *Result) ([]nightSub, [][]moverDet, []time.Time, error) {
	in.stage("measuring the night's subs")
	var subs []nightSub
	for _, fs := range stacks {
		for _, s := range fs.f.Night {
			if fs.cache.m[s.FrameID].bad {
				res.DroppedSubs++
				continue
			}
			subs = append(subs, nightSub{fs, s})
		}
	}
	sort.Slice(subs, func(a, b int) bool { return subs[a].sub.Start.Before(subs[b].sub.Start) })
	dets := make([][]moverDet, len(subs))
	moverSharp := maxSharpness * psfSharpness(math.Min(res.HFRNight, res.HFRRef)/hfrToSigma)
	times := make([]time.Time, len(subs))
	var midSum float64
	for j, ns := range subs {
		p, _, err := ns.fs.load(ctx, ns.sub)
		if err != nil {
			return nil, nil, nil, err
		}
		sm := footprint(p)
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
		for ci := range cands {
			c := &cands[ci]
			if c.wide {
				lightPoint(c, p, ns, residualR, sigWide, ns.fs.zpWide)
			} else {
				lightPoint(c, p, ns, apertureR, sig, ns.fs.zp)
			}
			c.sharp = append(c.sharp, signedSharpness(p, c.X, c.Y, c.Sign))
			sx, sy := math.NaN(), math.NaN()
			if sm.measured {
				sx, sy = sm.at(c.X, c.Y)
			}
			c.sensor = append(c.sensor, [2]float64{sx, sy})
			c.off = append(c.off, centroidOffset(p, c.X, c.Y, c.Sign))
		}
		if ns.fs.zp != nil && sig > 0 {
			d := *ns.fs.zp - 2.5*math.Log10(DetectSigma*sig)
			res.SubDepthPerFilter[ns.sub.Filter] = math.Max(res.SubDepthPerFilter[ns.sub.Filter], d)
		}
		dets[j] = moverDetections(p, m.RMSPlane(), edge, bmask, sig, moverSharp)
		if sm.ok {
			for k := range dets[j] {
				d := &dets[j][k]
				d.SX, d.SY = sm.at(d.X, d.Y)
				d.Sensor = true
			}
		}
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
	return subs, dets, times, nil
}

// usable keeps the filters with a sub on the night and two in the
// reference. A filter with one sub on the night adds it to the per-sub test,
// though not to the night's stack, which needs two to reject what one sub
// alone holds (with it in, Barnard's E's 2026-10-13 lost 15 of 139 known
// variables): a night of one L pair and an R, G and B sub held an eclipse
// of 2MASS J21043883+4319143 in all five.
func (in *Input) usable(res *Result) []*filterStacks {
	var stacks []*filterStacks
	nightNights, refNights := map[time.Time]bool{}, map[time.Time]bool{}
	for _, f := range in.Filters {
		if len(f.Night) < 1 || len(f.Ref) < 2 || len(f.Anchors) < 10 {
			continue
		}
		cache := &prepCache{m: map[int]prep{}}
		stacks = append(stacks, &filterStacks{f: f, cache: cache, load: in.loader(cache, f)})
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
	gaia := in.newGaiaIndex(W, H)
	sigmaNight := res.HFRNight / hfrToSigma
	sharpLimit := maxSharpness * psfSharpness(math.Max(res.HFRNight, res.HFRRef)/hfrToSigma)
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
			if o.B/math.Max(o.A, 1e-6) <= minAxisRatio || o.NPix < minPixels || o.A >= 4*sigmaNight || sharpness(sd, o.X, o.Y) > sharpLimit {
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
			c.wide = c.RefFlux > residualRefSNR*res.ApSigma*math.Sqrt(float64(res.NightSubs))
			if gi, d, moved := gaia.match(o.X, o.Y); gi >= 0 {
				g := in.Gaia[gi].G
				c.GaiaG, c.GaiaSepPx, c.GaiaMotionPx = &g, d, moved
			}
			cands = append(cands, c)
		}
		res.Funnel[name] = fn
	}
	return cands
}

// maxSharpness is how many times sharper than a star a detection may be.
// Extract finds sources in the 3x3 filtered image, so a defect pixel,
// spread over 2x2 pixels by the registration, passes the pixel count and
// the shape's moments, measured on so few pixels, read round: the hot
// pixels of candidates 13956 and 13767 were 5.6 and 5.3 times sharper than
// the night's stars, the changes of 215 catalogued variables at most 3.0
// (99% under 3.0), and the M 31 nova 1.5.
const maxSharpness = 4.0

// maxSubSharpness is how many times sharper than a star a new source may be
// in the night's subs, at the median of the subs it is in. A defect pixel
// sits sharp in each sub even where registration spreads it in the stack:
// on Pelican Nebula Panel 2's night of 2026-09-14, among 1,650 "new
// sources" that were such pixels, the 12 strongest stood at a median of 3.5
// to 5.3 times a star in their subs (11 over 3), and only 2.8 to 4.0 times
// in the stack; stars there stood at 0.6 to 1.3 times.
const maxSubSharpness = 3.0

// skyFixedPx is how far apart, in sensor pixels, two subs holding a
// source must put it for the night to have dithered under it. The
// footprints read the subs' shifts only to a few pixels: on Pelican Nebula
// Panel 2's night of 2026-09-14, shot without dithers, they put five subs
// up to 4.3 px apart.
const skyFixedPx = 8.0

// subSharpShare is the least share of its brightest sub's light a sub must
// hold for its sharpness to count: on Cygnis Loop Panel 2's night of
// 2026-08-19 a defect pixel was 3 to 7 times a star's sharpness in the two
// subs of one dither position and held a fifth as much light, at a star's
// sharpness, in the others it was "present" in, which held the median down.
const subSharpShare = 0.5

// inSubs measures, over the night subs a candidate is present in, its
// median sharpness against a star's, star, in those holding at least half
// the light of its brightest, and whether it moved with the sky across
// them.
func (c *cand) inSubs(star float64) (sharp float64, skyFixed bool) {
	var s, off []float64
	var at [][2]float64
	top := 0.0
	for _, f := range c.fluxes {
		top = math.Max(top, f)
	}
	for k, f := range c.fluxes {
		if !(f > presenceSigma*c.errs[k]) {
			continue
		}
		if k < len(c.sharp) && f >= subSharpShare*top {
			s = append(s, c.sharp[k])
		}
		if k < len(c.off) && !math.IsNaN(c.off[k][0]) {
			off = append(off, math.Hypot(c.off[k][0], c.off[k][1]))
		}
		if k < len(c.sensor) && !math.IsNaN(c.sensor[k][0]) {
			at = append(at, c.sensor[k])
		}
	}
	if len(s) >= 2 && star > 0 {
		sharp = median64(s) / star
	}
	if len(off) < 2 || median64(off) > skyFixedOffPx {
		return sharp, false
	}
	for i := range at {
		for j := range i {
			if math.Hypot(at[i][0]-at[j][0], at[i][1]-at[j][1]) > skyFixedPx {
				return sharp, true
			}
		}
	}
	return sharp, false
}

// skyFixedOffPx is how far, at the median of its subs, a source's light may
// sit from its place in the stack and still be fixed on the sky: a defect
// pixel walks with each sub's dither, so its light sits off it.
const skyFixedOffPx = 0.7

// centroidOffset is where the light of sign within 2.5 px of (x, y) is
// centred, from (x, y).
func centroidOffset(p *Plane, x, y float64, sign int) [2]float64 {
	xi, yi := int(math.Round(x)), int(math.Round(y))
	var sw, sx, sy float64
	for yy := yi - 3; yy <= yi+3; yy++ {
		for xx := xi - 3; xx <= xi+3; xx++ {
			dx, dy := float64(xx)-x, float64(yy)-y
			if math.Hypot(dx, dy) > 2.5 {
				continue
			}
			v := float64(sign) * float64(p.At(xx, yy))
			if !(v > 0) {
				continue
			}
			sw += v
			sx += v * dx
			sy += v * dy
		}
	}
	if !(sw > 0) {
		return [2]float64{math.NaN(), math.NaN()}
	}
	return [2]float64{sx / sw, sy / sw}
}

// signedSharpness is sharpness on the plane times sign, so a fading is
// measured as the light it lost.
func signedSharpness(p *Plane, x, y float64, sign int) float64 {
	if sign > 0 {
		return sharpness(p, x, y)
	}
	xi, yi := int(math.Round(x)), int(math.Round(y))
	q := NewPlane(5, 5)
	for yy := range 5 {
		for xx := range 5 {
			q.Data[yy*5+xx] = -p.At(xi-2+xx, yi-2+yy)
		}
	}
	return sharpness(q, 2, 2)
}

// sharpness is the brightest pixel within 1 px of (x, y) over the sum of
// the positive pixels within 2 px.
func sharpness(p *Plane, x, y float64) float64 {
	xi, yi := int(math.Round(x)), int(math.Round(y))
	peak, pos := math.Inf(-1), 0.0
	for yy := yi - 2; yy <= yi+2; yy++ {
		for xx := xi - 2; xx <= xi+2; xx++ {
			v := float64(p.At(xx, yy))
			if math.IsNaN(v) {
				continue
			}
			if v > 0 {
				pos += v
			}
			if xx >= xi-1 && xx <= xi+1 && yy >= yi-1 && yy <= yi+1 {
				peak = math.Max(peak, v)
			}
		}
	}
	if !(pos > 0) {
		return 0
	}
	return peak / pos
}

// psfSharpness is the sharpness of a Gaussian star of the given sigma,
// in pixels, centred on a pixel.
func psfSharpness(sigma float64) float64 {
	if !(sigma > 0) {
		return math.Inf(1)
	}
	const n, sub = 11, 8
	p := NewPlane(n, n)
	for y := range n {
		for x := range n {
			var v float64
			for sy := range sub {
				for sx := range sub {
					dx := float64(x-n/2) - 0.5 + (float64(sx)+0.5)/sub
					dy := float64(y-n/2) - 0.5 + (float64(sy)+0.5)/sub
					v += math.Exp(-(dx*dx + dy*dy) / (2 * sigma * sigma))
				}
			}
			p.Data[y*n+x] = float32(v)
		}
	}
	return sharpness(p, n/2, n/2)
}

// gaiaIndex finds the catalogued star at a pixel of the difference.
type gaiaIndex struct {
	grid    *Grid
	from    [][2]float64
	idx     []int
	maxMove float64
}

// newGaiaIndex places each Gaia star at its night position, with where it
// was at the reference's epoch.
func (in *Input) newGaiaIndex(w, h int) gaiaIndex {
	var g gaiaIndex
	var pts [][2]float64
	for i, s := range in.gaiaNight {
		x, y, ok := in.WCS.PixelOf(s.RA, s.Dec)
		if !ok || x <= -10 || y <= -10 || x >= float64(w)+10 || y >= float64(h)+10 {
			continue
		}
		rx, ry, ok := in.WCS.PixelOf(in.gaiaRef[i].RA, in.gaiaRef[i].Dec)
		if !ok {
			rx, ry = x, y
		}
		pts = append(pts, [2]float64{x, y})
		g.from = append(g.from, [2]float64{rx, ry})
		g.idx = append(g.idx, i)
		g.maxMove = math.Max(g.maxMove, math.Hypot(rx-x, ry-y))
	}
	g.grid = NewGrid(pts, 8)
	return g
}

// match returns the Gaia star whose path from the reference's epoch to the
// night's passes nearest (x, y), within gaiaMatchPx, how far from it, and
// how far the star moved; -1 when there is none.
func (g gaiaIndex) match(x, y float64) (star int, sep, moved float64) {
	best, bestD := -1, math.Inf(1)
	for _, i := range g.grid.Within(x, y, gaiaMatchPx+g.maxMove) {
		p, q := g.grid.pts[i], g.from[i]
		if d := segmentDistance(x, y, p[0], p[1], q[0], q[1]); d <= gaiaMatchPx && d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return -1, 0, 0
	}
	p, q := g.grid.pts[best], g.from[best]
	return g.idx[best], bestD, math.Hypot(p[0]-q[0], p[1]-q[1])
}

// movingStar says whether a change is the dipole a catalogued star leaves
// in the difference by moving between the reference and the night: the
// star moved, the difference has a dipole, and the residual the motion
// leaves, about 0.6 of the star's flux times its motion over the star
// profile's sigma, could make the change.
func (c Candidate) movingStar(sigma float64) bool {
	if c.GaiaG == nil || c.GaiaMotionPx < minStarMotionPx || c.Dipole < movingStarDipole || !(sigma > 0) {
		return false
	}
	return 0.6*c.RefFlux*c.GaiaMotionPx/sigma >= 0.5*math.Abs(c.DiffFlux)
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
func (in *Input) cut(cands []cand, ref, diff *Plane, local localFrac, res *Result) []Candidate {
	star := psfSharpness(res.HFRNight / hfrToSigma)
	var out []Candidate
	for _, sign := range []int{1, -1} {
		name := signName(sign)
		fn := res.Funnel[name]
		for i := range cands {
			c := &cands[i]
			if c.Sign != sign {
				continue
			}
			m, st := c.subTest()
			c.PresentIn, c.SubsTested, c.TopShare = st.present, st.n, st.top
			reject := func(reason string) {
				res.Rejected = append(res.Rejected, Rejection{X: c.X, Y: c.Y, Sign: c.Sign, SNR: c.SNR, Reason: reason, PresentIn: st.present, Subs: st.n,
					TopShare: st.top, MeanSigma: st.meanSigma, MedianShare: st.medianShare, Dipole: c.Dipole, Frac: c.Frac})
			}
			if st.reason != "" {
				reject(st.reason)
				continue
			}
			fn.PerSub++
			if c.Dipole > maxDipole {
				reject("dipole")
				continue
			}
			if c.movingStar(math.Max(res.HFRNight, res.HFRRef) / hfrToSigma) {
				reject(fmt.Sprintf("moving star: moved %.2f px since the reference, dipole %.2f", c.GaiaMotionPx, c.Dipole))
				continue
			}
			fn.NotDipole++
			if why := c.residual(ref, local, res); why != "" {
				reject(why)
				continue
			}
			fn.NotResidual++
			if why := m.refNightShift(); why != "" {
				reject(why)
				continue
			}
			if c.Filter != "" {
				if why := c.against(); why != "" {
					reject(why)
					continue
				}
			}
			cc := c.Candidate
			cc.SubSharp, cc.SkyFixed = m.inSubs(star)
			cc.LowConf = cc.SNR < PromoteSigma || c.SubsTested < minPromoteSubs || (c.GaiaG == nil && c.Sign > 0 && cc.SubSharp > maxSubSharpness)
			if cc.LowConf {
				fn.LowConfident++
			} else {
				fn.Promoted++
			}
			ra := Aperture(ref, c.X, c.Y, apertureR)
			if c.RefFlux > 0 && ra > 3*res.NightApSigma {
				if tot := c.RefFlux + c.DiffFlux; tot > 0 {
					dm := -2.5 * math.Log10(tot/c.RefFlux)
					cc.DeltaMag = &dm
				}
			}
			if res.ZeroPoint != nil {
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

// subTest is the candidate as the per-sub test measures it, in its own
// filter's subs for a filter's change, and the test's outcome: over all
// the night's subs, or else over one filter's.
func (c *cand) subTest() (*cand, perSubStats) {
	if c.Filter != "" {
		m := c.only(c.Filter)
		return m, perSub(m.fluxes, m.errs)
	}
	st := perSub(c.fluxes, c.errs)
	if st.reason == "" {
		return c, st
	}
	for _, f := range filtersOf(c.Light) {
		if f != broadbandClear && c.GaiaG == nil {
			continue
		}
		var fl, er []float64
		for k, pt := range c.Light {
			if pt.Filter == f {
				fl, er = append(fl, c.fluxes[k]), append(er, c.errs[k])
			}
		}
		if len(fl) >= minFilterSubs {
			if fs := perSub(fl, er); fs.reason == "" {
				return c, fs
			}
		}
	}
	return c, st
}

// residual says why a change on reference light is the light's
// subtraction residual, or "" when it is not: under 5% of that light, or,
// on a catalogued star, no more than its neighbours about as bright
// changed.
func (c *cand) residual(ref *Plane, local localFrac, res *Result) string {
	if !(c.RefFlux > residualRefSNR*res.ApSigma) {
		return ""
	}
	if c.Frac < minResidualFrac {
		return "star residual"
	}
	if c.GaiaG == nil {
		return ""
	}
	if m, spread, ok := local.around(c.X, c.Y, Aperture(ref, c.X, c.Y, residualR)); ok {
		excess := float64(c.Sign) * (c.DiffFlux/c.RefFlux - m)
		if excess < math.Max(minResidualFrac, localSigma*spread) {
			return fmt.Sprintf("like its neighbours: %+.3f against their %+.3f ± %.3f", c.DiffFlux/c.RefFlux, m, spread)
		}
	}
	return ""
}

// brightStars indexes the pixel positions, at the night's epoch, of the
// Gaia stars bright enough to leave subtraction residuals.
func (in *Input) brightStars() *Grid {
	var pts [][2]float64
	for _, s := range in.gaiaNight {
		if s.G > starResidualG {
			continue
		}
		if x, y, ok := in.WCS.PixelOf(s.RA, s.Dec); ok {
			pts = append(pts, [2]float64{x, y})
		}
	}
	return NewGrid(pts, 8)
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
	dra := wrapDeg(ra1-ra0) * math.Cos(dec0*deg)
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

// sourceMask marks the edge and every pixel within 3 px of one the
// reference shows over 3 sigma, so empty apertures in a crowded field are
// empty.
func sourceMask(ref *Plane, edge []bool) []bool {
	mesh := Background(ref, 128, nil)
	rms := mesh.RMSPlane()
	w, h := ref.W, ref.H
	out := slices.Clone(edge)
	row := make([]float32, w)
	for y := range h {
		mesh.rowInterp(mesh.Bg, y, row)
		for x := range w {
			v := ref.Data[y*w+x]
			if isNaN32(v) || v-row[x] <= 3*rms[y*w+x] {
				continue
			}
			for yy := max(0, y-3); yy <= min(h-1, y+3); yy++ {
				for xx := max(0, x-3); xx <= min(w-1, x+3); xx++ {
					out[yy*w+xx] = true
				}
			}
		}
	}
	return out
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
