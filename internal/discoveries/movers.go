package discoveries

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	moverMinRatio   = 0.3
	moverRepeatPx   = 2.0
	moverPredictPx  = 2.5
	moverMaxGap     = 2
	MoverMinHits    = 5
	moverMaxArcsMin = 6.0
)

type moverDet struct {
	X, Y, Flux, SNR float64
}

// moverDetections finds point sources in one sub minus its reference.
func moverDetections(d *Plane, rms []float32, edge, bright []bool, apSigma float64) []moverDet {
	objs := Extract(d, rms, nil, DetectSigma, 3)
	var out []moverDet
	for _, o := range objs {
		xi, yi := pixelIndex(o.X, d.W), pixelIndex(o.Y, d.H)
		i := yi*d.W + xi
		if edge[i] || bright[i] || o.B/math.Max(o.A, 1e-6) <= moverMinRatio {
			continue
		}
		fl := Aperture(d, o.X, o.Y, apertureR)
		snr := 0.0
		if apSigma > 0 {
			snr = fl / apSigma
		}
		out = append(out, moverDet{X: o.X, Y: o.Y, Flux: fl, SNR: snr})
	}
	return out
}

type hit struct{ sub, idx int }

// linkMovers links detections seen in one sub only into constant-rate
// tracks: each pair of detections at most 2 subs apart and no faster than
// 6"/min predicts a position in every other sub, and a track needs a
// detection within 2.5 px of the prediction in at least MoverMinHits subs.
func linkMovers(times []time.Time, dets [][]moverDet, scale float64) [][]hit {
	n := len(times)
	if n < MoverMinHits {
		return nil
	}
	var all [][2]float64
	var lab []int
	for j, ds := range dets {
		for _, d := range ds {
			all = append(all, [2]float64{d.X, d.Y})
			lab = append(lab, j)
		}
	}
	g := NewGrid(all, moverRepeatPx)
	transient := make([]bool, len(all))
	for i, p := range all {
		subs := map[int]bool{}
		for _, k := range g.Within(p[0], p[1], moverRepeatPx) {
			subs[lab[k]] = true
		}
		transient[i] = len(subs) == 1
	}
	pts := make([][][2]float64, n)
	idx := make([][]int, n)
	k := 0
	for j, ds := range dets {
		for di := range ds {
			if transient[k] {
				pts[j] = append(pts[j], all[k])
				idx[j] = append(idx[j], di)
			}
			k++
		}
	}
	grids := make([]*Grid, n)
	for j := range pts {
		grids[j] = NewGrid(pts[j], 4)
	}
	days := func(a, b int) float64 { return times[b].Sub(times[a]).Hours() / 24 }
	vmax := moverMaxArcsMin / scale * 1440
	seen := map[string]bool{}
	var sets [][]hit
	for a := range n {
		for b := a + 1; b <= min(a+moverMaxGap, n-1); b++ {
			dt := days(a, b)
			if !(dt > 0) {
				continue
			}
			for ia, pa := range pts[a] {
				for _, jb := range grids[b].Within(pa[0], pa[1], vmax*dt) {
					pb := pts[b][jb]
					vx, vy := (pb[0]-pa[0])/dt, (pb[1]-pa[1])/dt
					hs := []hit{{a, ia}, {b, jb}}
					for c := range n {
						if c == a || c == b {
							continue
						}
						t := days(a, c)
						px, py := pa[0]+vx*t, pa[1]+vy*t
						if ic, _ := grids[c].Nearest(px, py, moverPredictPx); ic >= 0 {
							hs = append(hs, hit{c, ic})
						}
					}
					if len(hs) < MoverMinHits {
						continue
					}
					sort.Slice(hs, func(i, j int) bool { return hs[i].sub < hs[j].sub })
					key := hitKey(hs)
					if !seen[key] {
						seen[key] = true
						sets = append(sets, hs)
					}
				}
			}
		}
	}
	sort.Slice(sets, func(i, j int) bool { return len(sets[i]) > len(sets[j]) })
	var keep [][]hit
	for _, s := range sets {
		dup := false
		for _, k := range keep {
			if shared(s, k) >= 3 {
				dup = true
				break
			}
		}
		if !dup {
			keep = append(keep, s)
		}
	}
	out := make([][]hit, len(keep))
	for i, s := range keep {
		out[i] = make([]hit, len(s))
		for j, h := range s {
			out[i][j] = hit{h.sub, idx[h.sub][h.idx]}
		}
	}
	return out
}

func hitKey(hs []hit) string {
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "%d:%d,", h.sub, h.idx)
	}
	return b.String()
}

func shared(a, b []hit) int {
	n := 0
	for _, x := range a {
		if slices.Contains(b, x) {
			n++
		}
	}
	return n
}

// fitTrack fits x and y linearly in time to a track's detections.
func fitTrack(ts []time.Time, xs, ys []float64) (t0 time.Time, x0, y0, vx, vy, resid float64) {
	t0 = ts[0]
	n := float64(len(ts))
	var st, stt, sx, sy, stx, sty float64
	for i, t := range ts {
		d := t.Sub(t0).Hours() / 24
		st += d
		stt += d * d
		sx += xs[i]
		sy += ys[i]
		stx += d * xs[i]
		sty += d * ys[i]
	}
	den := n*stt - st*st
	if den == 0 {
		return t0, xs[0], ys[0], 0, 0, 0
	}
	vx = (n*stx - st*sx) / den
	vy = (n*sty - st*sy) / den
	x0 = (sx - vx*st) / n
	y0 = (sy - vy*st) / n
	var rs float64
	for i, t := range ts {
		d := t.Sub(t0).Hours() / 24
		ex, ey := xs[i]-(x0+vx*d), ys[i]-(y0+vy*d)
		rs += ex*ex + ey*ey
	}
	return t0, x0, y0, vx, vy, math.Sqrt(rs / n)
}
