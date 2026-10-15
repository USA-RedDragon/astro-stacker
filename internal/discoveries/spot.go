package discoveries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// SpotNight is forced photometry at one spot on one night in one filter:
// the night's best subs scaled to their master, less the master, averaged,
// measured in the detection aperture. Limit is the 3 sigma limiting
// magnitude there; Mag is set when the spot shows at 3 sigma or more.
type SpotNight struct {
	Night    time.Time `json:"night"`
	Filter   string    `json:"filter"`
	Subs     int       `json:"subs"`
	Exposure float64   `json:"exposure"`
	Mid      time.Time `json:"mid"`
	Flux     float64   `json:"flux"`
	FluxErr  float64   `json:"flux_err"`
	SNR      float64   `json:"snr"`
	Detected bool      `json:"detected"`
	Mag      *float64  `json:"mag,omitempty"`
	MagErr   *float64  `json:"mag_err,omitempty"`
	Limit    *float64  `json:"limit,omitempty"`
}

// SpotOptions bound how much a spot measurement reads.
type SpotOptions struct {
	// Nights is how many of the most recent nights before Before are
	// measured.
	Nights int
	// PerNight is how many subs, the best first, one night and filter
	// takes.
	PerNight int
	Before   time.Time
}

const (
	spotHalf      = 64
	spotKeepOut   = 8.0
	spotApertures = 400
	spotDetect    = 3.0
)

var errNoMaster = errors.New("no master")

// ErrNoSpot says the spot is outside the target's registered frame.
var ErrNoSpot = errors.New("the spot is outside the target's frame")

type spotMaster struct {
	win     *Plane
	anchors []Anchor
	zp      *float64
}

// SpotNights measures ra, dec on the most recent nights of object before
// opts.Before, per filter.
func (r *Runner) SpotNights(ctx context.Context, object string, ra, dec float64, opts SpotOptions) ([]SpotNight, error) {
	w, wid, hei, err := r.referenceWCS(ctx, object)
	if err != nil {
		return nil, err
	}
	x, y, ok := w.PixelOf(ra, dec)
	if !ok || x < 0 || y < 0 || x >= float64(wid) || y >= float64(hei) {
		return nil, ErrNoSpot
	}
	rows, err := r.subRows(ctx, object)
	if err != nil {
		return nil, err
	}
	type key struct {
		night  time.Time
		filter string
	}
	groups := map[key][]subRow{}
	nightSet := map[time.Time]bool{}
	for _, s := range rows {
		if !s.Night.Before(opts.Before) {
			continue
		}
		groups[key{s.Night, s.Filter}] = append(groups[key{s.Night, s.Filter}], s)
		nightSet[s.Night] = true
	}
	nights := make([]time.Time, 0, len(nightSet))
	for n := range nightSet {
		nights = append(nights, n)
	}
	sort.Slice(nights, func(a, b int) bool { return nights[a].After(nights[b]) })
	if opts.Nights > 0 && len(nights) > opts.Nights {
		nights = nights[:opts.Nights]
	}
	keep := map[time.Time]bool{}
	for _, n := range nights {
		keep[n] = true
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		if keep[k.night] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(a, b int) bool {
		if !keys[a].night.Equal(keys[b].night) {
			return keys[a].night.After(keys[b].night)
		}
		return filterRank(keys[a].filter) < filterRank(keys[b].filter)
	})
	masters := map[string]*spotMaster{}
	var out []SpotNight
	for _, k := range keys {
		m, ok := masters[k.filter]
		if !ok {
			m, err = r.spotMaster(ctx, object, k.filter, w, x, y)
			if errors.Is(err, errNoMaster) {
				m, err = nil, nil
			}
			if err != nil {
				return nil, err
			}
			masters[k.filter] = m
		}
		if m == nil {
			continue
		}
		subs := groups[k]
		sort.SliceStable(subs, func(a, b int) bool { return subs[a].Weight > subs[b].Weight })
		if opts.PerNight > 0 && len(subs) > opts.PerNight {
			subs = subs[:opts.PerNight]
		}
		sn, ok, err := r.spotNight(ctx, m, subs, x, y)
		if err != nil {
			return nil, err
		}
		if ok {
			sn.Night, sn.Filter = k.night, k.filter
			out = append(out, sn)
		}
	}
	return out, nil
}

func (r *Runner) referenceWCS(ctx context.Context, object string) (*WCS, int, int, error) {
	var tr app.TargetReference
	if err := r.db.WithContext(ctx).Where("object = ?", object).First(&tr).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("no registration reference: %w", err)
	}
	if tr.WCS == nil {
		return nil, 0, 0, errors.New("the registration reference has no plate solution")
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*tr.WCS), &cards); err != nil {
		return nil, 0, 0, fmt.Errorf("read the plate solution: %w", err)
	}
	kw := map[string]string{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	var st app.Stack
	if err := r.db.WithContext(ctx).Where("object = ? AND master_key IS NOT NULL AND width > 0", object).First(&st).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("no master: %w", err)
	}
	w, err := ParseWCS(kw, st.Height)
	if err != nil {
		return nil, 0, 0, err
	}
	return w, st.Width, st.Height, nil
}

