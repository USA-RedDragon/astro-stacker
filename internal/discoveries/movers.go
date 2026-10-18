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
	// sensorRepeatPx is how near two detections' sensor pixels must be for
	// them to be one defect of the sensor.
	sensorRepeatPx = 1.5
	// sensorRepeats is how many other subs must have a detection on the
	// same sensor pixel: a hot pixel is in nearly every sub, while a slow
	// asteroid on dithered subs meets its own detections there by chance.
	sensorRepeats = 3
)

type moverDet struct {
	X, Y, Flux, SNR float64
	// SX and SY are the sensor pixel the detection fell on, when the
	// sub's sensorMap is known (Sensor).
	SX, SY float64
	Sensor bool
}

// moverDetections finds point sources in one sub minus its reference,
// leaving out those sharper than sharpLimit, hot pixels, and those with no
// light in the aperture, the ring of a star's subtraction residual.
func moverDetections(d *Plane, rms []float32, edge, bright []bool, apSigma, sharpLimit float64) []moverDet {
	objs := Extract(d, rms, nil, DetectSigma, 3)
	var out []moverDet
	for _, o := range objs {
		xi, yi := pixelIndex(o.X, d.W), pixelIndex(o.Y, d.H)
		i := yi*d.W + xi
		if edge[i] || bright[i] || o.B/math.Max(o.A, 1e-6) <= moverMinRatio || sharpness(d, o.X, o.Y) > sharpLimit {
			continue
		}
		fl := Aperture(d, o.X, o.Y, apertureR)
		if !(fl > 0) || !(apSigma > 0) {
			continue
		}
		out = append(out, moverDet{X: o.X, Y: o.Y, Flux: fl, SNR: fl / apSigma})
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
// 2 px, has a detection within 2 px of on the sky, nor 3 such subs within
// 1.5 px on the sensor, as for a hot pixel the registration walks across
// the sky.
func transients(times []time.Time, dets [][]moverDet, scale float64) ([][2]float64, []int, []bool) {
	var all [][2]float64
	var lab []int
	for j, ds := range dets {
		for _, d := range ds {
			all = append(all, [2]float64{d.X, d.Y})
			lab = append(lab, j)
		}
	}
	var sens [][2]float64
	var sensIdx []int
	for _, ds := range dets {
		for _, d := range ds {
			if d.Sensor {
				sens = append(sens, [2]float64{d.SX, d.SY})
			} else {
				sens = append(sens, [2]float64{math.NaN(), math.NaN()})
			}
		}
	}
	for i, p := range sens {
		if !math.IsNaN(p[0]) {
			sensIdx = append(sensIdx, i)
		}
	}
	sensPts := make([][2]float64, len(sensIdx))
	for k, i := range sensIdx {
		sensPts[k] = sens[i]
	}
	g := NewGrid(all, moverRepeatPx)
	sg := NewGrid(sensPts, sensorRepeatPx)
	transient := make([]bool, len(all))
	staticGap := time.Duration(moverRepeatPx * scale / moverMinArcMin * float64(time.Minute))
	apart := func(i, k int) bool {
		d := times[lab[k]].Sub(times[lab[i]])
		return d >= staticGap || d <= -staticGap
	}
	for i, p := range all {
		transient[i] = true
		for _, k := range g.Within(p[0], p[1], moverRepeatPx) {
			if apart(i, k) {
				transient[i] = false
				break
			}
		}
		if !transient[i] || math.IsNaN(sens[i][0]) {
			continue
		}
		subs := map[int]bool{}
		for _, k := range sg.Within(sens[i][0], sens[i][1], sensorRepeatPx) {
			if apart(i, sensIdx[k]) {
				subs[lab[sensIdx[k]]] = true
			}
		}
		transient[i] = len(subs) < sensorRepeats
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
					if !fastEnough(hs, pts, times, vmin, vmax) {
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

// fastEnough says whether a track's fitted rate, not only its seed pair's,
// is within the rates linked, and it moves at least 4 px from its first
// detection to its last. Noise around a fixed residual, scattered by a
// couple of pixels, gives seed pairs fast enough and other subs' detections
// within 2.5 px of their predictions, so a track of them fits a rate below
// the slowest linked.
func fastEnough(hs []hit, pts [][][2]float64, times []time.Time, vmin, vmax float64) bool {
	ts := make([]time.Time, len(hs))
	xs := make([]float64, len(hs))
	ys := make([]float64, len(hs))
	for i, h := range hs {
		ts[i], xs[i], ys[i] = times[h.sub], pts[h.sub][h.idx][0], pts[h.sub][h.idx][1]
	}
	v := math.Hypot(slope(ts, xs), slope(ts, ys))
	return v >= vmin && v <= vmax && v*ts[len(ts)-1].Sub(ts[0]).Hours()/24 >= 2*moverRepeatPx
}

// starResidualPx and starResidualG bound the stars a track is taken for a
// subtraction residual of: every detection within 5 px, about two star
// widths, of a star of G 17 or brighter, where the night's and the
// reference's star profiles differ most.
const (
	starResidualPx = 5.0
	starResidualG  = 17.0
)

// onBrightStars says whether all of a track's detections lie within 5 px of
// bright catalogued stars, at pixel positions stars: residuals of their
// subtraction, centroids wandering with each sub's seeing or lining up
// along a close pair, not a mover.
func onBrightStars(tr Track, stars *Grid) bool {
	if stars == nil || len(tr.Points) == 0 {
		return false
	}
	for _, p := range tr.Points {
		if i, _ := stars.Nearest(p.X, p.Y, starResidualPx); i < 0 {
			return false
		}
	}
	return true
}

// moverNullShuffles is how many times the null test shuffles the subs.
const moverNullShuffles = 3

// MoverNull is the night's linking null test: its detections, less those
// of the tracks linked, linked again with the subs shuffled in time, so that
// no object moving at a constant rate can link and every track left is
// chance or a defect. Tracks is the
// mean of the shuffles' track counts, MaxHits the most detections one of
// their tracks had.
type MoverNull struct {
	Tracks  float64
	MaxHits int
}

// nullMovers links the night's detections, less those of the tracks
// linked, with the subs shuffled in time.
func nullMovers(times []time.Time, dets [][]moverDet, linked [][]hit, scale, area float64) MoverNull {
	n := len(times)
	var out MoverNull
	if n < MoverMinHits {
		return out
	}
	used := map[hit]bool{}
	for _, hs := range linked {
		for _, h := range hs {
			used[h] = true
		}
	}
	rest := make([][]moverDet, n)
	for j, ds := range dets {
		for k, d := range ds {
			if !used[hit{j, k}] {
				rest[j] = append(rest[j], d)
			}
		}
	}
	dets = rest
	total := 0
	for k := range moverNullShuffles {
		perm := shuffle(n, uint64(k)+1)
		shuffled := make([][]moverDet, n)
		for j := range n {
			shuffled[j] = dets[perm[j]]
		}
		links, _ := linkMovers(times, shuffled, scale, area)
		total += len(links)
		for _, hs := range links {
			out.MaxHits = max(out.MaxHits, len(hs))
		}
	}
	out.Tracks = float64(total) / moverNullShuffles
	return out
}

// shuffle is a permutation of n, the same for the same seed: a
// Fisher-Yates shuffle driven by xorshift64*.
func shuffle(n int, seed uint64) []int {
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	x := seed*0x9E3779B97F4A7C15 | 1
	for i := n - 1; i > 0; i-- {
		x ^= x >> 12
		x ^= x << 25
		x ^= x >> 27
		j := int((x*0x2545F4914F6CDD1D)>>34) % (i + 1)
		perm[i], perm[j] = perm[j], perm[i]
	}
	return perm
}

// trackChi is the rms of a track's residuals from its fitted motion in
// units of each detection's expected position error: its centroid error,
// 1.5 times the star profile's sigma over its SNR, and the plate solution's
// error astroPx.
func trackChi(tr Track, sigmaPx, astroPx float64) float64 {
	if len(tr.Points) == 0 {
		return 0
	}
	var sum float64
	for _, p := range tr.Points {
		x, y := tr.Position(p.Time)
		e := math.Hypot(1.5*sigmaPx/math.Max(p.SNR, 1), astroPx)
		r := math.Hypot(p.X-x, p.Y-y) / math.Max(e, 0.05)
		sum += r * r
	}
	return math.Sqrt(sum / float64(len(tr.Points)))
}

// maxTrackChi is the most a mover's residuals may exceed its detections'
// position errors.
const maxTrackChi = 3.0

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

// slope is the least-squares rate of vs in time, per day.
func slope(ts []time.Time, vs []float64) float64 {
	n := float64(len(ts))
	var st, stt, sv, stv float64
	for i, t := range ts {
		d := t.Sub(ts[0]).Hours() / 24
		st += d
		stt += d * d
		sv += vs[i]
		stv += d * vs[i]
	}
	den := n*stt - st*st
	if den == 0 {
		return 0
	}
	return (n*stv - st*sv) / den
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
