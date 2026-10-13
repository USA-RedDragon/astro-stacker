package goals

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	BodyComet          = "comet"
	BodyAsteroid       = "asteroid"
	BodyMethod         = 1
	ComaRadiusArcsec   = 30.0
	TailLengthArcsec   = 180.0
	TailHalfAngleDeg   = 20.0
	DefaultAsteroidSNR = 50.0
	MinTrailPx         = 12.0
	PointAperture      = 6
	PointSkyInner      = 8
	PointSkyOuter      = 12
	SaturatedShare     = 0.5
	NightLayout        = time.DateOnly
)

const (
	ReasonNoSubsTonight   = "no subs tonight yet"
	ReasonTonightNotBuilt = "tonight's body-aligned stack is not built yet"
	ReasonSaturated       = "the asteroid is saturated in most subs, so it is brighter than any SNR goal needs"
)

type ComaMasks struct {
	Head, Tail         []bool
	HeadBins, TailBins int
}

func ComaRegions(bw, bh int, headX, headY float64, tail *[2]float64, arcsecPerPx float64) ComaMasks {
	c := ComaMasks{Head: make([]bool, bw*bh), Tail: make([]bool, bw*bh)}
	if !(arcsecPerPx > 0) {
		return c
	}
	coma := ComaRadiusArcsec / arcsecPerPx
	length := TailLengthArcsec / arcsecPerPx
	cosHalf := math.Cos(TailHalfAngleDeg * math.Pi / 180)
	for by := range bh {
		for bx := range bw {
			dx := (float64(bx)+0.5)*NoiseBin - 0.5 - headX
			dy := (float64(by)+0.5)*NoiseBin - 0.5 - headY
			d := math.Hypot(dx, dy)
			switch {
			case d <= coma:
				c.Head[by*bw+bx] = true
				c.HeadBins++
			case tail != nil && d <= length && (dx*tail[0]+dy*tail[1])/d >= cosHalf:
				c.Tail[by*bw+bx] = true
				c.TailBins++
			}
		}
	}
	return c
}

func (c ComaMasks) Union() []bool {
	out := make([]bool, len(c.Head))
	for i := range out {
		out[i] = c.Head[i] || c.Tail[i]
	}
	return out
}

const MinTailBins = 10

type ComaSignal struct {
	Head, Tail float64
	HasTail    bool
	Signal     float64
}

func MeasureComa(res Result, c ComaMasks) (ComaSignal, bool) {
	covered := res.Masks.Covered
	head := selectValues(res.Mean, and(c.Head, covered))
	if len(head) == 0 {
		return ComaSignal{}, false
	}
	out := ComaSignal{Head: median(head) - res.Sky}
	out.Signal = out.Head
	if tail := selectValues(res.Mean, and(c.Tail, covered)); len(tail) >= MinTailBins {
		out.Tail, out.HasTail = median(tail)-res.Sky, true
		out.Signal = (out.Head + out.Tail) / 2
	}
	return out, true
}

type PointFlux struct {
	Flux      float64
	Saturated bool
	OK        bool
}

func ApertureFlux(sub []float32, w, h int, x, y float64, saturation float32) PointFlux {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	if cx-PointSkyOuter < 0 || cy-PointSkyOuter < 0 || cx+PointSkyOuter >= w || cy+PointSkyOuter >= h {
		return PointFlux{}
	}
	var sum float64
	n := 0
	ring := make([]float64, 0, 4*PointSkyOuter*PointSkyOuter)
	for dy := -PointSkyOuter; dy <= PointSkyOuter; dy++ {
		for dx := -PointSkyOuter; dx <= PointSkyOuter; dx++ {
			v := sub[(cy+dy)*w+cx+dx]
			r2 := dx*dx + dy*dy
			switch {
			case r2 <= PointAperture*PointAperture:
				if v >= saturation {
					return PointFlux{Saturated: true}
				}
				if v == 0 || v != v {
					return PointFlux{}
				}
				sum += float64(v)
				n++
			case r2 > PointSkyInner*PointSkyInner && r2 <= PointSkyOuter*PointSkyOuter && v != 0 && v < saturation && v == v:
				ring = append(ring, float64(v))
			}
		}
	}
	if len(ring) < 20 {
		return PointFlux{}
	}
	return PointFlux{Flux: sum - float64(n)*median(ring), OK: true}
}

