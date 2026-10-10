package discover

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	CompletionDone       = "done"
	CompletionInProgress = "in-progress"
	CompletionMeasuring  = "measuring"
	CompletionNotStarted = "not-started"

	DoneByGoals = "goals"
	DoneByTS    = "ts-complete"

	upTonightHours = 1.0
)

var ErrUnknownList = errors.New("unknown catalogue")

type SubjectRef struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	ProjectID  int      `json:"projectId,omitempty"`
	State      string   `json:"state,omitempty"`
	Status     string   `json:"status"`
	Method     string   `json:"method"`
	Basis      string   `json:"basis,omitempty"`
	Coverage   *float64 `json:"coverage"`
	Done       bool     `json:"done"`
	DoneBy     string   `json:"doneBy,omitempty"`
	Completion string   `json:"completion"`
	Tally      Tally    `json:"tally"`
	Why        string   `json:"why,omitempty"`
	Hours      float64  `json:"hours"`
}

type Tonight struct {
	MinAltitude    float64    `json:"minAltitude"`
	AltitudeSource string     `json:"minAltitudeSource"`
	UpThreshold    float64    `json:"upThresholdHours"`
	Up             bool       `json:"up"`
	Hours          float64    `json:"hours"`
	Start          *time.Time `json:"start,omitempty"`
	End            *time.Time `json:"end,omitempty"`
	PeakAlt        *float64   `json:"peakAlt"`
	PeakAt         *time.Time `json:"peakAt,omitempty"`
	MoonSep        *float64   `json:"moonSeparation"`
	MoonIllum      float64    `json:"moonIllumination"`
	SiteResolved   bool       `json:"siteResolved"`
}

type CatalogueEntry struct {
	Index     int                `json:"index"`
	Label     string             `json:"label"`
	Object    catalog.Object     `json:"object"`
	Status    string             `json:"status"`
	DoneBy    string             `json:"doneBy,omitempty"`
	Scheduled bool               `json:"scheduled"`
	Tally     Tally              `json:"tally"`
	Hours     map[string]float64 `json:"hours"`
	Subjects  []SubjectRef       `json:"subjects"`
	Tonight   *Tonight           `json:"tonight,omitempty"`
	Fit       *sky.Fit           `json:"fit"`
}

type CatalogueSummary struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Total      int    `json:"total"`
	Done       int    `json:"done"`
	DoneGoals  int    `json:"doneByGoals"`
	DoneTS     int    `json:"doneByTs"`
	InProgress int    `json:"inProgress"`
	Measuring  int    `json:"measuring"`
	NotStarted int    `json:"notStarted"`
	Scheduled  int    `json:"scheduled"`
	UpTonight  int    `json:"upTonight"`
}

type Overview struct {
	Backfill    *goals.Backfill    `json:"backfill,omitempty"`
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
	AltitudeSource   string     `json:"minAltitudeSource"`
	UpTonightHours   float64    `json:"upTonightHours"`
}

func nightInfo(n sky.Night, minAlt float64, source string) *NightInfo {
	return &NightInfo{Start: n.Start, Dusk: n.Dusk, Dawn: n.Dawn, DarkHours: n.DarkHours(), MoonIllumination: math.Round(n.MoonIllumination()*100) / 100,
		MinAltitude: minAlt, AltitudeSource: source, UpTonightHours: upTonightHours}
}

const (
	defaultMinAltitude = 30
	altitudeSetting    = MinAltitudeSetting
	altitudeBuiltIn    = "the built-in default"
)

func (s *Service) minAlt() float64 {
	if s.Rig.MinAltitude > 0 {
		return s.Rig.MinAltitude
	}
	return defaultMinAltitude
}

func (s *Service) minAltSource() string {
	if s.Rig.MinAltitude > 0 {
		return altitudeSetting
	}
	return altitudeBuiltIn
}

func round1(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*10) / 10
	return &r
}

func (s *Service) tonightFor(n *sky.Night, o catalog.Object) *Tonight {
	return s.tonightAt(n, o, s.minAlt(), s.minAltSource())
}

func (s *Service) tonightAt(n *sky.Night, o catalog.Object, minAlt float64, source string) *Tonight {
	if n == nil {
		return nil
	}
	w := n.Window(o.RA, o.Dec, minAlt)
	return &Tonight{
		Up: w.Hours >= upTonightHours, Hours: w.Hours, Start: w.Start, End: w.End, PeakAlt: round1(w.PeakAlt), PeakAt: w.PeakAt,
		MoonSep: round1(n.MoonSeparation(o.RA, o.Dec)), MoonIllum: math.Round(n.MoonIllumination()*100) / 100, SiteResolved: true,
		MinAltitude: minAlt, AltitudeSource: source, UpThreshold: upTonightHours,
	}
}

func (s *Service) altitudeFor(snap *snapshot, subjects map[string]Subject, o catalog.Object) (float64, string) {
	for _, l := range snap.byObject[o.ID] {
		if !l.Linked() {
			continue
		}
		if subj, ok := subjects[l.Subject]; ok && subj.MinAltitude != nil && *subj.MinAltitude > 0 {
			return *subj.MinAltitude, "the minimum altitude of Target Scheduler project " + subj.Name
		}
	}
	return s.minAlt(), s.minAltSource()
}

func (snap *snapshot) linkTally(subj Subject, l Link) Tally {
	targets := l.Targets
	if len(targets) == 0 {
		targets = subj.Targets
	}
	var t Tally
	for _, name := range targets {
		if d := snap.objects[name]; d != nil {
			t.add(d)
		}
	}
	return t
}

