package match

import (
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const confirmedEvidence = "you confirmed this match"

type Decisions struct {
	pairs map[string]string
}

func pairKey(subject, objectID string) string {
	return subject + "\x00" + objectID
}

func NewDecisions(xrefs []app.ObjectXref) Decisions {
	d := Decisions{pairs: make(map[string]string, len(xrefs))}
	for _, x := range xrefs {
		if x.Decision == app.XrefConfirmed || x.Decision == app.XrefRejected {
			d.pairs[pairKey(x.Subject, x.ObjectID)] = x.Decision
		}
	}
	return d
}

func (d Decisions) Decision(subject, objectID string) string {
	return d.pairs[pairKey(subject, objectID)]
}

func (d Decisions) ApplyResults(subject string, results []Result) []Result {
	out := make([]Result, 0, len(results))
	for _, r := range results {
		switch d.Decision(subject, r.Object.ID) {
		case app.XrefRejected:
			continue
		case app.XrefConfirmed:
			r.Decision = app.XrefConfirmed
			r.Confidence = 1
			r.Evidence = append([]string{confirmedEvidence}, r.Evidence...)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}

func (d Decisions) ApplyExisting(pick catalog.Object, existing []Existing, matches []ExistingMatch) []ExistingMatch {
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[m.Subject] = true
	}
	for _, e := range existing {
		if !seen[e.SubjectKey()] && d.Decision(e.SubjectKey(), pick.ID) == app.XrefConfirmed {
			seen[e.SubjectKey()] = true
			matches = append(matches, ExistingMatch{Subject: e.SubjectKey(), Existing: e})
		}
	}
	out := make([]ExistingMatch, 0, len(matches))
	for _, m := range matches {
		switch d.Decision(m.Subject, pick.ID) {
		case app.XrefRejected:
			continue
		case app.XrefConfirmed:
			m.Decision = app.XrefConfirmed
			m.Confidence = 1
			m.Evidence = append([]string{confirmedEvidence}, m.Evidence...)
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}