type PointResult struct {
	SNR       float64
	Flux      float64
	Sigma     float64
	Measured  int
	Saturated int
}

func PointSNR(fluxes []float64, saturated int) (PointResult, error) {
	r := PointResult{Measured: len(fluxes), Saturated: saturated}
	total := len(fluxes) + saturated
	if total < MinSubs {
		return r, ErrInsufficientData
	}
	if float64(saturated) > SaturatedShare*float64(total) {
		return r, nil
	}
	if len(fluxes) < MinSubs {
		return r, ErrInsufficientData
	}
	r.Flux = median(fluxes)
	r.Sigma = mad(fluxes)
	if !(r.Sigma > 0) {
		return r, fmt.Errorf("the asteroid's flux does not vary between subs")
	}
	r.SNR = r.Flux / (r.Sigma / math.Sqrt(float64(len(fluxes))))
	return r, nil
}

func PointSaturated(r PointResult) bool {
	total := r.Measured + r.Saturated
	return total > 0 && float64(r.Saturated) > SaturatedShare*float64(total)
}

func NightOf(t time.Time, longitude float64) time.Time {
	if math.IsNaN(longitude) {
		longitude = 0
	}
	n := t.UTC().Add(time.Duration(longitude / 15 * float64(time.Hour))).Add(-12 * time.Hour)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func BodyKind(kind string) string {
	if kind == BodyAsteroid {
		return BodyAsteroid
	}
	return BodyComet
}

func DefaultBodyGoal(kind, filter string) Goal {
	g := DefaultGoal(filter)
	if BodyKind(kind) == BodyAsteroid {
		g.SNR = DefaultAsteroidSNR
	}
	return g
}

type BodyNightView struct {
	Night          string     `json:"night"`
	Subs           int        `json:"subs"`
	EffectiveHours float64    `json:"effectiveHours"`
	SNR            *float64   `json:"snr"`
	HeadSNR        *float64   `json:"headSnr,omitempty"`
	TailSNR        *float64   `json:"tailSnr,omitempty"`
	Progress       float64    `json:"progress"`
	Done           bool       `json:"done"`
	Plateau        bool       `json:"plateau"`
	Saturated      bool       `json:"saturated"`
	TrailPx        *float64   `json:"trailPx,omitempty"`
	TrailShort     bool       `json:"trailShort"`
	Note           string     `json:"note,omitempty"`
	PreviewKey     string     `json:"previewKey,omitempty"`
	MeasuredAt     *time.Time `json:"measuredAt,omitempty"`
	// ProjectedSNR is the SNR projected from the measured one to the
	// ProjectedSubs taken that night, set while subs taken outrun the
	// measurement.
	ProjectedSNR  *float64 `json:"projectedSnr,omitempty"`
	ProjectedSubs int      `json:"projectedSubs,omitempty"`
}

type BodyStatus struct {
	Kind        string          `json:"kind"`
	Measure     string          `json:"measure"`
	Goal        float64         `json:"goal"`
	Tonight     string          `json:"tonight"`
	TonightSubs int             `json:"tonightSubs"`
	Current     *BodyNightView  `json:"current,omitempty"`
	Nights      []BodyNightView `json:"nights"`
	Done        bool            `json:"done"`
	DoneNight   string          `json:"doneNight,omitempty"`
	MinTrailPx  float64         `json:"minTrailPx"`
}

const (
	CometMeasureText    = "Signal-to-noise of the comet's head and inner tail in that night's stack aligned on the comet: the median of the head within 30 arcseconds and the tail out to 3 arcminutes within 20 degrees of the anti-Sun direction, against the stack's sky noise. Each night is its own image."
	AsteroidMeasureText = "Signal-to-noise of the asteroid in one night's stack aligned on it: its flux in a 6-pixel aperture, against how much that flux varies from sub to sub. One night that reaches the goal, with a trail of at least 12 pixels in the star-aligned stack, finishes it."
)

func bodyMeasurement(n app.BodyNight) app.GoalMeasurement {
	return app.GoalMeasurement{Object: n.Object, Filter: n.Filter, Subs: n.Subs, Levels: n.Levels, EffectiveHours: n.EffectiveHours,
		Signal: n.Signal, NoiseA: n.NoiseA, NoiseB: n.NoiseB, NoiseNow: n.NoiseNow, SNR: n.SNR, GainPerHourPct: n.GainPerHourPct,
		LowConfidence: n.LowConfidence, LowReason: n.LowReason, MeasuredAt: n.MeasuredAt, Error: n.Error}
}

func nightSaturated(n app.BodyNight) bool {
	return n.Error == nil && n.Kind == BodyAsteroid && n.Saturated
}

func trailShort(n app.BodyNight) bool {
	return n.Kind == BodyAsteroid && (n.TrailPx == nil || *n.TrailPx < MinTrailPx)
}

func evaluateNight(kind string, n app.BodyNight, g Goal) Progress {
	g.Kind = KindSNR
	m := bodyMeasurement(n)
	m.SNR = math.Max(0, m.SNR)
	p := Evaluate(m, g)
	if BodyKind(kind) == BodyAsteroid {
		p.Plateau = false
		p.PlateauAt = Plateau{Unmeasured: "an asteroid has no plateau: one good night finishes it", GainPct: PlateauGainPct}
		if nightSaturated(n) {
			p.Progress, p.HoursNeeded, p.LowConfidence, p.LowReason = 1, 0, false, ReasonSaturated
		}
		p.Done = p.Progress >= 1 && (n.SNR > 0 || nightSaturated(n)) && !trailShort(n) && n.Subs >= MinSubs
		if !p.Done && p.Progress >= 1 {
			p.Progress = 0.999
		}
		p.GainPerHourPct = GainPerHour(1, 0, n.EffectiveHours)
	}
	return p
}

func nightView(kind string, n app.BodyNight, g Goal) BodyNightView {
	v := BodyNightView{Night: n.Night.Format(NightLayout), Subs: n.Subs, EffectiveHours: n.EffectiveHours, TrailPx: n.TrailPx, HeadSNR: n.HeadSNR, TailSNR: n.TailSNR}
	if n.PreviewKey != nil {
		v.PreviewKey = *n.PreviewKey
	}
	if n.Error != nil {
		v.Note = BodyErrorText(*n.Error, n.Subs)
		return v
	}
	at := n.MeasuredAt
	v.MeasuredAt = &at
	p := evaluateNight(kind, n, g)
	v.Progress, v.Done, v.Plateau = p.Progress, p.Done, p.Plateau
	v.Saturated = nightSaturated(n)
	if !v.Saturated {
		snr := n.SNR
		v.SNR = &snr
	}
	v.TrailShort = trailShort(n)
	if n.LowReason != "" && !v.Saturated {
		v.Note = n.LowReason
	}
	return v
}

func BodyErrorText(err string, subs int) string {
	if err == ErrInsufficientData.Error() {
		return fmt.Sprintf("%d subs that night; %d are needed to measure", subs, MinSubs)
	}
	return err
}

func bodyCollecting(subs int) Readiness {
	rd := Ready(nil, nil, subs)
	rd.MinSubs = UnmeasurableSubLimit
	rd.Open = subs < rd.SubLimit
	return rd
}

// Project scales a night's measured SNR to the taken subs of that night,
// as SNR grows with the square root of the subs, so a goal can be marked
// done before the next measurement. It returns false when there is nothing
// to project: no SNR, or no subs beyond those measured.
func Project(n app.BodyNight, taken int) (app.BodyNight, bool) {
	if n.Error != nil || n.Subs <= 0 || taken <= n.Subs || !(n.SNR > 0) {
		return n, false
	}
	scale := float64(taken) / float64(n.Subs)
	n.SNR *= math.Sqrt(scale)
	n.EffectiveHours *= scale
	n.Subs = taken
	return n, true
}

func EvaluateBody(kind string, nights []app.BodyNight, g Goal, tonight time.Time, tonightSubs int) (*Progress, Readiness, BodyStatus) {
	return EvaluateBodyTaken(kind, nights, g, tonight, tonightSubs, 0)
}

// EvaluateBodyTaken is EvaluateBody that also projects tonight's measured
// SNR to the taken subs of tonight, which run ahead of the stacked ones,
// and marks the goal done once the projection reaches it. The next
// measurement replaces the projection, and reopens the goal if it fell
// short.
func EvaluateBodyTaken(kind string, nights []app.BodyNight, g Goal, tonight time.Time, tonightSubs, taken int) (*Progress, Readiness, BodyStatus) {
	kind = BodyKind(kind)
	sorted := slices.Clone(nights)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Night.Before(sorted[j].Night) })
	st := BodyStatus{Kind: kind, Goal: g.SNR, Tonight: tonight.Format(NightLayout), TonightSubs: tonightSubs, MinTrailPx: MinTrailPx, Nights: []BodyNightView{}}
	st.Measure = CometMeasureText
	if kind == BodyAsteroid {
		st.Measure = AsteroidMeasureText
	}
	var cur *app.BodyNight
	var doneNight *app.BodyNight
	for i := range sorted {
		n := &sorted[i]
		v := nightView(kind, *n, g)
		st.Nights = append(st.Nights, v)
		if n.Night.Equal(tonight) {
			cur = n
			c := v
			st.Current = &c
		}
		if kind == BodyAsteroid && v.Done && doneNight == nil {
			doneNight = n
		}
	}
	if doneNight != nil {
		st.Done, st.DoneNight = true, doneNight.Night.Format(NightLayout)
		p := evaluateNight(kind, *doneNight, g)
		m := bodyMeasurement(*doneNight)
		return &p, Ready(&m, &p, doneNight.Subs), st
	}
	if cur == nil {
		rd := bodyCollecting(tonightSubs)
		rd.Reason = ReasonNoSubsTonight
		if tonightSubs > 0 {
			rd.Reason = ReasonTonightNotBuilt
		}
		return nil, rd, st
	}
	m := bodyMeasurement(*cur)
	if cur.Error != nil {
		rd := bodyCollecting(tonightSubs)
		if *cur.Error != ErrInsufficientData.Error() {
			rd.State = StateUnmeasurable
		}
		rd.Reason = BodyErrorText(*cur.Error, cur.Subs)
		return nil, rd, st
	}
	proj, projected := Project(*cur, taken)
	if !projected {
		p := evaluateNight(kind, *cur, g)
		return &p, Ready(&m, &p, tonightSubs), st
	}
	p := evaluateNight(kind, proj, g)
	p.ProjectedFrom = cur.Subs
	pm := bodyMeasurement(proj)
	rd := Ready(&pm, &p, tonightSubs)
	rd.Reason = fmt.Sprintf("SNR %.1f projected from %.1f measured on %d subs to the %d subs taken; the next measurement checks it", proj.SNR, cur.SNR, cur.Subs, taken)
	snr := proj.SNR
	st.Current.ProjectedSNR, st.Current.ProjectedSubs = &snr, taken
	st.Current.Progress, st.Current.Done = p.Progress, p.Done
	return &p, rd, st
}
