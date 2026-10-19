package discoverysource

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryalert"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Views of the inbox.
const (
	ViewLook      = "look"
	ViewKnown     = "known"
	ViewMovers    = "movers"
	ViewLow       = "low"
	ViewVetted    = "vetted"
	ViewArtifacts = "artifacts"
	ViewUnsure    = "unsure"
)

// Views lists every view; the empty view is everything.
func Views() []string {
	return []string{ViewLook, ViewKnown, ViewMovers, ViewLow, ViewUnsure, ViewArtifacts, ViewVetted}
}

// SearchArcsec is how near a searched position a source must be.
const SearchArcsec = 60.0

// Filter picks the sources to list. Since and Until bound the nights a
// source was seen on; Night picks the sources seen on that night.
type Filter struct {
	Object string
	Since  *time.Time
	Until  *time.Time
	Night  *time.Time
	Search string
	View   string
}

// Summary is a source with its candidates and what they add up to.
type Summary struct {
	Source app.DiscoverySource
	// Members are the source's candidates, newest night first and, within
	// a night, the one that matters most first.
	Members []app.DiscoveryCandidate
	// Best is the candidate that matters most on the newest night with the
	// source's class; its position and cutouts stand for the source.
	Best  app.DiscoveryCandidate
	Class string
	First time.Time
	Last  time.Time
	// Seen counts the nights with a candidate; Compared the nights the
	// target was compared since the first of them.
	Seen     int
	Compared int
	// Checked and Failed are the catalogs the candidates' runs consulted
	// and those that failed every time.
	Checked []string
	Failed  []string
	// Listed is whether vetting found it with high confidence in a catalog
	// of known objects.
	Listed bool
	// Unsure is whether vetting could not decide it.
	Unsure bool
	// LikelyArtifact is whether vetting called it an artifact with medium or
	// high confidence.
	LikelyArtifact bool
}

// Look says whether a source is new and not vetted.
func (s Summary) Look() bool {
	return discoveries.IsNew(s.Class) && s.Source.Vetting == "" && !s.Listed && !s.LikelyArtifact && !s.Unsure
}

// In says whether a source belongs to a view.
func (s Summary) In(view string) bool {
	change := s.Source.Kind == app.DiscoveryKindChange
	switch view {
	case ViewLook:
		return s.Look()
	case ViewKnown:
		return change && s.Source.Vetting == "" && (strings.HasPrefix(s.Class, "known_") || s.Listed)
	case ViewMovers:
		return !change
	case ViewLow:
		return change && s.Source.Vetting == "" && s.Class == discoveries.ClassLowConfidence
	case ViewUnsure:
		return s.Source.Vetting == "" && s.Unsure && !s.Listed && !s.LikelyArtifact && discoveries.IsNew(s.Class)
	case ViewArtifacts:
		return s.Source.Vetting == "" && s.LikelyArtifact && discoveries.IsNew(s.Class)
	case ViewVetted:
		return s.Source.Vetting != ""
	}
	return true
}

// Counts are how many sources each view holds.
type Counts struct {
	Look      int `json:"look"`
	Known     int `json:"known"`
	Movers    int `json:"movers"`
	NewMovers int `json:"new_movers"`
	// KnownMovers counts the moving objects a catalog names.
	KnownMovers int `json:"known_movers"`
	Low         int `json:"low"`
	Vetted      int `json:"vetted"`
	Artifacts   int `json:"artifacts"`
	Unsure      int `json:"unsure"`
	All         int `json:"all"`
	// LookEarlier counts the sources that need a look and were last seen
	// before Since, so the range leaves them out.
	LookEarlier int `json:"look_earlier"`
}

func countOf(list []Summary) Counts {
	var c Counts
	for _, s := range list {
		c.All++
		if s.In(ViewLook) {
			c.Look++
		}
		if s.In(ViewKnown) {
			c.Known++
		}
		if s.In(ViewMovers) {
			c.Movers++
			if s.Look() {
				c.NewMovers++
			}
			if s.Class == discoveries.ClassKnownAsteroid || s.Listed {
				c.KnownMovers++
			}
		}
		if s.In(ViewLow) {
			c.Low++
		}
		if s.In(ViewVetted) {
			c.Vetted++
		}
		if s.In(ViewUnsure) {
			c.Unsure++
		}
		if s.In(ViewArtifacts) {
			c.Artifacts++
		}
	}
	return c
}

