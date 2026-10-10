package discover

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	CompletionDone       = "done"
	CompletionInProgress = "in-progress"
	CompletionNotStarted = "not-started"

	upTonightHours = 1.0
)

var ErrUnknownList = errors.New("unknown catalogue")

type SubjectRef struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	ProjectID int     `json:"projectId,omitempty"`
	State     string  `json:"state,omitempty"`
	Status    string  `json:"status"`
	Method    string  `json:"method"`
	Done      bool    `json:"done"`
	Hours     float64 `json:"hours"`
}

type Tonight struct {
	Up           bool       `json:"up"`
	Hours        float64    `json:"hours"`
	Start        *time.Time `json:"start,omitempty"`
	End          *time.Time `json:"end,omitempty"`
	PeakAlt      float64    `json:"peakAlt"`
	PeakAt       *time.Time `json:"peakAt,omitempty"`
	MoonSep      float64    `json:"moonSeparation"`
	MoonIllum    float64    `json:"moonIllumination"`
	SiteResolved bool       `json:"siteResolved"`
}

type CatalogueEntry struct {
	Index    int                `json:"index"`
	Label    string             `json:"label"`
	Object   catalog.Object     `json:"object"`
	Status   string             `json:"status"`
	Hours    map[string]float64 `json:"hours"`
	Subjects []SubjectRef       `json:"subjects"`
	Tonight  *Tonight           `json:"tonight,omitempty"`
	Fit      sky.Fit            `json:"fit"`
}

type CatalogueSummary struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Total      int    `json:"total"`
	Done       int    `json:"done"`
	InProgress int    `json:"inProgress"`
	NotStarted int    `json:"notStarted"`
	UpTonight  int    `json:"upTonight"`
}

type Overview struct {
	Catalogues  []CatalogueSummary `json:"catalogues"`
	OpenMatches int                `json:"openMatches"`
	Night       *NightInfo         `json:"night,omitempty"`
	SiteError   string             `json:"siteError,omitempty"`
	Sources     []catalog.Source   `json:"sources"`
}

type NightInfo struct {
	Start            time.Time  `json:"start"`
	Dusk             *time.Time `json:"dusk,omitempty"`
	Dawn             *time.Time `json:"dawn,omitempty"`
	DarkHours        float64    `json:"darkHours"`
	MoonIllumination float64    `json:"moonIllumination"`
	MinAltitude      float64    `json:"minAltitude"`
}

func nightInfo(n sky.Night, minAlt float64) *NightInfo {
	return &NightInfo{Start: n.Start, Dusk: n.Dusk, Dawn: n.Dawn, DarkHours: n.DarkHours(), MoonIllumination: math.Round(n.MoonIllumination()*100) / 100, MinAltitude: minAlt}
}

func (s *Service) minAlt() float64 {
	if s.Rig.MinAltitude > 0 {
		return s.Rig.MinAltitude
	}
	return 30
}

func (s *Service) tonightFor(n *sky.Night, o catalog.Object) *Tonight {
	if n == nil {
		return nil
	}
	w := n.Window(o.RA, o.Dec, s.minAlt())
	return &Tonight{
		Up: w.Hours >= upTonightHours, Hours: w.Hours, Start: w.Start, End: w.End, PeakAlt: math.Round(w.PeakAlt*10) / 10, PeakAt: w.PeakAt,
		MoonSep: math.Round(n.MoonSeparation(o.RA, o.Dec)*10) / 10, MoonIllum: math.Round(n.MoonIllumination()*100) / 100, SiteResolved: true,
	}
}

func (s *Service) entry(snap *snapshot, subjects map[string]Subject, n *sky.Night, o catalog.Object) CatalogueEntry {
	e := CatalogueEntry{Object: o, Hours: map[string]float64{}, Subjects: []SubjectRef{}, Status: CompletionNotStarted}
	seen := map[string]bool{}
	for _, l := range snap.byObject[o.ID] {
		subj, ok := subjects[l.Subject]
		if !ok || seen[l.Subject] {
			continue
		}
		seen[l.Subject] = true
		ref := SubjectRef{Key: subj.Key, Name: subj.Name, ProjectID: subj.ProjectID, State: subj.StateName, Status: l.Status, Method: l.Method,
			Done: subj.Done, Hours: math.Round(subj.TotalHours()*100) / 100}
		e.Subjects = append(e.Subjects, ref)
		if !l.Linked() {
			continue
		}
		for f, h := range subj.Hours {
			e.Hours[f] += h
		}
		switch {
		case subj.Done:
			e.Status = CompletionDone
		case e.Status != CompletionDone && (subj.TotalHours() > 0 || subj.Scheduled()):
			e.Status = CompletionInProgress
		}
	}
	for f, h := range e.Hours {
		e.Hours[f] = math.Round(h*100) / 100
	}
	e.Tonight = s.tonightFor(n, o)
	e.Fit = s.Rig.Frame.Fit(o.MajorArcmin, o.MinorArcmin, sky.DefaultOverlap)
	return e
}

