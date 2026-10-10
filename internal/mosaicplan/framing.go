package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
)

const (
	defaultOverlap     = 15.0
	defaultMinAltitude = 30.0
	sourceRequest      = "request"
	sourceDefault      = "default"
	sourceTargets      = "median effective hours of your targets"
)

type FramingBasis struct {
	HoursPerPanel        *float64 `json:"hoursPerPanel"`
	HoursPerPanelSource  *string  `json:"hoursPerPanelSource"`
	Targets              int      `json:"targets"`
	HoursPerClearNight   *float64 `json:"hoursPerClearNight"`
	ClearNightsPerSeason *float64 `json:"clearNightsPerSeason"`
	HistoryNights        int      `json:"historyNights"`
	Reason               *string  `json:"reason"`
}

type FramingRequest struct {
	RA            float64  `json:"ra"`
	Dec           float64  `json:"dec"`
	MajorArcmin   float64  `json:"majorArcmin"`
	MinorArcmin   *float64 `json:"minorArcmin"`
	PA            *float64 `json:"pa"`
	Rotation      *float64 `json:"rotation,omitempty"`
	Overlap       *float64 `json:"overlap,omitempty"`
	Rows          int      `json:"rows,omitempty"`
	Cols          int      `json:"cols,omitempty"`
	Brick         bool     `json:"brick,omitempty"`
	HoursPerPanel float64  `json:"hoursPerPanel,omitempty"`
	MinAltitude   *float64 `json:"minAltitude,omitempty"`
}

type FramingOption struct {
	mosaics.Layout
	Hours       *float64 `json:"hours"`
	Nights      *int     `json:"nights"`
	Seasons     *int     `json:"seasons"`
	Cost        string   `json:"cost"`
	Recommended bool     `json:"recommended"`
}

type Framing struct {
	Rig               mosaics.Rig     `json:"rig"`
	RigInfo           rigsource.Rig   `json:"rigInfo"`
	Rotation          float64         `json:"rotation"`
	SuggestedRotation float64         `json:"suggestedRotation"`
	Overlap           float64         `json:"overlap"`
	OverlapDefault    float64         `json:"overlapDefault"`
	OverlapSource     string          `json:"overlapSource"`
	MinAltitude       float64         `json:"minAltitude"`
	MinAltitudeSource string          `json:"minAltitudeSource"`
	NightHours        *float64        `json:"nightHours"`
	Basis             FramingBasis    `json:"basis"`
	BestMonths        []string        `json:"bestMonths"`
	SiteKnown         bool            `json:"siteKnown"`
	Options           []FramingOption `json:"options"`
	Chosen            *FramingOption  `json:"chosen,omitempty"`
}

