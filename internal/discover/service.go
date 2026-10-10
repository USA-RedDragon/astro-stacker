package discover

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

var ErrNoSite = errors.New("the observatory site is not known yet: set discover.site-latitude and discover.site-longitude, or wait for a light with SITELAT and SITELONG in its header")

type Rig struct {
	Frame       sky.Frame          `json:"frame"`
	Colour      bool               `json:"colour"`
	Filters     map[string]float64 `json:"filters"`
	Exposures   map[string]float64 `json:"exposures"`
	TypicalHFR  float64            `json:"typicalHfr"`
	TypicalRMS  float64            `json:"typicalGuideRms"`
	MinAltitude float64            `json:"minAltitude"`
	SkyBright   float64            `json:"skyBrightness"`
}

type SiteFunc func(ctx context.Context) (sky.Site, error)

type Service struct {
	Measure func(ctx context.Context) rigsource.Rig
	Sky     func(ctx context.Context) skybright.Value
	HAlpha  func() (*halpha.Map, halpha.Status)
	Catalog *catalog.Index
	AppDB   *gorm.DB
	SchedDB *gorm.DB
	Site    SiteFunc
	Rig     Rig
	Now     func() time.Time
	TTL     time.Duration

	mu       sync.Mutex
	snap     *snapshot
	nights   map[time.Time]sky.Night
	years    map[int]*sky.Year
	finder   *finderCache
	siteMemo *sky.Site
}

type snapshot struct {
	at       time.Time
	subjects []Subject
	links    []Link
	bySubj   map[string][]Link
	byObject map[string][]Link
}

func ParseFilterValues(entries []string) map[string]float64 {
	out := map[string]float64{}
	for _, e := range entries {
		for part := range strings.SplitSeq(e, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, val, _ := strings.Cut(part, "=")
			name = strings.ToUpper(strings.TrimSpace(name))
			v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			if err != nil {
				v = 0
			}
			out[name] = v
		}
	}
	return out
}

func staticInfo(r Rig) rigsource.Rig {
	out := rigsource.Empty()
	out.Basis.Source = "static"
	if r.Frame.FocalLength > 0 && r.Frame.PixelSize > 0 && r.Frame.WidthPx > 0 && r.Frame.HeightPx > 0 {
		fl, px, w, h := r.Frame.FocalLength, r.Frame.PixelSize, r.Frame.WidthPx, r.Frame.HeightPx
		sc, wd, hd := r.Frame.Scale(), r.Frame.WidthDeg(), r.Frame.HeightDeg()
		out.FocalLength, out.PixelSize, out.WidthPx, out.HeightPx, out.Scale, out.WidthDeg, out.HeightDeg = &fl, &px, &w, &h, &sc, &wd, &hd
	}
	c := r.Colour
	out.Colour = &c
	for _, f := range rigsource.FilterOrder() {
		if _, ok := r.Filters[f]; ok {
			out.Filters = append(out.Filters, f)
		}
	}
	for f, e := range r.Exposures {
		out.Exposures[f] = e
	}
	if r.TypicalHFR > 0 {
		v := r.TypicalHFR
		out.TypicalHFR = &v
	}
	if r.TypicalRMS > 0 {
		v := r.TypicalRMS
		out.TypicalGuideRMS = &v
	}
	return out
}

func (s *Service) halphaMap() (*halpha.Map, halpha.Status) {
	if s.HAlpha == nil {
		return nil, halpha.Status{State: halpha.StateOff, Source: halpha.SourceText}
	}
	return s.HAlpha()
}

func (s *Service) skyValue(ctx context.Context) skybright.Value {
	if s.Sky != nil {
		return s.Sky(ctx)
	}
	if s.Rig.SkyBright > 0 {
		return skybright.Configured(s.Rig.SkyBright)
	}
	return skybright.None("no sky brightness source is wired into the Discover service")
}

func (s *Service) rig(ctx context.Context) (Rig, rigsource.Rig) {
	sv := s.skyValue(ctx)
	if s.Measure == nil {
		r := s.Rig
		r.SkyBright = 0
		if sv.Mag != nil {
			r.SkyBright = *sv.Mag
		}
		return r, staticInfo(s.Rig)
	}
	m := s.Measure(ctx)
	r := Rig{MinAltitude: s.Rig.MinAltitude, Filters: map[string]float64{}, Exposures: map[string]float64{}}
	if sv.Mag != nil {
		r.SkyBright = *sv.Mag
	}
	if m.Known() {
		r.Frame = sky.Frame{FocalLength: *m.FocalLength, PixelSize: *m.PixelSize, WidthPx: *m.WidthPx, HeightPx: *m.HeightPx}
	}
	r.Colour = m.Colour != nil && *m.Colour
	for _, f := range m.Filters {
		r.Filters[f] = s.Rig.Filters[f]
	}
	for f, e := range m.Exposures {
		r.Exposures[f] = e
	}
	if m.TypicalHFR != nil {
		r.TypicalHFR = *m.TypicalHFR
	}
	if m.TypicalGuideRMS != nil {
		r.TypicalRMS = *m.TypicalGuideRMS
	}
	return r, m
}

func (r Rig) known() bool {
	return r.Frame.FocalLength > 0 && r.Frame.PixelSize > 0 && r.Frame.WidthPx > 0 && r.Frame.HeightPx > 0
}

