package ownimage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	apR        = 1.5
	annIn      = 8.0
	annOut     = 14.0
	stampHalf  = 15
	stampSide  = 2*stampHalf + 1
	edgeLook   = 15
	satLook    = 20
	satAtR     = 2.5
	satShare   = 0.95
	satFloor   = 0.8
	noDistance = 99.0
)

// rec is one sub's measurement of one spot.
type rec struct {
	flux, ferr float64
	fluxOK     bool
	comps      []float64
	sigma      float64
	ap, peak   float64
	noise      float64
	apOK       bool
	sx, sy     float64
	sensOK     bool
	edge       float64
	satAt      bool
	satNear    float64
	stamp      []float32
}

// subInfo is what is known of a sub as a whole.
type subInfo struct {
	fm  sensorMap
	sat float64
}

func planeMax(p *discoveries.Plane) float64 {
	m := math.Inf(-1)
	for _, v := range p.Data {
		if !isNaN32(v) && float64(v) > m {
			m = float64(v)
		}
	}
	return m
}

func annulus(p *discoveries.Plane, x, y float64) (bg, noise float64) {
	var v []float64
	for yy := int(y - annOut); yy <= int(y+annOut)+1; yy++ {
		for xx := int(x - annOut); xx <= int(x+annOut)+1; xx++ {
			d := math.Hypot(float64(xx)-x, float64(yy)-y)
			if d < annIn || d > annOut {
				continue
			}
			if q := p.At(xx, yy); !isNaN32(q) {
				v = append(v, float64(q))
			}
		}
	}
	if len(v) < 30 {
		return math.NaN(), math.NaN()
	}
	return median(v), robustStd(v)
}

type shapeFit struct {
	sigma, sx, sy float64
	ok            bool
}

type fitKey struct {
	t *tile
	m *starModel
}

// subFits fits each tile's star width and shift once per sub, and each
// star once per tile, however many spots use them.
type subFits struct {
	p      *discoveries.Plane
	shapes map[*tile]shapeFit
	stars  map[fitKey]fitOut
}

func (f *subFits) shape(t *tile) shapeFit {
	if s, ok := f.shapes[t]; ok {
		return s
	}
	var s shapeFit
	s.sigma, s.sx, s.sy, s.ok = fitShape(f.p, t.shape)
	f.shapes[t] = s
	return s
}

func (f *subFits) star(t *tile, m *starModel) fitOut {
	k := fitKey{t, m}
	if o, ok := f.stars[k]; ok {
		return o
	}
	s := f.shape(t)
	o := fitStar(f.p, m, s.sigma, s.sx, s.sy, true)
	f.stars[k] = o
	return o
}

func measureSpot(p *discoveries.Plane, sp *spotPlan, info subInfo, si int, fits *subFits) *rec {
	cx, cy := int(math.Round(sp.x)), int(math.Round(sp.y))
	if isNaN32(p.At(cx, cy)) {
		return nil
	}
	r := &rec{edge: noDistance, satNear: noDistance}
	if info.fm.ok {
		r.sx, r.sy = info.fm.at(sp.x, sp.y)
		r.sensOK = true
	}
	measureLocal(p, sp, info, r)
	if sh := fits.shape(sp.tile); sh.ok {
		r.sigma = sh.sigma
		o := fits.star(sp.tile, sp.target)
		r.flux, r.ferr, r.fluxOK = o.flux, o.err, o.ok
		r.comps = make([]float64, len(sp.comps))
		for j, c := range sp.comps {
			r.comps[j] = math.NaN()
			if o := fits.star(sp.tile, c); o.ok {
				r.comps[j] = o.flux
			}
		}
	}
	if sp.night[si] || sp.ref[si] {
		bg, _ := annulus(p, sp.x, sp.y)
		r.stamp = make([]float32, stampSide*stampSide)
		for j := range stampSide {
			for i := range stampSide {
				r.stamp[j*stampSide+i] = p.At(cx-stampHalf+i, cy-stampHalf+j) - float32(bg)
			}
		}
	}
	return r
}

func measureLocal(p *discoveries.Plane, sp *spotPlan, info subInfo, r *rec) {
	cx, cy := int(math.Round(sp.x)), int(math.Round(sp.y))
	bg, noise := annulus(p, sp.x, sp.y)
	if finite(bg) {
		hasNaN := false
		peak := math.Inf(-1)
		for yy := cy - 2; yy <= cy+2; yy++ {
			for xx := cx - 2; xx <= cx+2; xx++ {
				if math.Hypot(float64(xx)-sp.x, float64(yy)-sp.y) > apR+0.71 {
					continue
				}
				v := p.At(xx, yy)
				if isNaN32(v) {
					hasNaN = true
					continue
				}
				if math.Hypot(float64(xx)-sp.x, float64(yy)-sp.y) <= apR {
					peak = math.Max(peak, float64(v)-bg)
				}
			}
		}
		if !hasNaN {
			r.ap = discoveries.Aperture(p, sp.x, sp.y, apR) - bg*math.Pi*apR*apR
			r.peak, r.noise, r.apOK = peak, noise, true
		}
	}
	satOn := info.sat >= satFloor
	for yy := cy - satLook; yy <= cy+satLook; yy++ {
		for xx := cx - satLook; xx <= cx+satLook; xx++ {
			d := math.Hypot(float64(xx)-sp.x, float64(yy)-sp.y)
			v := p.At(xx, yy)
			if isNaN32(v) {
				if d <= edgeLook {
					r.edge = math.Min(r.edge, d)
				}
				continue
			}
			if satOn && float64(v) >= satShare*info.sat {
				r.satNear = math.Min(r.satNear, d)
				if d <= satAtR {
					r.satAt = true
				}
			}
		}
	}
}

// stream reads every sub once, threads at a time, and measures every spot
// in it.
func stream(ctx context.Context, d Data, subs []Sub, plans []*spotPlan, threads int) error {
	need := make([]bool, len(subs))
	for i := range subs {
		for _, sp := range plans {
			if sp.inFrame {
				need[i] = true
				break
			}
		}
	}
	jobs := make(chan int)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for range max(threads, 1) {
		wg.Go(func() {
			for i := range jobs {
				p, err := d.Load(ctx, subs[i].Key)
				if err != nil {
					if ctx.Err() != nil {
						mu.Lock()
						firstErr = errors.Join(firstErr, ctx.Err())
						mu.Unlock()
						continue
					}
					slog.Warn("Could not read a sub for vetting", "key", subs[i].Key, "error", err)
					continue
				}
				info := subInfo{fm: footprint(p), sat: planeMax(p)}
				fits := &subFits{p: p, shapes: map[*tile]shapeFit{}, stars: map[fitKey]fitOut{}}
				for _, sp := range plans {
					if !sp.inFrame {
						continue
					}
					r := measureSpot(p, sp, info, i, fits)
					mu.Lock()
					sp.recs[i] = r
					mu.Unlock()
				}
			}
		})
	}
	for i := range subs {
		if !need[i] {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil || ctx.Err() != nil {
		return fmt.Errorf("measure the subs: %w", errors.Join(firstErr, ctx.Err()))
	}
	return nil
}