// List gathers new candidates into sources, then lists the sources the
// filter picks, those that need a look first, then the newest change, with
// the count of each view before the view is applied.
func List(ctx context.Context, db *gorm.DB, f Filter) ([]Summary, Counts, error) {
	if err := Sync(ctx, db); err != nil {
		return nil, Counts{}, err
	}
	db = db.WithContext(ctx)
	ids := db.Model(&app.DiscoveryCandidate{}).Distinct("source_id").Where("source_id IS NOT NULL")
	if f.Object != "" {
		ids = ids.Where("object = ?", f.Object)
	}
	if f.Since != nil {
		ids = ids.Where("night >= ?", *f.Since)
	}
	if f.Until != nil {
		ids = ids.Where("night <= ?", *f.Until)
	}
	if f.Night != nil {
		ids = ids.Where("night >= ? AND night < ?", *f.Night, f.Night.AddDate(0, 0, 1))
	}
	list, err := summaries(db, func(q *gorm.DB) *gorm.DB { return q.Where("id IN (?)", ids) })
	if err != nil {
		return nil, Counts{}, err
	}
	if f.Search != "" {
		list = slices.DeleteFunc(list, func(s Summary) bool { return !matchesSearch(s, f.Search) })
	}
	counts := countOf(list)
	if f.Since != nil && f.Until == nil && f.Night == nil {
		if counts.LookEarlier, err = lookEarlier(db, f, ids); err != nil {
			return nil, Counts{}, err
		}
	}
	if f.View != "" {
		list = slices.DeleteFunc(list, func(s Summary) bool { return !s.In(f.View) })
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Look() != b.Look() {
			return a.Look()
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		if a.Class != b.Class {
			return Better(a.Class, b.Class)
		}
		return a.Best.SNR > b.Best.SNR
	})
	return list, counts, nil
}

func lookEarlier(db *gorm.DB, f Filter, inRange *gorm.DB) (int, error) {
	news := db.Model(&app.DiscoveryCandidate{}).Select("source_id").Where("source_id IS NOT NULL AND class IN ?",
		[]string{discoveries.ClassNova, discoveries.ClassSupernova, discoveries.ClassNewVariable, discoveries.ClassUnknownMover})
	if f.Object != "" {
		news = news.Where("object = ?", f.Object)
	}
	list, err := summaries(db, func(q *gorm.DB) *gorm.DB {
		return q.Where("vetting = ? AND id IN (?) AND id NOT IN (?)", "", news, inRange)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range list {
		if s.Look() && (f.Search == "" || matchesSearch(s, f.Search)) {
			n++
		}
	}
	return n, nil
}

// Get is one source with its candidates.
func Get(ctx context.Context, db *gorm.DB, id int) (Summary, error) {
	if err := Sync(ctx, db); err != nil {
		return Summary{}, err
	}
	list, err := summaries(db.WithContext(ctx), func(q *gorm.DB) *gorm.DB { return q.Where("id = ?", id) })
	if err != nil {
		return Summary{}, err
	}
	if len(list) == 0 {
		return Summary{}, gorm.ErrRecordNotFound
	}
	return list[0], nil
}

// SourceOf is the source a candidate belongs to.
func SourceOf(ctx context.Context, db *gorm.DB, candidateID int) (int, error) {
	if err := Sync(ctx, db); err != nil {
		return 0, err
	}
	var c app.DiscoveryCandidate
	if err := db.WithContext(ctx).Select("id", "source_id").First(&c, candidateID).Error; err != nil {
		return 0, err
	}
	if c.SourceID == nil {
		return 0, gorm.ErrRecordNotFound
	}
	return *c.SourceID, nil
}

func summaries(db *gorm.DB, pick func(*gorm.DB) *gorm.DB) ([]Summary, error) {
	var sources []app.DiscoverySource
	if err := pick(db.Model(&app.DiscoverySource{})).Find(&sources).Error; err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}
	sids := make([]int, len(sources))
	objects := map[string]bool{}
	for i, s := range sources {
		sids[i] = s.ID
		objects[s.Object] = true
	}
	var cands []app.DiscoveryCandidate
	if err := db.Omit("light", "track").Where("source_id IN (?)", pick(db.Model(&app.DiscoverySource{})).Select("id")).Find(&cands).Error; err != nil {
		return nil, err
	}
	members := map[int][]app.DiscoveryCandidate{}
	runIDs := map[int]bool{}
	for _, c := range cands {
		members[*c.SourceID] = append(members[*c.SourceID], c)
		runIDs[c.RunID] = true
	}
	runs, err := runsOf(db, runIDs)
	if err != nil {
		return nil, err
	}
	compared, err := comparedNights(db, objects)
	if err != nil {
		return nil, err
	}
	var listed []int
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id IN (?) AND suggested = ? AND confidence IN ?",
		pick(db.Model(&app.DiscoverySource{})).Select("id"), app.VetKnown, []string{"medium", "high"}).Pluck("source_id", &listed).Error; err != nil {
		return nil, err
	}
	var artifacts []int
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id IN (?) AND suggested = ? AND confidence IN ?",
		pick(db.Model(&app.DiscoverySource{})).Select("id"), app.VetArtifact, []string{"low", "medium", "high"}).Pluck("source_id", &artifacts).Error; err != nil {
		return nil, err
	}
	var unsure []int
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id IN (?) AND suggested = ?",
		pick(db.Model(&app.DiscoverySource{})).Select("id"), app.VetUnsure).Pluck("source_id", &unsure).Error; err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(sources))
	for _, s := range sources {
		if len(members[s.ID]) == 0 {
			continue
		}
		sum := summarize(s, members[s.ID], runs, compared[s.Object])
		sum.Listed = slices.Contains(listed, s.ID)
		sum.LikelyArtifact = slices.Contains(artifacts, s.ID)
		sum.Unsure = slices.Contains(unsure, s.ID)
		out = append(out, sum)
	}
	return out, nil
}