func completionRank(status string) int {
	switch status {
	case CompletionDone:
		return 3
	case CompletionMeasuring:
		return 2
	case CompletionInProgress:
		return 1
	}
	return 0
}

func (s *Service) entry(snap *snapshot, subjects map[string]Subject, n *sky.Night, o catalog.Object, rig Rig) CatalogueEntry {
	e := CatalogueEntry{Object: o, Hours: map[string]float64{}, Subjects: []SubjectRef{}, Status: CompletionNotStarted}
	seen := map[string]bool{}
	counted := map[string]bool{}
	for _, l := range snap.byObject[o.ID] {
		subj, ok := subjects[l.Subject]
		if !ok || seen[l.Subject] {
			continue
		}
		seen[l.Subject] = true
		t := snap.linkTally(subj, l)
		ref := SubjectRef{Key: subj.Key, Name: subj.Name, ProjectID: subj.ProjectID, State: subj.StateName, Status: l.Status, Method: l.Method,
			Basis: l.Basis, Coverage: l.Coverage, Why: l.Why, Tally: t, Completion: t.status(subj.TSDone), DoneBy: t.doneBy(subj.TSDone),
			Hours: math.Round(t.Hours*100) / 100}
		ref.Done = ref.DoneBy != ""
		ref.Tally.Hours = ref.Hours
		e.Subjects = append(e.Subjects, ref)
		if !l.Linked() {
			continue
		}
		targets := l.Targets
		if len(targets) == 0 {
			targets = subj.Targets
		}
		for _, name := range targets {
			d := snap.objects[name]
			if d == nil || counted[name] {
				continue
			}
			counted[name] = true
			for f, h := range d.hours {
				e.Hours[f] += h
			}
			e.Tally.add(d)
		}
		if completionRank(ref.Completion) > completionRank(e.Status) {
			e.Status, e.DoneBy = ref.Completion, ref.DoneBy
		}
	}
	for f, h := range e.Hours {
		e.Hours[f] = math.Round(h*100) / 100
	}
	e.Tally.Hours = math.Round(e.Tally.Hours*100) / 100
	alt, source := s.altitudeFor(snap, subjects, o)
	e.Tonight = s.tonightAt(n, o, alt, source)
	if e.Status == CompletionNotStarted && slices.ContainsFunc(e.Subjects, func(r SubjectRef) bool { return r.Status == StatusPlanned }) {
		e.Scheduled = true
	}
	e.Fit = rig.fit(o.MajorArcmin, o.Minor())
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
	out := Overview{SiteError: siteErr, Sources: s.Catalog.Sources(), Backfill: s.backfill(ctx)}
	if n != nil {
		out.Night = nightInfo(*n, s.minAlt(), s.minAltSource())
	}
	rig, _ := s.rig(ctx)
	for _, l := range s.Catalog.Lists() {
		sum := CatalogueSummary{Key: l.Key, Name: l.Name, Total: l.Total}
		for _, o := range s.Catalog.List(l.Key) {
			e := s.entry(snap, subjects, n, o, rig)
			switch e.Status {
			case CompletionDone:
				sum.Done++
				if e.DoneBy == DoneByTS {
					sum.DoneTS++
				} else {
					sum.DoneGoals++
				}
			case CompletionInProgress:
				sum.InProgress++
			case CompletionMeasuring:
				sum.Measuring++
			default:
				sum.NotStarted++
				if e.Scheduled {
					sum.Scheduled++
				}
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
	rig, _ := s.rig(ctx)
	out := make([]CatalogueEntry, 0, len(objs))
	for i, o := range objs {
		e := s.entry(snap, subjects, n, o, rig)
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
	Rig    rigsource.Rig      `json:"rig"`
	HAlpha *halpha.Sample     `json:"halpha"`
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
	rig, info := s.rig(ctx)
	d := ObjectDetail{CatalogueEntry: s.entry(snap, subjectMap(snap), n, o, rig), Links: snap.byObject[o.ID], Rig: info}
	if m, _ := s.halphaMap(); m != nil {
		if smp, ok := m.Sample(o.RA, o.Dec, o.MajorArcmin/120); ok {
			d.HAlpha = &smp
		}
	}
	if d.Links == nil {
		d.Links = []Link{}
	}
	if yr, err := s.year(ctx, s.now().Year()); err == nil {
		alt, _ := s.altitudeFor(snap, subjectMap(snap), o)
		d.Months = roundMonths(yr.Hours(o.RA, o.Dec, alt))
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

type Tally struct {
	Filters  int     `json:"filters"`
	Measured int     `json:"measured"`
	Done     int     `json:"done"`
	Short    int     `json:"short"`
	Hours    float64 `json:"hours"`
}

func (t *Tally) add(d *objectData) {
	t.Filters += d.filters
	t.Measured += d.measured
	t.Done += d.done
	t.Short += d.short
	for _, h := range d.hours {
		t.Hours += h
	}
}

func (t Tally) doneBy(tsComplete bool) string {
	switch {
	case tsComplete:
		return DoneByTS
	case t.Filters > 0 && t.Measured == t.Filters && t.Done == t.Filters:
		return DoneByGoals
	}
	return ""
}

func (t Tally) status(tsComplete bool) string {
	switch {
	case t.doneBy(tsComplete) != "":
		return CompletionDone
	case t.Filters == 0:
		return CompletionNotStarted
	case t.Measured < t.Filters:
		return CompletionMeasuring
	}
	return CompletionInProgress
}

func (s *Service) backfill(ctx context.Context) *goals.Backfill {
	var live goals.BackfillLive
	if s.Backfill != nil {
		live = s.Backfill()
	}
	b, err := goals.CountBackfill(ctx, s.AppDB, live)
	if err != nil {
		return nil
	}
	return &b
}