func (r Rig) fit(major, minor float64) *sky.Fit {
	if !r.known() {
		return nil
	}
	f := r.Frame.Fit(major, minor, sky.DefaultOverlap)
	return &f
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) site(ctx context.Context) (sky.Site, error) {
	s.mu.Lock()
	memo := s.siteMemo
	s.mu.Unlock()
	if memo != nil {
		return *memo, nil
	}
	if s.Site == nil {
		return sky.Site{}, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return sky.Site{}, err
	}
	s.mu.Lock()
	s.siteMemo = &site
	s.mu.Unlock()
	return site, nil
}

func (s *Service) night(ctx context.Context, at time.Time) (sky.Night, error) {
	site, err := s.site(ctx)
	if err != nil {
		return sky.Night{}, err
	}
	start := site.LocalNoon(at)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nights == nil {
		s.nights = map[time.Time]sky.Night{}
	}
	if n, ok := s.nights[start]; ok {
		return n, nil
	}
	n := site.NightOf(at, 5*time.Minute)
	if len(s.nights) > 8 {
		s.nights = map[time.Time]sky.Night{}
	}
	s.nights[start] = n
	return n, nil
}

func (s *Service) year(ctx context.Context, y int) (*sky.Year, error) {
	site, err := s.site(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.years == nil {
		s.years = map[int]*sky.Year{}
	}
	if yr, ok := s.years[y]; ok {
		return yr, nil
	}
	yr := site.Year(y, 20*time.Minute)
	s.years[y] = &yr
	return &yr, nil
}

func (s *Service) Invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.finder = nil
	s.mu.Unlock()
}

func (s *Service) snapshot(ctx context.Context) (*snapshot, error) {
	ttl := s.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()
	if snap != nil && s.now().Sub(snap.at) < ttl {
		return snap, nil
	}
	subjects, err := s.loadSubjects(ctx)
	if err != nil {
		return nil, err
	}
	decisions, rows, err := s.decisions(ctx)
	if err != nil {
		return nil, err
	}
	snap = &snapshot{at: s.now(), subjects: subjects, bySubj: map[string][]Link{}, byObject: map[string][]Link{}}
	known := map[string]bool{}
	for i, subj := range subjects {
		known[subj.Key] = true
		links, reason := s.matchSubject(ctx, subj)
		subjects[i].NotCatalogue = reason
		snap.links = append(snap.links, trimSuggestions(applyDecisions(links, decisions))...)
	}
	seen := map[string]bool{}
	for _, l := range snap.links {
		seen[l.Subject+"\x00"+l.Object.ID] = true
	}
	for _, r := range rows {
		if r.Decision != app.XrefConfirmed || seen[r.Subject+"\x00"+r.ObjectID] || !known[r.Subject] {
			continue
		}
		o, ok := s.Catalog.Get(r.ObjectID)
		if !ok {
			continue
		}
		name := r.SubjectName
		if name == "" {
			name = r.Subject
		}
		snap.links = append(snap.links, Link{Subject: r.Subject, SubjectName: name, Object: o, Method: "manual",
			Confidence: 1, Why: "Linked by you.", Separation: -1, Status: StatusConfirmed})
	}
	for _, l := range snap.links {
		snap.bySubj[l.Subject] = append(snap.bySubj[l.Subject], l)
		snap.byObject[l.Object.ID] = append(snap.byObject[l.Object.ID], l)
	}
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
	return snap, nil
}

func (s *Service) Subjects(ctx context.Context) ([]Subject, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.subjects, nil
}

func (s *Service) Links(ctx context.Context, subject string) ([]Link, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.bySubj[subject], nil
}

type ReviewItem struct {
	Link
	Decided bool   `json:"decided"`
	State   string `json:"state,omitempty"`
}

func (s *Service) Review(ctx context.Context, includeDecided bool) ([]ReviewItem, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, subj := range snap.subjects {
		states[subj.Key] = subj.StateName
	}
	out := []ReviewItem{}
	for _, l := range snap.links {
		decided := (l.Status == StatusConfirmed || l.Status == StatusRejected) && l.Method != "manual"
		if l.Status == StatusSuggested || includeDecided && decided {
			out = append(out, ReviewItem{Link: l, Decided: decided, State: states[l.Subject]})
		}
	}
	return out, nil
}

type Position struct {
	RA, Dec float64
}

func (s *Service) Resolve(ctx context.Context, name string, pos *Position, mosaicRadius float64) ([]Link, error) {
	if ra, dec, ok := catalog.ParseCoordinates(name); ok {
		name = ""
		if pos == nil {
			pos = &Position{RA: ra, Dec: dec}
		}
	}
	subj := Subject{Key: "query", Name: name, Kind: SubjectObject, State: StateNone, Hours: map[string]float64{}}
	if name != "" {
		subj.Targets = []string{name}
	}
	if pos != nil {
		subj.RA, subj.Dec, subj.HasPos, subj.Radius = pos.RA, pos.Dec, true, mosaicRadius
		subj.Mosaic = mosaicRadius > 0
	}
	if name == "" && !subj.HasPos {
		return nil, fmt.Errorf("%w: give a name or coordinates", ErrBadQuery)
	}
	links, _ := s.matchSubject(ctx, subj)
	return links, nil
}

var ErrBadQuery = errors.New("bad query")
