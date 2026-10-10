package match_test

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog/match"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func cygnusLoop() catalog.Object {
	return obj(sh2103, "Cygnus Loop", 312.75, 30.7, 180, "Cygnus Loop Nebula")
}

func existing() []match.Existing {
	return []match.Existing{
		{Kind: match.KindProject, ID: "p1", Name: cygnisLoop, RA: 312.2, Dec: 31.2, HasCoords: true},
		{Kind: match.KindTarget, ID: "t7", Name: "Cygnis Loop Panel 2", RA: 313.5, Dec: 30.6, HasCoords: true},
		{Kind: match.KindStack, ID: sh2103, Name: "SH2-103"},
		{Kind: match.KindProject, ID: "p2", Name: "Veil West", RA: 311.4, Dec: 30.7, HasCoords: true},
		{Kind: match.KindStack, ID: m31, Name: m31, RA: 10.68, Dec: 41.27, HasCoords: true},
		{Kind: match.KindProject, ID: "p3", Name: cygnisLoop, RA: 83.8, Dec: -5.4, HasCoords: true},
		{Kind: match.KindTarget, ID: "t9", Name: garlicName},
	}
}

func byID(ms []match.ExistingMatch) map[string]match.ExistingMatch {
	out := map[string]match.ExistingMatch{}
	for _, m := range ms {
		out[m.Existing.ID] = m
	}
	return out
}

func evidenceHas(ev []string, substr string) bool {
	for _, e := range ev {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func TestExistingMatches(t *testing.T) {
	t.Parallel()
	ms := match.ExistingMatches(cygnusLoop(), existing())
	got := byID(ms)
	cases := []struct {
		id       string
		present  bool
		minConf  float64
		maxConf  float64
		evidence []string
	}{
		{"p1", true, 0.8, 1, []string{"“Cygnis” is one letter from “Cygnus”", fromCentre}},
		{"t7", true, 0.8, 1, []string{"“Cygnis” is one letter", "it is panel 2 of “Cygnis Loop”"}},
		{sh2103, true, 0.85, 1, []string{"name matches designation Sh2-103"}},
		{"p2", true, 0.4, 0.75, []string{fromCentre}},
		{m31, false, 0, 0, nil},
		{"p3", true, 0.1, 0.5, []string{"coordinates do not confirm it"}},
		{"t9", false, 0, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			m, ok := got[tc.id]
			if ok != tc.present {
				t.Fatalf("%s present = %v (%+v)", tc.id, ok, m)
			}
			if !ok {
				return
			}
			if m.Confidence < tc.minConf || m.Confidence > tc.maxConf {
				t.Fatalf("%s confidence %.2f outside %.2f–%.2f (%v)", tc.id, m.Confidence, tc.minConf, tc.maxConf, m.Evidence)
			}
			for _, e := range tc.evidence {
				if !evidenceHas(m.Evidence, e) {
					t.Fatalf("%s evidence %v lacks %q", tc.id, m.Evidence, e)
				}
			}
			if m.Subject != match.Subject(m.Existing.Kind, m.Existing.Name) {
				t.Fatalf("%s subject %q", tc.id, m.Subject)
			}
		})
	}
	for i := 1; i < len(ms); i++ {
		if ms[i].Confidence > ms[i-1].Confidence {
			t.Fatal("matches not sorted by confidence")
		}
	}
}

func TestExistingSubjectOverride(t *testing.T) {
	t.Parallel()
	e := match.Existing{Subject: "stack:Sh2-103", Kind: match.KindStack, Name: "Cygnus Loop mosaic", RA: 312.7, Dec: 30.7, HasCoords: true}
	ms := match.ExistingMatches(cygnusLoop(), []match.Existing{e})
	if len(ms) != 1 || ms[0].Subject != "stack:Sh2-103" {
		t.Fatalf("got %+v", ms)
	}
}

func TestDecisionsApplyExisting(t *testing.T) {
	t.Parallel()
	pick := cygnusLoop()
	ex := existing()
	d := match.NewDecisions([]app.ObjectXref{
		{Subject: "project:Veil West", ObjectID: pick.ID, Decision: app.XrefRejected},
		{Subject: "target:Garlic Nebula", ObjectID: pick.ID, Decision: app.XrefConfirmed},
		{Subject: "project:Cygnis Loop", ObjectID: m31, Decision: app.XrefRejected},
	})
	got := byID(d.ApplyExisting(pick, ex, match.ExistingMatches(pick, ex)))
	if _, ok := got["p2"]; ok {
		t.Fatal("rejected pair kept")
	}
	t9, ok := got["t9"]
	if !ok || t9.Decision != app.XrefConfirmed || t9.Confidence != 1 {
		t.Fatalf("confirmed pair missing or not marked: %+v", t9)
	}
	if _, ok := got["p1"]; !ok {
		t.Fatal("a rejection for another object suppressed p1")
	}
}

func TestDecisionsApplyResults(t *testing.T) {
	t.Parallel()
	results := []match.Result{
		{Object: obj(sh2103, "Cygnus Loop", 312.75, 30.7, 180), Confidence: 0.6},
		{Object: obj("NGC 6960", "Veil Nebula", 311.4, 30.71, 70), Confidence: 0.8},
		{Object: obj("NGC 6992", "Eastern Veil", 314, 31.7, 60), Confidence: 0.5},
	}
	subject := match.Subject(match.KindProject, cygnisLoop)
	d := match.NewDecisions([]app.ObjectXref{
		{Subject: subject, ObjectID: sh2103, Decision: app.XrefConfirmed},
		{Subject: subject, ObjectID: "NGC 6960", Decision: app.XrefRejected},
		{Subject: "project:Other", ObjectID: "NGC 6992", Decision: app.XrefRejected},
	})
	got := d.ApplyResults(subject, results)
	if len(got) != 2 || got[0].Object.ID != sh2103 || got[0].Decision != app.XrefConfirmed || got[0].Confidence != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Evidence[0] != "you confirmed this match" {
		t.Fatalf("evidence %v", got[0].Evidence)
	}
	if got[1].Object.ID != "NGC 6992" || got[1].Decision != "" {
		t.Fatalf("undecided pair changed: %+v", got[1])
	}
}
