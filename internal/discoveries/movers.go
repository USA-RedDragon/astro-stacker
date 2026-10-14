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
	moverMinArcMin  = 0.15
	moverPairWindow = 35 * time.Minute
	moverMinShare   = 0.16
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

// linkMovers links transient detections into constant-rate tracks. A
// detection is static, and left out, when another sub has one within 2 px
// and is far enough away in time for a mover at the slowest rate linked,
// 0.15"/min, to have gone that far; so the many subs of an interleaved
// L, R, G and B night, a few minutes apart, don't hide an asteroid's
// detections from each other. Each pair of transient detections at most 2
// subs or 35 minutes apart (so a faint mover seen only in the L subs of
// such a night still pairs), moving 0.15 to 6"/min, predicts a position in every other sub,
// and a track needs a detection within 2.5 px of the prediction in at least
// MinHits(n) subs and must move at least 4 px between its first and last.
// transients flattens the subs' detections and marks the ones no other
// sub, far enough away in time for the slowest mover linked to have moved
// 2 px, has a detection within 2 px of.
func transients(times []time.Time, dets [][]moverDet, scale float64) ([][2]float64, []int, []bool) {
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
	staticGap := time.Duration(moverRepeatPx * scale / moverMinArcMin * float64(time.Minute))
	for i, p := range all {
		transient[i] = true
		for _, k := range g.Within(p[0], p[1], moverRepeatPx) {
			if d := times[lab[k]].Sub(times[lab[i]]); d >= staticGap || d <= -staticGap {
				transient[i] = false
				break
			}
		}
	}
	return all, lab, transient
}

const moverFalseTracks = 0.05

// chanceHits is how many subs besides a seed pair's two must agree before a
// track is unlikely to be chance: with the transient detections of every
// sub scattered over area, a prediction lands within 2.5 px of one in a
// sub with probability density times the disc, so the agreeing subs of a
// random pair are about Poisson with the sum of those as mean, and k is the
// least count whose tail, times the pairs tried, is under 0.05 false
// tracks. A crowded Milky Way field with many residuals needs more subs.
func chanceHits(pts [][][2]float64, area float64, pairs int) int {
	if area <= 0 || pairs == 0 {
		return 0
	}
	var lambda float64
	for _, p := range pts {
		lambda += float64(len(p)) / area * math.Pi * moverPredictPx * moverPredictPx
	}
	term := math.Exp(-lambda)
	cdf := term
	for k := 1; k < len(pts); k++ {
		if float64(pairs)*(1-cdf) < moverFalseTracks {
			return k
		}
		term *= lambda / float64(k)
		cdf += term
	}
	return len(pts)
}

// MinHits is how many of a night's n subs a track must be seen in: 5, or
// 16% of the subs on a night of many, as chance alignments of noise grow
// with the subs to pick from. On IC 3393's 51-sub L, R, G and B night chance
// tracks had at most 7 detections and every known asteroid brighter than
// V 19.5 at least 10.
func MinHits(n int) int {
	return max(MoverMinHits, int(math.Ceil(moverMinShare*float64(n))))
}

func linkMovers(times []time.Time, dets [][]moverDet, scale, area float64) ([][]hit, [][]bool) {
	n := len(times)
	if n < MoverMinHits {
		return nil, nil
	}
	all, _, transient := transients(times, dets, scale)
	pts := make([][][2]float64, n)
	idx := make([][]int, n)
	flags := make([][]bool, n)
	k := 0
	for j, ds := range dets {
		for di := range ds {
			flags[j] = append(flags[j], transient[k])
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
	minHits := MinHits(n)
	vmax := moverMaxArcsMin / scale * 1440
	vmin := moverMinArcMin / scale * 1440
	seen := map[string]bool{}
	var sets [][]hit
	pairs := 0
	for a := range n {
		for b := a + 1; b < n && (b <= a+moverMaxGap || times[b].Sub(times[a]) <= moverPairWindow); b++ {
			dt := days(a, b)
			if !(dt > 0) {
				continue
			}
			for ia, pa := range pts[a] {
				for _, jb := range grids[b].Within(pa[0], pa[1], vmax*dt) {
					pairs++
					pb := pts[b][jb]
					vx, vy := (pb[0]-pa[0])/dt, (pb[1]-pa[1])/dt
					if math.Hypot(vx, vy) < vmin {
						continue
					}
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
					if len(hs) < minHits {
						continue
					}
					sort.Slice(hs, func(i, j int) bool { return hs[i].sub < hs[j].sub })
					if math.Hypot(vx, vy)*days(hs[0].sub, hs[len(hs)-1].sub) < 2*moverRepeatPx {
						continue
					}
					key := hitKey(hs)
					if !seen[key] {
						seen[key] = true
						sets = append(sets, hs)
					}
				}
			}
		}
	}
	if need := chanceHits(pts, area, pairs) + 2; need > minHits {
		sets = slices.DeleteFunc(sets, func(s []hit) bool { return len(s) < need })
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
	return out, flags
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

// validArea counts the pixels away from the edges, where detections are
// made.
func validArea(edge []bool) float64 {
	n := 0
	for _, e := range edge {
		if !e {
			n++
		}
	}
	return float64(n)
}