func listLabel(list string, o catalog.Object) string {
	prefix := map[string]string{
		"messier": "M ", "caldwell": "C ", "herschel400": "NGC ", "sharpless": "Sh2-", "vdb": "vdB ", "barnard": "B ",
		"lbn": "LBN ", "arp": "Arp ", "hickson": "HCG ", "rcw": "RCW ", "green": "G",
	}[list]
	for _, d := range append([]string{o.Designation}, o.Aliases...) {
		if strings.HasPrefix(d, prefix) {
			if _, ok := catalog.Canonical(d); ok {
				return d
			}
		}
	}
	return o.Designation
}

func labelNumber(label string) int {
	digits := strings.TrimLeftFunc(label, func(r rune) bool { return r < '0' || r > '9' })
	end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
	if end >= 0 {
		digits = digits[:end]
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n
}

func (s *Service) tonightNight(ctx context.Context) (*sky.Night, string) {
	n, err := s.night(ctx, s.now())
	if err != nil {
		return nil, err.Error()
	}
	return &n, ""
}

func subjectMap(snap *snapshot) map[string]Subject {
	out := make(map[string]Subject, len(snap.subjects))
	for _, subj := range snap.subjects {
		out[subj.Key] = subj
	}
	return out
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Overview{}, err
	}
	n, siteErr := s.tonightNight(ctx)
	subjects := subjectMap(snap)
	out := Overview{SiteError: siteErr, Sources: s.Catalog.Sources()}
	if n != nil {
		out.Night = nightInfo(*n, s.minAlt())
	}
	for _, l := range s.Catalog.Lists() {
		sum := CatalogueSummary{Key: l.Key, Name: l.Name, Total: l.Total}
		for _, o := range s.Catalog.List(l.Key) {
			e := s.entry(snap, subjects, n, o)
			switch e.Status {
			case CompletionDone:
				sum.Done++
			case CompletionInProgress:
				sum.InProgress++
			default:
				sum.NotStarted++
			}
			if e.Tonight != nil && e.Tonight.Up {
				sum.UpTonight++
			}
		}
		out.Catalogues = append(out.Catalogues, sum)
	}
	for _, l := range snap.links {
		if l.Status == StatusSuggested {
			out.OpenMatches++
		}
	}
	return out, nil
}

func (s *Service) Catalogue(ctx context.Context, key string) ([]CatalogueEntry, error) {
	objs := s.Catalog.List(key)
	if len(objs) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownList, key)
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	n, _ := s.tonightNight(ctx)
	subjects := subjectMap(snap)
	out := make([]CatalogueEntry, 0, len(objs))
	for i, o := range objs {
		e := s.entry(snap, subjects, n, o)
		e.Label = listLabel(key, o)
		e.Index = labelNumber(e.Label)
		if key == "herschel400" || e.Index == 0 {
			e.Index = i + 1
		}
		out = append(out, e)
	}
	return out, nil
}

type ObjectDetail struct {
	CatalogueEntry
	Links  []Link             `json:"links"`
	Months [12]float64        `json:"months"`
	Lists  []catalog.ListInfo `json:"lists"`
}

func (s *Service) Object(ctx context.Context, id string) (ObjectDetail, error) {
	o, ok := s.Catalog.Get(id)
	if !ok {
		return ObjectDetail{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return ObjectDetail{}, err
	}
	n, _ := s.tonightNight(ctx)
	d := ObjectDetail{CatalogueEntry: s.entry(snap, subjectMap(snap), n, o), Links: snap.byObject[o.ID]}
	if d.Links == nil {
		d.Links = []Link{}
	}
	if yr, err := s.year(ctx, s.now().Year()); err == nil {
		d.Months = roundMonths(yr.Hours(o.RA, o.Dec, s.minAlt()))
	}
	for _, l := range s.Catalog.Lists() {
		for _, k := range o.Lists {
			if k == l.Key {
				d.Lists = append(d.Lists, l)
			}
		}
	}
	return d, nil
}

func roundMonths(m [12]float64) [12]float64 {
	for i := range m {
		m[i] = math.Round(m[i]*10) / 10
	}
	return m
}

var ErrNotFound = errors.New("not found")
