package ownimage

import (
	"fmt"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// TrackFlags are the moving object checks that call it an artifact.
type TrackFlags struct {
	HotPixel bool `json:"hot_pixel"`
	NotStar  bool `json:"not_star"`
	Flash    bool `json:"flash"`
	Faint    bool `json:"faint"`
}

const (
	trackMinFrames = 3
	trackSeenSN    = 3.0
	// trackSeenShare is the least share of its detections, and
	// trackCrossShare of the frames it crossed, a moving object must show
	// in when measured again: every one of 62 known asteroids of signal to
	// noise 4 to 80 showed in all its detections and at least 62% of the
	// frames it crossed.
	trackSeenShare  = 0.6
	trackCrossShare = 0.5
	trackCrossMin   = 5
	trackSamePixel  = 1.5
	// trackSensorShare is how small a part of its motion across the
	// frames a track may make on the sensor before the two cannot be told
	// apart.
	trackSensorShare = 0.25
	trackMinMove     = 3.0
	trackFlashShare  = 0.6
	trackMinShapeSN  = 5.0
)

// TrackChecks judges a measured track of nPoints detections at signal to
// noise snrs: rate is its speed in arcsec per minute, scale the frame's
// arcsec per pixel and angle its direction on the frame in degrees from
// +x toward +y.
func TrackChecks(res *TrackResult, nPoints int, snrs []float64, rate, scale, angle float64) ([]check.Check, TrackFlags) {
	var f TrackFlags
	frames := trackFrames(res, nPoints, snrs, &f)
	sensor := trackSensor(res, &f)
	shape := trackShape(res, rate, scale, angle, &f)
	steady := trackSteady(res, &f)
	return []check.Check{frames, sensor, shape, steady}, f
}

func detected(res *TrackResult) []TrackSub {
	var out []TrackSub
	for _, s := range res.Subs {
		if s.Detected {
			out = append(out, s)
		}
	}
	return out
}

func trackFrames(res *TrackResult, nPoints int, snrs []float64, f *TrackFlags) check.Check {
	lo, hi, med := math.NaN(), math.NaN(), median(snrs)
	if len(snrs) > 0 {
		s := append([]float64(nil), snrs...)
		sort.Float64s(s)
		lo, hi = s[0], s[len(s)-1]
	}
	ev := fmt.Sprintf("Found in %d frames, at signal to noise %.0f to %.0f (typically %.0f).", nPoints, lo, hi, med)
	if nPoints == 0 {
		return check.Skipped(check.MoverFrames, "Not measured. The track has no detections stored.")
	}
	dets := detected(res)
	measured, seen := 0, 0
	for _, s := range dets {
		if s.Measured {
			measured++
			if s.Flux >= trackSeenSN*s.Err {
				seen++
			}
		}
	}
	if measured < trackMinFrames {
		if nPoints < trackMinFrames {
			f.Faint = true
			return check.New(check.MoverFrames, check.Fail, ev+fmt.Sprintf(" At least %d are needed.", trackMinFrames))
		}
		return check.New(check.MoverFrames, check.Pass, ev+" It could not be measured again against the masters.")
	}
	again := fmt.Sprintf(" Measured again against the masters, %d of the %d show it at signal to noise %.0f or more.", seen, measured, trackSeenSN)
	if seen < trackMinFrames || float64(seen) < trackSeenShare*float64(measured) {
		f.Faint = true
		return check.New(check.MoverFrames, check.Fail, ev+again+" A real object found in a frame shows again there when measured.")
	}
	return check.New(check.MoverFrames, check.Pass, ev+again)
}

func trackSensor(res *TrackResult, f *TrackFlags) check.Check {
	var xs, ys, sx, sy, us, vs []float64
	var ts []float64
	for _, s := range detected(res) {
		if !s.Sensor {
			continue
		}
		xs, ys = append(xs, s.X), append(ys, s.Y)
		sx, sy = append(sx, s.SX), append(sy, s.SY)
		us, vs = append(us, s.U), append(vs, s.V)
		ts = append(ts, s.Time.Sub(res.Subs[0].Time).Hours())
	}
	dark, warm := darkWords(res)
	if len(xs) < trackMinFrames {
		return check.Skipped(check.MoverSensor, "Not measured yet. Where the frames sat on the sensor could not be worked out."+dark)
	}
	move := math.Hypot(xs[len(xs)-1]-xs[0], ys[len(ys)-1]-ys[0])
	spread := maxFromMedian(sx, sy)
	shift := maxFromMedian(us, vs) * 2
	onSensor := rmsAbout(sx, sy)
	onSky := lineRMS(ts, xs, ys)
	switch {
	case spread <= trackSamePixel && move >= trackMinMove:
		f.HotPixel = true
		return check.New(check.MoverSensor, check.Fail, fmt.Sprintf("All %d detections fell on the same sensor pixel, within %.1f px, while the frames shifted %.0f px: a hot pixel carried along by the shifting frames.", len(xs), spread, move)+dark)
	case warm:
		f.HotPixel = true
		return check.New(check.MoverSensor, check.Fail, fmt.Sprintf("In %d of its %d detections it sits on one of the master dark's hottest pixels, against %d of %d places 6 px away: leftover hot pixels, a different one in each frame, lined up by chance.",
			res.Dark, res.DarkN, res.Near, res.NearN))
	case onSensor < onSky && spread < move:
		f.HotPixel = true
		return check.New(check.MoverSensor, check.Fail, fmt.Sprintf("The detections keep to one place on the sensor (%.1f px scatter) better than to a straight path on the sky (%.1f px scatter).", onSensor, onSky)+dark)
	}
	crossed := math.Hypot(sx[len(sx)-1]-sx[0], sy[len(sy)-1]-sy[0])
	if spread <= 2*trackSamePixel && crossed < trackSensorShare*move {
		return check.Skipped(check.MoverSensor, fmt.Sprintf("Not clear. It crossed only %.0f px of the sensor while moving %.0f px across the frames: the frames drifted along with it, so the sensor cannot tell it from a hot pixel.", crossed, move)+dark)
	}
	return check.New(check.MoverSensor, check.Pass, fmt.Sprintf("It crossed %.0f px of the sensor while the frames shifted %.0f px, and keeps to its straight path on the sky (%.1f px scatter), not to one sensor pixel.", crossed, shift, onSky)+dark)
}

func frameWord(n int) string {
	if n == 1 {
		return "1 frame"
	}
	return fmt.Sprintf("%d frames", n)
}

// darkWords says how often the detections sit on hot pixels of the
// master dark against places near them, and whether that is far more
// often than chance: three or more, half of them or more, and five times
// the rate nearby.
func darkWords(res *TrackResult) (string, bool) {
	if res.DarkN == 0 || res.NearN == 0 {
		return "", false
	}
	rate := float64(res.Near) / float64(res.NearN)
	share := float64(res.Dark) / float64(res.DarkN)
	warm := res.Dark >= 3 && share >= 0.5 && share >= 5*rate
	return fmt.Sprintf(" One of the master dark's hottest pixels sits under %d of its %d detections, and under %d of %d places 6 px away.", res.Dark, res.DarkN, res.Near, res.NearN), warm
}

func maxFromMedian(xs, ys []float64) float64 {
	mx, my := median(xs), median(ys)
	var best float64
	for i := range xs {
		best = math.Max(best, math.Hypot(xs[i]-mx, ys[i]-my))
	}
	return best
}

func rmsAbout(xs, ys []float64) float64 {
	mx, my := mean(xs), mean(ys)
	var s float64
	for i := range xs {
		s += (xs[i]-mx)*(xs[i]-mx) + (ys[i]-my)*(ys[i]-my)
	}
	return math.Sqrt(s / float64(len(xs)))
}

func lineRMS(ts, xs, ys []float64) float64 {
	fit := func(v []float64) []float64 {
		n := float64(len(ts))
		var st, stt, sv, stv float64
		for i, t := range ts {
			st += t
			stt += t * t
			sv += v[i]
			stv += t * v[i]
		}
		out := make([]float64, len(v))
		den := n*stt - st*st
		if den == 0 {
			m := sv / n
			for i := range v {
				out[i] = v[i] - m
			}
			return out
		}
		b := (n*stv - st*sv) / den
		a := (sv - b*st) / n
		for i, t := range ts {
			out[i] = v[i] - a - b*t
		}
		return out
	}
	rx, ry := fit(xs), fit(ys)
	var s float64
	for i := range rx {
		s += rx[i]*rx[i] + ry[i]*ry[i]
	}
	return math.Sqrt(s / float64(len(rx)))
}

// cov is an ellipse's covariance: xx, xy, yy.
func cov(e Ellipse) [3]float64 {
	th := e.Angle * math.Pi / 180
	c, s := math.Cos(th), math.Sin(th)
	a2, b2 := e.Major*e.Major, e.Minor*e.Minor
	return [3]float64{a2*c*c + b2*s*s, (a2 - b2) * c * s, a2*s*s + b2*c*c}
}

// widthRatios compares two shapes: how many times wider o is than e
// along the direction it is most wider, and along the one it is least,
// from the generalized eigenvalues of their covariances.
func widthRatios(o, e [3]float64) (wide, narrow float64) {
	detE := e[0]*e[2] - e[1]*e[1]
	detO := o[0]*o[2] - o[1]*o[1]
	mid := o[0]*e[2] + o[2]*e[0] - 2*o[1]*e[1]
	disc := math.Sqrt(math.Max(mid*mid-4*detE*detO, 0))
	l1, l2 := (mid+disc)/(2*detE), (mid-disc)/(2*detE)
	return math.Sqrt(math.Max(l1, 0)), math.Sqrt(math.Max(l2, 0))
}

func trackShape(res *TrackResult, rate, scale, angle float64, f *TrackFlags) check.Check {
	sh := res.Shape
	if sh == nil && res.ShapeNote != "" {
		return check.Skipped(check.MoverShape, "Not measured yet. "+res.ShapeNote)
	}
	if sh == nil || !(sh.Star.Minor > 0) {
		return check.Skipped(check.MoverShape, "Not measured yet. It is too faint in the frames, or no bright stars near it could be measured, to compare shapes.")
	}
	if sh.SNR < trackMinShapeSN {
		return check.Skipped(check.MoverShape, fmt.Sprintf("Not measured yet. Summed over %s it is too faint to measure its shape (signal to noise %.1f).", frameWord(sh.Frames), sh.SNR))
	}
	var exp []float64
	for _, s := range detected(res) {
		exp = append(exp, s.Exposure)
	}
	exposure := median(exp)
	trail := 0.0
	if scale > 0 && finite(exposure) {
		trail = rate * exposure / 60 / scale
	}
	want := cov(sh.Star)
	th := angle * math.Pi / 180
	t2 := trail * trail / 12
	want[0] += t2 * math.Cos(th) * math.Cos(th)
	want[1] += t2 * math.Cos(th) * math.Sin(th)
	want[2] += t2 * math.Sin(th) * math.Sin(th)
	wide, narrow := widthRatios(cov(sh.Object), want)
	motion := fmt.Sprintf(" At %.2f arcsec a minute over %.0f s exposures it trails %.1f px, which is allowed for.", rate, exposure, trail)
	stars := fmt.Sprintf("the %d bright stars near it, summed over the same %s", sh.Stars, frameWord(sh.Frames))
	if sh.Frames == 1 {
		stars = fmt.Sprintf("the %d bright stars near it in the same frame", sh.Stars)
	}
	switch {
	case narrow < psfMinRatio:
		f.NotStar = true
		return check.New(check.MoverShape, check.Fail, fmt.Sprintf("It is sharper than %s: %.2f times their width across its narrow side. A hot pixel or a cosmic ray hit looks like this.", stars, narrow)+motion)
	case wide > psfMaxRatio:
		f.NotStar = true
		return check.New(check.MoverShape, check.Fail, fmt.Sprintf("It is %.1f times as wide as %s along its wide side, too spread out for a point of light moving at its speed.", wide, stars)+motion)
	}
	return check.New(check.MoverShape, check.Pass, fmt.Sprintf("It has the shape of %s: %.2f times their width along its wide side and %.2f across, at a signal to noise of %.0f.", stars, wide, narrow, sh.SNR)+motion)
}

// trackSteady looks at every sub from the first detection to the last:
// a real object shows in a row of them, while a flash or noise puts its
// light in one.
func trackSteady(res *TrackResult, f *TrackFlags) check.Check {
	first, last := -1, -1
	for i, s := range res.Subs {
		if s.Detected {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return check.Skipped(check.MoverSteady, "Not measured yet. None of the detections could be measured.")
	}
	window := res.Subs[first : last+1]
	var sns []float64
	measured, seen, run, best := 0, 0, 0, 0
	for _, s := range window {
		if !s.Measured || !(s.Err > 0) {
			run = 0
			continue
		}
		measured++
		sn := s.Flux / s.Err
		sns = append(sns, math.Max(sn, 0))
		if sn >= trackSeenSN {
			seen++
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	if measured < trackMinFrames {
		return check.Skipped(check.MoverSteady, fmt.Sprintf("Not measured yet. Only %d of the %d frames it crossed could be measured against the masters.", measured, len(window)))
	}
	var total, top float64
	for _, v := range sns {
		total += v
		top = math.Max(top, v)
	}
	share := 0.0
	if total > 0 {
		share = top / total
	}
	switch {
	case seen == 0:
		f.Flash = true
		return check.New(check.MoverSteady, check.Fail, fmt.Sprintf("None of the %d frames it crossed shows it at signal to noise %.0f or more: noise, not an object.", measured, trackSeenSN))
	case seen == 1:
		f.Flash = true
		return check.New(check.MoverSteady, check.Fail, fmt.Sprintf("Of the %d frames it crossed, only 1 shows it at signal to noise %.0f or more: noise or a single flash, not an object.", measured, trackSeenSN))
	case share >= trackFlashShare:
		f.Flash = true
		return check.New(check.MoverSteady, check.Fail, fmt.Sprintf("One frame holds %.0f%% of its light over the %d frames it crossed: a flash, like a satellite glint.", 100*share, measured))
	case measured >= trackCrossMin && float64(seen) < trackCrossShare*float64(measured):
		f.Flash = true
		return check.New(check.MoverSteady, check.Fail, fmt.Sprintf("It shows in only %d of the %d frames it crossed; a real object found that often shows in most of them. Noise lined up by chance looks like this.", seen, measured))
	case best < 2:
		f.Flash = true
		return check.New(check.MoverSteady, check.Fail, fmt.Sprintf("It shows in %d of the %d frames it crossed, but never in two in a row.", seen, measured))
	}
	return check.New(check.MoverSteady, check.Pass, fmt.Sprintf("It shows in %d of the %d frames it crossed, %d of them in a row, and no frame holds more than %.0f%% of its light.", seen, measured, best, 100*share))
}
