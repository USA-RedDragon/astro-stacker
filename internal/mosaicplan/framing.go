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
	defaultHoursPerPanel = 9.0
	defaultNightHours    = 7.0
	shutterEfficiency    = 0.4
	clearNightsPerSeason = 25.0
	defaultOverlap       = 15.0
	defaultMinAltitude   = 30.0
)

type FramingRequest struct {
	RA            float64  `json:"ra"`
	Dec           float64  `json:"dec"`
	MajorArcmin   float64  `json:"majorArcmin"`
	MinorArcmin   float64  `json:"minorArcmin"`
	PA            float64  `json:"pa"`
	Rotation      *float64 `json:"rotation,omitempty"`
	Overlap       *float64 `json:"overlap,omitempty"`
	Rows          int      `json:"rows,omitempty"`
	Cols          int      `json:"cols,omitempty"`
	Brick         bool     `json:"brick,omitempty"`
	HoursPerPanel float64  `json:"hoursPerPanel,omitempty"`
	MinAltitude   float64  `json:"minAltitude,omitempty"`
}

type FramingOption struct {
	mosaics.Layout
	Hours       float64 `json:"hours"`
	Nights      int     `json:"nights"`
	Seasons     int     `json:"seasons"`
	Cost        string  `json:"cost"`
	Recommended bool    `json:"recommended"`
}

type Framing struct {
	Rig               mosaics.Rig     `json:"rig"`
	RigInfo           rigsource.Rig   `json:"rigInfo"`
	Rotation          float64         `json:"rotation"`
	SuggestedRotation float64         `json:"suggestedRotation"`
	Overlap           float64         `json:"overlap"`
	NightHours        float64         `json:"nightHours"`
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
	o := mosaics.Outline{Centre: mosaics.Point{RA: ra, Dec: req.Dec}, MajorArcmin: math.Max(0, req.MajorArcmin), MinorArcmin: math.Max(0, req.MinorArcmin), PADeg: req.PA}
	if o.MinorArcmin == 0 {
		o.MinorArcmin = o.MajorArcmin
	}
	rig, info, err := s.rig(ctx)
	if err != nil {
		return Framing{}, err
	}
	out := Framing{Rig: rig, RigInfo: info, Overlap: defaultOverlap, NightHours: defaultNightHours, BestMonths: []string{}}
	if req.Overlap != nil {
		out.Overlap = math.Max(0, math.Min(50, *req.Overlap))
	}
	out.SuggestedRotation = mosaics.SuggestRotation(o, out.Overlap, rig)
	out.Rotation = out.SuggestedRotation
	if req.Rotation != nil {
		out.Rotation = math.Mod(math.Mod(*req.Rotation, 180)+180, 180)
	}
	minAlt := req.MinAltitude
	if minAlt <= 0 {
		minAlt = defaultMinAltitude
	}
	if s.Site != nil {
		if site, ok := s.Site(ctx); ok {
			out.SiteKnown = true
			months := mosaics.MonthlyDarkHours(now.Year(), site, []mosaics.Point{o.Centre}, minAlt)
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
				sum += months[m]
				if months[m] >= usableMonthly {
					out.BestMonths = append(out.BestMonths, time.Month(m + 1).String()[:3])
				}
			}
			if sum > 0 {
				out.NightHours = sum / 3
			}
		}
	}
	hpp := req.HoursPerPanel
	if hpp <= 0 {
		hpp = defaultHoursPerPanel
	}
	cost := func(l mosaics.Layout) FramingOption {
		opt := FramingOption{Layout: l, Hours: hpp * float64(len(l.Panels))}
		if out.NightHours > 0 {
			opt.Nights = int(math.Ceil(opt.Hours / (shutterEfficiency * out.NightHours)))
		}
		opt.Seasons = max(1, int(math.Ceil(float64(opt.Nights)/clearNightsPerSeason)))
		season := "1 season"
		if opt.Seasons > 1 {
			season = fmt.Sprintf("%d seasons", opt.Seasons)
		}
		opt.Cost = fmt.Sprintf("%.0f h effective · ≈ %d nights · %s", opt.Hours, opt.Nights, season)
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