type runChecks struct {
	ID      int
	Checked string
	Failed  string
}

func runsOf(db *gorm.DB, ids map[int]bool) (map[int]runChecks, error) {
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	out := map[int]runChecks{}
	for chunk := range slices.Chunk(list, 500) {
		var rows []runChecks
		if err := db.Model(&app.DiscoveryRun{}).Select("id", "checked", "failed").Where("id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.ID] = r
		}
	}
	return out, nil
}

func comparedNights(db *gorm.DB, objects map[string]bool) (map[string][]time.Time, error) {
	names := make([]string, 0, len(objects))
	for o := range objects {
		names = append(names, o)
	}
	type row struct {
		Object string
		Night  time.Time
	}
	out := map[string][]time.Time{}
	for chunk := range slices.Chunk(names, 500) {
		var rows []row
		if err := db.Model(&app.DiscoveryRun{}).Distinct("object", "night").Where("state = ? AND object IN ?", app.DiscoveryDone, chunk).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.Object] = append(out[r.Object], dateOf(r.Night))
		}
	}
	return out, nil
}

func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func summarize(s app.DiscoverySource, members []app.DiscoveryCandidate, runs map[int]runChecks, compared []time.Time) Summary {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if !a.Night.Equal(b.Night) {
			return a.Night.After(b.Night)
		}
		if a.Class != b.Class {
			return Better(a.Class, b.Class)
		}
		return a.SNR > b.SNR
	})
	out := Summary{Source: s, Members: members, Class: members[0].Class}
	nights := map[time.Time]bool{}
	checked, failed := map[string]bool{}, map[string]bool{}
	for _, m := range members {
		if Better(m.Class, out.Class) {
			out.Class = m.Class
		}
		d := dateOf(m.Night)
		nights[d] = true
		if out.First.IsZero() || d.Before(out.First) {
			out.First = d
		}
		if d.After(out.Last) {
			out.Last = d
		}
		r := runs[m.RunID]
		for _, c := range strings.Split(r.Checked, ",") {
			if c = strings.TrimSpace(c); c != "" {
				checked[c] = true
			}
		}
		for _, c := range strings.Split(r.Failed, ",") {
			if c = strings.TrimSpace(c); c != "" {
				failed[c] = true
			}
		}
	}
	i := slices.IndexFunc(members, func(m app.DiscoveryCandidate) bool { return m.Class == out.Class })
	out.Best = members[i]
	out.Seen = len(nights)
	for _, n := range compared {
		if !n.Before(out.First) {
			out.Compared++
		}
	}
	out.Compared = max(out.Compared, out.Seen)
	for c := range checked {
		out.Checked = append(out.Checked, c)
	}
	for c := range failed {
		if !checked[c] {
			out.Failed = append(out.Failed, c)
		}
	}
	sort.Strings(out.Checked)
	sort.Strings(out.Failed)
	return out
}

func matchesSearch(s Summary, q string) bool {
	if ra, dec, ok := ParsePosition(q); ok {
		return discoveryalert.SepArcsec(ra, dec, s.Best.RA, s.Best.Dec) <= SearchArcsec
	}
	q = strings.ToLower(strings.TrimSpace(q))
	if strings.Contains(strings.ToLower(s.Source.Object), q) {
		return true
	}
	for _, m := range s.Members {
		var ms []discoveries.Match
		if m.Matches == "" || json.Unmarshal([]byte(m.Matches), &ms) != nil {
			continue
		}
		if slices.ContainsFunc(ms, func(x discoveries.Match) bool { return strings.Contains(strings.ToLower(x.Name), q) }) {
			return true
		}
	}
	return false
}
