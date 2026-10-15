package period

import (
	"math"
	"regexp"
	"slices"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	errFloor   = 0.012
	fitBinDays = 0.007
	srchBinDay = 0.02
	densityBin = 0.25
)

type obs struct {
	t, y, e float64
	w       float64
	g, s    int
}

type series struct {
	survey, band, key string
	group, idx        int
	raw               []obs
	fit               []obs
	srch              []obs
	dil               float64
	sigma             float64
	level             float64
	relative          bool
	sector            int
}

type group struct {
	key, survey, band string
	series            []int
	dil               float64
	points            int
}

type data struct {
	series []*series
	groups []*group
	t0, t1 float64
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func mad(v []float64, m float64) float64 {
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return 1.4826 * median(d)
}

var sectorRE = regexp.MustCompile(`(?i)\bs(?:ector)?\s*0*(\d{1,3})\b`)

func sectorOf(s lc.Series) int {
	for _, txt := range []string{s.Band, s.Note} {
		if m := sectorRE.FindStringSubmatch(txt); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func prepare(in []lc.Series) *data {
	d := &data{t0: math.Inf(1), t1: math.Inf(-1)}
	byKey := map[string]int{}
	for _, s := range in {
		if s.Survey == lc.Own {
			continue
		}
		ps := cleanPoints(s.Points)
		if len(ps) < 3 {
			continue
		}
		mags := make([]float64, len(ps))
		for i, p := range ps {
			mags[i] = p.Mag
		}
		lvl := median(mags)
		sig := mad(mags, lvl)
		pp := pointScatter(ps)
		if sig < pp {
			sig = pp
		}
		se := &series{survey: s.Survey, band: s.Band, relative: s.Relative,
			level: lvl, sigma: math.Max(sig, 1e-4), dil: s.Dilution, sector: sectorOf(s)}
		if se.dil <= 0 || se.dil > 1 {
			se.dil = 1
		}
		se.key = s.Survey + " " + s.Band
		if s.Survey == lc.TESS {
			se.key = lc.TESS
		}
		for _, p := range ps {
			e := p.Err
			if !(e > 0) || math.IsInf(e, 0) {
				e = pp
			}
			se.raw = append(se.raw, obs{t: p.BJD, y: p.Mag - lvl, e: e})
			d.t0, d.t1 = math.Min(d.t0, p.BJD), math.Max(d.t1, p.BJD)
		}
		gi, ok := byKey[se.key]
		if !ok {
			gi = len(d.groups)
			byKey[se.key] = gi
			d.groups = append(d.groups, &group{key: se.key, survey: s.Survey, band: s.Band, dil: se.dil})
			if s.Survey == lc.TESS {
				d.groups[gi].band = ""
			}
		}
		g := d.groups[gi]
		g.dil = math.Min(g.dil, se.dil)
		se.group, se.idx = gi, len(d.series)
		g.series = append(g.series, se.idx)
		g.points += len(se.raw)
		se.fit = binObs(se.raw, fitBinDays)
		se.srch = binObs(se.raw, srchBinDay)
		densityWeights(se.srch)
		d.series = append(d.series, se)
	}
	return d
}

func cleanPoints(in []lc.Point) []lc.Point {
	out := make([]lc.Point, 0, len(in))
	for _, p := range in {
		if math.IsNaN(p.BJD) || math.IsNaN(p.Mag) || math.IsInf(p.Mag, 0) || math.IsInf(p.BJD, 0) {
			continue
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b lc.Point) int {
		switch {
		case a.BJD < b.BJD:
			return -1
		case a.BJD > b.BJD:
			return 1
		}
		return 0
	})
	return out
}

func pointScatter(ps []lc.Point) float64 {
	if len(ps) < 3 {
		return 0.02
	}
	diffs := make([]float64, 0, len(ps)-1)
	for i := 1; i < len(ps); i++ {
		diffs = append(diffs, ps[i].Mag-ps[i-1].Mag)
	}
	return mad(diffs, median(diffs)) / math.Sqrt2
}

func binObs(in []obs, width float64) []obs {
	if len(in) < 2 {
		return slices.Clone(in)
	}
	gaps := make([]float64, 0, len(in)-1)
	for i := 1; i < len(in); i++ {
		gaps = append(gaps, in[i].t-in[i-1].t)
	}
	if median(gaps) >= width/2 {
		out := slices.Clone(in)
		for i := range out {
			out[i].w = 1
		}
		return out
	}
	var out []obs
	for i := 0; i < len(in); {
		j := i
		start := in[i].t
		var st, sy, se2 float64
		for j < len(in) && in[j].t-start < width {
			st += in[j].t
			sy += in[j].y
			se2 += in[j].e * in[j].e
			j++
		}
		n := float64(j - i)
		out = append(out, obs{t: st / n, y: sy / n, e: math.Sqrt(se2) / n, w: n})
		i = j
	}
	return out
}

func densityWeights(o []obs) {
	counts := map[int64]int{}
	for _, p := range o {
		counts[int64(math.Floor(p.t/densityBin))]++
	}
	for i := range o {
		o[i].w = 1 / float64(counts[int64(math.Floor(o[i].t/densityBin))])
	}
}

func fitErr(e float64) float64 { return math.Sqrt(e*e + errFloor*errFloor) }

func dilute(depth, dil float64) float64 {
	if dil >= 1 || depth <= 0 {
		return depth
	}
	return -2.5 * math.Log10(1-dil*(1-math.Pow(10, -0.4*depth)))
}

func undilute(depth, dil float64) float64 {
	if dil >= 1 || depth <= 0 {
		return depth
	}
	f := (1 - math.Pow(10, -0.4*depth)) / dil
	if f >= 0.999 {
		f = 0.999
	}
	return -2.5 * math.Log10(1-f)
}
