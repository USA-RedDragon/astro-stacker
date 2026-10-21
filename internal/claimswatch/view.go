package claimswatch

import (
	"context"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// Mention statuses on the pages.
const (
	StatusNew   = "new"
	StatusKnown = "known"
	// StatusFirstAfter is a mention already there at the first check but
	// dated after the user's record.
	StatusFirstAfter = "first_check_after_record"
	MarkOwn          = "own"
)

// MentionView is a mention on the pages.
type MentionView struct {
	ID        int        `json:"id"`
	SourceID  int        `json:"source_id"`
	Where     string     `json:"where"`
	Label     string     `json:"label"`
	Title     string     `json:"title"`
	Detail    string     `json:"detail"`
	URL       string     `json:"url"`
	Dated     *time.Time `json:"dated"`
	FirstSeen time.Time  `json:"first_seen"`
	Status    string     `json:"status"`
	Mark      string     `json:"mark"`
}

// CheckView is the last look at one place for a star.
type CheckView struct {
	Where string `json:"where"`
	State string `json:"state"`
	Note  string `json:"note"`
	New   int    `json:"new"`
	Known int    `json:"known"`
}

// StarView is a star's box on its card.
type StarView struct {
	SourceID    int           `json:"source_id"`
	Name        string        `json:"name"`
	Record      *Record       `json:"record"`
	LastChecked *time.Time    `json:"last_checked"`
	New         int           `json:"new"`
	Known       int           `json:"known"`
	Checks      []CheckView   `json:"checks"`
	Mentions    []MentionView `json:"mentions"`
}

// Summary is the claims watch on the My discoveries page.
type Summary struct {
	Stars       int         `json:"stars"`
	LastChecked *time.Time  `json:"last_checked"`
	NextCheck   *time.Time  `json:"next_check"`
	Running     bool        `json:"running"`
	Record      *Record     `json:"record"`
	NewCount    int         `json:"new_count"`
	Sources     []PlaceInfo `json:"sources"`
	PerStar     []StarView  `json:"per_star"`
}

// StatusOf is a mention's status against its star's record.
func StatusOf(m app.ClaimMention, rec *Record) string {
	switch {
	case !m.Baseline:
		return StatusNew
	case rec != nil && m.Dated != nil && m.Dated.After(rec.Published):
		return StatusFirstAfter
	default:
		return StatusKnown
	}
}

func (s *Service) mentionView(m app.ClaimMention) MentionView {
	mark := m.Mark
	if mark == "" && m.Own {
		mark = MarkOwn
	}
	return MentionView{ID: m.ID, SourceID: m.SourceID, Where: m.Where, Label: Label(m.Where), Title: m.Title, Detail: m.Detail, URL: m.URL,
		Dated: m.Dated, FirstSeen: m.FirstSeen, Status: StatusOf(m, RecordFor(s.records(), m.SourceID)), Mark: mark}
}

// Open tells whether a new mention still waits for the user.
func (v MentionView) Open() bool { return v.Status == StatusNew && v.Mark == "" }

// Summary reads the watch for the pages.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	out := Summary{Sources: Catalogue(), Running: s.Running(), PerStar: []StarView{}}
	last, ok, err := s.LastChecked(ctx)
	if err != nil {
		return out, err
	}
	if ok {
		out.LastChecked = &last
	}
	if next, err := s.Next(ctx); err == nil {
		out.NextCheck = &next
	}
	stars, err := s.Stars(ctx)
	if err != nil {
		return out, err
	}
	out.Stars = len(stars)
	var checks []app.ClaimCheck
	if err := s.DB.WithContext(ctx).Find(&checks).Error; err != nil {
		return out, err
	}
	var rows []app.ClaimMention
	if err := s.DB.WithContext(ctx).Order("first_seen DESC, id").Find(&rows).Error; err != nil {
		return out, err
	}
	byStar := map[int][]app.ClaimCheck{}
	for _, c := range checks {
		byStar[c.SourceID] = append(byStar[c.SourceID], c)
	}
	ments := map[int][]MentionView{}
	for _, r := range rows {
		ments[r.SourceID] = append(ments[r.SourceID], s.mentionView(r))
	}
	common := map[*Record]int{}
	for _, st := range stars {
		v := StarView{SourceID: st.SourceID, Name: st.Name, Record: RecordFor(s.records(), st.SourceID), Mentions: ments[st.SourceID], Checks: []CheckView{}}
		if v.Mentions == nil {
			v.Mentions = []MentionView{}
		}
		if v.Record != nil {
			common[v.Record]++
		}
		got := map[string]app.ClaimCheck{}
		for _, c := range byStar[st.SourceID] {
			got[c.Where] = c
			if v.LastChecked == nil || c.CheckedAt.After(*v.LastChecked) {
				t := c.CheckedAt
				v.LastChecked = &t
			}
		}
		for _, p := range Catalogue() {
			c, ok := got[p.Where]
			cv := CheckView{Where: p.Where, State: app.ClaimNotChecked, Note: "Not checked yet."}
			if ok {
				cv.State, cv.Note = c.State, c.Note
			}
			for _, m := range v.Mentions {
				if m.Where != p.Where {
					continue
				}
				if m.Status == StatusNew {
					cv.New++
				} else {
					cv.Known++
				}
			}
			v.Checks = append(v.Checks, cv)
		}
		for _, m := range v.Mentions {
			if m.Status == StatusNew {
				v.New++
			} else {
				v.Known++
			}
			if m.Open() {
				out.NewCount++
			}
		}
		out.PerStar = append(out.PerStar, v)
	}
	best := 0
	for r, n := range common {
		if n > best {
			best, out.Record = n, r
		}
	}
	sort.SliceStable(out.PerStar, func(i, j int) bool { return out.PerStar[i].SourceID < out.PerStar[j].SourceID })
	return out, nil
}

// NewMention is a mention a check found after the baseline, for the
// morning report.
type NewMention struct {
	MentionView
	StarName string
	Record   *Record
}

// FoundBetween are the new mentions first seen in [from, to).
func (s *Service) FoundBetween(ctx context.Context, from, to time.Time) ([]NewMention, error) {
	var rows []app.ClaimMention
	if err := s.DB.WithContext(ctx).Where("baseline = ? AND own = ? AND first_seen >= ? AND first_seen < ?", false, false, from, to).
		Order("source_id, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]NewMention, 0, len(rows))
	for _, r := range rows {
		out = append(out, NewMention{MentionView: s.mentionView(r), StarName: r.StarName, Record: RecordFor(s.records(), r.SourceID)})
	}
	return out, nil
}