// spotMaster cuts the master of a filter around the spot and finds its
// anchors and zero point; nil when the filter has no master.
func (r *Runner) spotMaster(ctx context.Context, object, filter string, w *WCS, x, y float64) (*spotMaster, error) {
	var stacks []app.Stack
	if err := r.db.WithContext(ctx).Where("object = ? AND filter = ? AND master_key IS NOT NULL", object, filter).Limit(1).Find(&stacks).Error; err != nil {
		return nil, err
	}
	if len(stacks) == 0 {
		return nil, errNoMaster
	}
	st := stacks[0]
	anchors, err := r.anchorsOf(ctx, st)
	if err != nil {
		return nil, err
	}
	b, err := r.store.Get(ctx, *st.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("fetch the %s master: %w", filter, err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("decode the %s master: %w", filter, err)
	}
	p := PlaneOf(im)
	Background(p, 128, nil).Subtract(p)
	m := &spotMaster{win: window(p, x, y), anchors: anchors}
	ra, dec := w.SkyOf(float64(p.W)/2, float64(p.H)/2)
	radius := math.Hypot(float64(p.W), float64(p.H)) / 2 * w.PixelScale() / 3600 * 1.05
	if gaia, err := r.gaia(ctx, object, ra, dec, radius); err == nil {
		if zp, ok := zeroPoint(p, w, gaia, nil, apertureR); ok {
			m.zp = &zp
		}
	}
	return m, nil
}

// window copies the square of side 2*spotHalf+1 centred on the pixel
// nearest (x, y); pixels off the image are NaN.
func window(p *Plane, x, y float64) *Plane {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	side := 2*spotHalf + 1
	out := NewPlane(side, side)
	for j := range side {
		for i := range side {
			out.Data[j*side+i] = p.At(cx-spotHalf+i, cy-spotHalf+j)
		}
	}
	return out
}

// spotNight averages subs, scaled to the master and less it, around the
// spot and measures it; false when no sub could be scaled.
func (r *Runner) spotNight(ctx context.Context, m *spotMaster, subs []subRow, x, y float64) (SpotNight, bool, error) {
	side := 2*spotHalf + 1
	sum := make([]float64, side*side)
	wts := make([]float64, side*side)
	var sn SpotNight
	var mids []time.Time
	for _, s := range subs {
		p, err := r.loadSub(ctx, Sub{FrameID: s.FrameID, Filter: s.Filter, Key: s.Key})
		if err != nil {
			return sn, false, err
		}
		mesh := Background(p, 128, nil)
		mesh.Subtract(p)
		var ratios []float64
		for _, a := range m.anchors {
			if fl := Aperture(p, a.X, a.Y, anchorRadius); fl > 0 && a.Flux > 0 {
				ratios = append(ratios, a.Flux/fl)
			}
		}
		if len(ratios) < minAnchors {
			continue
		}
		scale := median64(ratios)
		rms := mesh.GlobalRMS() * scale
		wt := 1.0
		if rms > 0 {
			wt = 1 / (rms * rms)
		}
		win := window(p, x, y)
		for i, v := range win.Data {
			mv := m.win.Data[i]
			if isNaN32(v) || isNaN32(mv) {
				continue
			}
			sum[i] += wt * (float64(v)*scale - float64(mv))
			wts[i] += wt
		}
		sn.Subs++
		sn.Exposure += s.Exposure
		if s.DateObs.IsZero() {
			continue
		}
		mids = append(mids, s.DateObs.Add(time.Duration(s.Exposure/2*float64(time.Second))))
	}
	if sn.Subs == 0 {
		return sn, false, nil
	}
	d := NewPlane(side, side)
	for i := range d.Data {
		if wts[i] > 0 {
			d.Data[i] = float32(sum[i] / wts[i])
		} else {
			d.Data[i] = float32(math.NaN())
		}
	}
	c := float64(spotHalf) + (x - math.Round(x))
	cy := float64(spotHalf) + (y - math.Round(y))
	sn.Flux = Aperture(d, c, cy, apertureR)
	var samples []float64
	for i := 1; len(samples) < spotApertures && i < spotApertures*4; i++ {
		ax := 4 + halton(i, 2)*float64(side-8)
		ay := 4 + halton(i, 3)*float64(side-8)
		if math.Hypot(ax-c, ay-cy) < spotKeepOut || isNaN32(d.Data[int(ay)*side+int(ax)]) {
			continue
		}
		samples = append(samples, Aperture(d, ax, ay, apertureR))
	}
	med, sd := clippedStd(samples)
	if !(sd > 0) {
		return sn, false, nil
	}
	sn.Flux -= med
	sn.FluxErr = sd
	sn.SNR = sn.Flux / sd
	sn.Detected = sn.SNR >= spotDetect
	if len(mids) > 0 {
		var t int64
		for _, mt := range mids {
			t += mt.UnixNano() / int64(len(mids))
		}
		sn.Mid = time.Unix(0, t).UTC()
	}
	if m.zp != nil {
		lim := *m.zp - 2.5*math.Log10(spotDetect*sd)
		sn.Limit = &lim
		if sn.Detected {
			mg := *m.zp - 2.5*math.Log10(sn.Flux)
			me := 1.0857 / sn.SNR
			sn.Mag, sn.MagErr = &mg, &me
		}
	}
	return sn, true, nil
}