func (s *Service) Frame(ctx context.Context, req FramingRequest, now time.Time) (Framing, error) {
	if req.Dec < -90 || req.Dec > 90 || math.IsNaN(req.RA) || math.IsNaN(req.Dec) {
		return Framing{}, fmt.Errorf("%w: coordinates out of range", ErrBadRequest)
	}
	ra := math.Mod(req.RA+360, 360)
	o := outline(ra, req)
	rig, info, err := s.rig(ctx)
	if err != nil {
		return Framing{}, err
	}
	out := Framing{Rig: rig, RigInfo: info, Overlap: defaultOverlap, OverlapDefault: defaultOverlap, OverlapSource: sourceDefault,
		MinAltitude: defaultMinAltitude, MinAltitudeSource: sourceDefault, BestMonths: []string{}}
	if req.Overlap != nil {
		out.Overlap, out.OverlapSource = math.Max(0, math.Min(50, *req.Overlap)), sourceRequest
	}
	out.SuggestedRotation = mosaics.SuggestRotation(o, out.Overlap, rig)
	out.Rotation = out.SuggestedRotation
	if req.Rotation != nil {
		out.Rotation = math.Mod(math.Mod(*req.Rotation, 180)+180, 180)
	}
	if req.MinAltitude != nil {
		out.MinAltitude, out.MinAltitudeSource = math.Max(0, math.Min(89, *req.MinAltitude)), sourceRequest
	}
	minAlt := out.MinAltitude
	var months [12]float64
	if s.Site != nil {
		if site, ok := s.Site(ctx); ok {
			out.SiteKnown = true
			months = mosaics.MonthlyDarkHours(now.Year(), site, []mosaics.Point{o.Centre}, minAlt)
			idx := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
			slices.SortFunc(idx, func(a, b int) int {
				switch {
				case months[a] > months[b]:
					return -1
				case months[a] < months[b]:
					return 1
				}
				return a - b
			})
			var sum float64
			for _, m := range idx[:3] {
				if months[m] >= usableMonthly {
					sum += months[m]
					out.BestMonths = append(out.BestMonths, time.Month(m + 1).String()[:3])
				}
			}
			if len(out.BestMonths) > 0 {
				nh := math.Round(sum/float64(len(out.BestMonths))*10) / 10
				out.NightHours = &nh
			}
		}
	}
	if err := s.framingBasis(ctx, req, months, out.SiteKnown, &out.Basis); err != nil {
		return out, err
	}
	b := out.Basis
	cost := func(l mosaics.Layout) FramingOption {
		opt := FramingOption{Layout: l}
		if b.HoursPerPanel == nil {
			opt.Cost = "hours per panel unknown: no target has an hour of effective exposure yet"
			return opt
		}
		h := *b.HoursPerPanel * float64(len(l.Panels))
		opt.Hours = &h
		if b.HoursPerClearNight == nil {
			opt.Cost = fmt.Sprintf("%.0f h effective · nights unknown: %s", h, *b.Reason)
			return opt
		}
		n := int(math.Ceil(h / *b.HoursPerClearNight))
		opt.Nights = &n
		if b.ClearNightsPerSeason == nil || *b.ClearNightsPerSeason <= 0 {
			opt.Cost = fmt.Sprintf("%.0f h effective · ≈ %d clear nights at your %.1f h per clear night", h, n, *b.HoursPerClearNight)
			return opt
		}
		se := max(1, int(math.Ceil(float64(n) / *b.ClearNightsPerSeason)))
		opt.Seasons = &se
		season := "1 season"
		if se > 1 {
			season = fmt.Sprintf("%d seasons", se)
		}
		opt.Cost = fmt.Sprintf("%.0f h effective · ≈ %d clear nights at your %.1f h per clear night · %s", h, n, *b.HoursPerClearNight, season)
		return opt
	}
	for i, l := range mosaics.Alternatives(o, out.Rotation, out.Overlap, rig) {
		opt := cost(l)
		opt.Recommended = i == 0
		out.Options = append(out.Options, opt)
	}
	if req.Rows > 0 && req.Cols > 0 {
		var l mosaics.Layout
		if req.Brick {
			l = mosaics.Brick(o, req.Rows, req.Cols, out.Rotation, out.Overlap, rig)
		} else {
			l = mosaics.Grid(o, req.Rows, req.Cols, out.Rotation, out.Overlap, rig)
		}
		c := cost(l)
		out.Chosen = &c
		if !slices.ContainsFunc(out.Options, func(x FramingOption) bool { return x.ID == l.ID }) {
			out.Options = append(out.Options, c)
		}
	}
	if out.Options == nil {
		out.Options = []FramingOption{}
	}
	return out, nil
}

func (s *Service) framingBasis(ctx context.Context, req FramingRequest, months [12]float64, siteKnown bool, b *FramingBasis) error {
	if req.HoursPerPanel > 0 {
		v, src := req.HoursPerPanel, sourceRequest
		b.HoursPerPanel, b.HoursPerPanelSource = &v, &src
	} else {
		med, n, err := s.medianTargetHours(ctx)
		if err != nil {
			return err
		}
		if med != nil {
			src := sourceTargets
			b.HoursPerPanel, b.HoursPerPanelSource, b.Targets = med, &src, n
		}
	}
	hist, err := s.clearNights(ctx)
	if err != nil {
		return err
	}
	b.HistoryNights = len(hist.hours)
	if v := hist.hoursPerClearNight(); v != nil {
		r := math.Round(*v*100) / 100
		b.HoursPerClearNight = &r
	} else {
		b.Reason = reason(fmt.Sprintf("only %d clear nights of history; at least %d are needed", len(hist.hours), minHistoryNights))
	}
	if siteKnown {
		b.ClearNightsPerSeason = clearNightsIn(hist.perMonth(), usableMonths(months))
	} else if b.Reason == nil {
		b.Reason = reason("the observatory site is not known, so the target's season can't be worked out")
	}
	return nil
}

func outline(ra float64, req FramingRequest) mosaics.Outline {
	o := mosaics.Outline{Centre: mosaics.Point{RA: ra, Dec: req.Dec}, MajorArcmin: math.Max(0, req.MajorArcmin)}
	if req.MinorArcmin != nil {
		o.MinorArcmin = math.Max(0, *req.MinorArcmin)
	}
	if o.MinorArcmin == 0 {
		o.MinorArcmin = o.MajorArcmin
	}
	if req.PA != nil {
		o.PADeg = *req.PA
	}
	return o
}
