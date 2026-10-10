package mosaics_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func panelProject(guid, project, prefix string, centre mosaics.Point, rows, cols int, overlap float64, numbers []int) mosaics.TSProject {
	centres := gridCentres(centre, rows, cols, 0, overlap, testRig())
	p := mosaics.TSProject{GUID: guid, Name: project, IsMosaic: true}
	for i, c := range centres {
		n := i + 1
		if numbers != nil {
			n = numbers[i]
		}
		p.Targets = append(p.Targets, mosaics.TSTarget{
			GUID: fmt.Sprintf("%s-t%02d", guid, n), Name: fmt.Sprintf("%s Panel %d", prefix, n),
			RA: c.RA, Dec: c.Dec, Active: true,
		})
	}
	return p
}

func markarian() mosaics.TSProject {
	return panelProject("mk", "Markarian Chain", "Markarian Chain", mosaics.Point{RA: 187, Dec: 13}, 3, 3, 0.15, nil)
}

func rho() mosaics.TSProject {
	return panelProject("rho", "Rho", "IC 4604", mosaics.Point{RA: 246.4, Dec: -23.4}, 3, 4, 0.15, nil)
}

func gappy() mosaics.TSProject {
	return panelProject("gap", "Pelican", "Pelican", mosaics.Point{RA: 312.7, Dec: 44.3}, 1, 5, 0.15, []int{1, 2, 3, 6, 12})
}

func rosette() mosaics.TSProject {
	return mosaics.TSProject{GUID: "ros", Name: "Rosette", Targets: []mosaics.TSTarget{
		{GUID: "ros-rgb", Name: "Rosette Nebula", RA: 97.98, Dec: 4.95},
		{GUID: "ros-sho", Name: "Rosette Nebula SHO", RA: 97.98, Dec: 4.95},
	}}
}

func TestPanelName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want string
	}{
		{"IC 4604 Panel 12", "12"},
		{"Markarian Chain Panel 1", "1"},
		{"m31 panel3", "3"},
		{"Heart PANEL 2  ", "2"},
		{"Rosette Nebula", ""},
		{"Panel 4 of Rho", ""},
		{"Subpanel 4", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := mosaics.PanelName.FindStringSubmatch(tc.name)
			got := ""
			if m != nil {
				got = m[1]
			}
			if got != tc.want {
				t.Fatalf("matched %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPropose(t *testing.T) {
	t.Parallel()
	notFlagged := markarian()
	notFlagged.GUID, notFlagged.Name, notFlagged.IsMosaic = "nf", "Markarian Chain", false
	stray := panelProject("stray", "Heart", "Heart", mosaics.Point{RA: 38, Dec: 61.5}, 1, 3, 0.1, nil)
	stray.Targets[2].RA, stray.Targets[2].Dec = 10, 41
	flaggedField := rosette()
	flaggedField.Name, flaggedField.IsMosaic = "Rosette Combined", true
	flaggedField.Targets[1].Name = "NGC 2244"
	loose := mosaics.TSProject{GUID: "ca", Name: "Cygnus Arc", IsMosaic: true, Targets: []mosaics.TSTarget{
		{GUID: "ca-west", Name: "Arc West", RA: mosaics.Offset(mosaics.Point{RA: 310, Dec: 40}, 0, -1.4, 0).RA, Dec: mosaics.Offset(mosaics.Point{RA: 310, Dec: 40}, 0, -1.4, 0).Dec},
		{GUID: "ca-east", Name: "Arc East", RA: mosaics.Offset(mosaics.Point{RA: 310, Dec: 40}, 0, 1.4, 0).RA, Dec: mosaics.Offset(mosaics.Point{RA: 310, Dec: 40}, 0, 1.4, 0).Dec},
	}}
	cases := []struct {
		name       string
		project    mosaics.TSProject
		kind       string
		confidence string
		auto       bool
		panels     int
		issue      []string
		suggestion []string
	}{
		{"markarian chain", markarian(), mosaics.KindMosaic, mosaics.ConfidenceHigh, true, 9, nil, []string{"Adopt as a 9-panel mosaic."}},
		{"rho with ic 4604 panels", rho(), mosaics.KindMosaic, mosaics.ConfidenceHigh, false, 12,
			[]string{`"IC 4604 Panel N"`, `"Rho"`},
			[]string{"Adopt as a 12-panel mosaic and keep the panel names, so the stacker still finds its frames."}},
		{"gappy numbering", gappy(), mosaics.KindMosaic, mosaics.ConfidenceMedium, false, 5,
			[]string{"1, 2, 3, 6, 12", "1 to 5"},
			[]string{"Keep the numbers as they are and place panels by their coordinates, not their numbers."}},
		{"rosette", rosette(), mosaics.KindNotMosaic, mosaics.ConfidenceMedium, false, 0,
			[]string{`Its 2 targets are on the same field: "Rosette Nebula" and "Rosette Nebula SHO".`},
			[]string{"Don't adopt it as a mosaic. Treat it as one target with 2 exposure sets."}},
		{"flagged co-located", flaggedField, mosaics.KindNotMosaic, mosaics.ConfidenceMedium, false, 0,
			[]string{"same field"}, []string{"2 exposure sets"}},
		{"not flagged", notFlagged, mosaics.KindMosaic, mosaics.ConfidenceMedium, false, 9,
			[]string{"TS doesn't flag it as a mosaic."}, []string{"Adopt it as a mosaic"}},
		{"stray panel", stray, mosaics.KindMosaic, mosaics.ConfidenceMedium, false, 3,
			[]string{"Panel 3 overlaps no other panel"}, []string{"Panel 3"}},
		{"loose names", loose, mosaics.KindMosaic, mosaics.ConfidenceLow, false, 2,
			[]string{"aren't named"}, []string{"Check the panel order"}},
		{"eight panels", panelProject("e8", "Veil", "Veil", mosaics.Point{RA: 312, Dec: 31}, 2, 4, 0.2, nil), mosaics.KindMosaic, mosaics.ConfidenceHigh, true, 8, nil, []string{"Adopt as an 8-panel mosaic."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps := mosaics.Propose([]mosaics.TSProject{tc.project}, testRig())
			if len(ps) != 1 {
				t.Fatalf("%d proposals", len(ps))
			}
			p := ps[0]
			if p.Kind != tc.kind || p.Confidence != tc.confidence || p.Auto != tc.auto || len(p.Panels) != tc.panels {
				t.Fatalf("got %s %s auto=%v with %d panels, want %s %s auto=%v with %d: %q %q",
					p.Kind, p.Confidence, p.Auto, len(p.Panels), tc.kind, tc.confidence, tc.auto, tc.panels, p.Issue, p.Suggestion)
			}
			if p.ProjectGUID != tc.project.GUID || p.Project != tc.project.Name || len(p.Fingerprint) != 16 {
				t.Fatalf("identity %q %q %q", p.ProjectGUID, p.Project, p.Fingerprint)
			}
			if tc.auto != (p.Issue == "") {
				t.Fatalf("auto %v with issue %q", p.Auto, p.Issue)
			}
			for _, s := range tc.issue {
				if !strings.Contains(p.Issue, s) {
					t.Fatalf("issue %q lacks %q", p.Issue, s)
				}
			}
			for _, s := range tc.suggestion {
				if !strings.Contains(p.Suggestion, s) {
					t.Fatalf("suggestion %q lacks %q", p.Suggestion, s)
				}
			}
		})
	}
}

func TestProposeMarkarianGeometry(t *testing.T) {
	t.Parallel()
	project := markarian()
	p := mosaics.Propose([]mosaics.TSProject{project}, testRig())[0]
	want := map[int]int{1: 2, 2: 3, 3: 2, 4: 3, 5: 4, 6: 3, 7: 2, 8: 3, 9: 2}
	for i, a := range p.Panels {
		if a.Panel != i+1 || a.Row != i/3 || a.Col != i%3 {
			t.Fatalf("panel %d at %d,%d numbered %d", i, a.Row, a.Col, a.Panel)
		}
		if a.TargetGUID != project.Targets[i].GUID || a.Target != project.Targets[i].Name {
			t.Fatalf("panel %d is %q %q", a.Panel, a.TargetGUID, a.Target)
		}
		if a.Footprint != mosaics.PanelFootprint(a.Centre, a.RotationDeg, testRig()) {
			t.Fatalf("panel %d footprint does not match its centre", a.Panel)
		}
		if len(a.Neighbours) != want[a.Panel] {
			t.Fatalf("panel %d has neighbours %v, want %d", a.Panel, a.Neighbours, want[a.Panel])
		}
	}
	centre := p.Panels[4]
	got := strings.Join(centre.Neighbours, ",")
	if got != "mk-t02,mk-t04,mk-t06,mk-t08" {
		t.Fatalf("centre panel neighbours %s", got)
	}
}

func TestProposeRhoPlacesByCoordinates(t *testing.T) {
	t.Parallel()
	project := rho()
	project.Targets[0], project.Targets[5] = project.Targets[5], project.Targets[0]
	p := mosaics.Propose([]mosaics.TSProject{project}, testRig())[0]
	for i, a := range p.Panels {
		if a.Panel != i+1 || a.Row != i/4 || a.Col != i%4 {
			t.Fatalf("panel %d at %d,%d", a.Panel, a.Row, a.Col)
		}
	}
}

func TestProposeGappyRowsAndCols(t *testing.T) {
	t.Parallel()
	p := mosaics.Propose([]mosaics.TSProject{gappy()}, testRig())[0]
	wantPanels := []int{1, 2, 3, 6, 12}
	for i, a := range p.Panels {
		if a.Panel != wantPanels[i] || a.Row != 0 || a.Col != i {
			t.Fatalf("panel %d at %d,%d, want %d at 0,%d", a.Panel, a.Row, a.Col, wantPanels[i], i)
		}
	}
}

func TestProposeLooseNamesNumberEastFirst(t *testing.T) {
	t.Parallel()
	c := mosaics.Point{RA: 310, Dec: 40}
	west, east := mosaics.Offset(c, 0, -1.4, 0), mosaics.Offset(c, 0, 1.4, 0)
	project := mosaics.TSProject{GUID: "ca", Name: "Cygnus Arc", IsMosaic: true, Targets: []mosaics.TSTarget{
		{GUID: "w", Name: "Arc West", RA: west.RA, Dec: west.Dec},
		{GUID: "e", Name: "Arc East", RA: east.RA, Dec: east.Dec},
	}}
	p := mosaics.Propose([]mosaics.TSProject{project}, testRig())[0]
	if p.Panels[0].TargetGUID != "e" || p.Panels[0].Panel != 1 || p.Panels[1].TargetGUID != "w" || p.Panels[1].Panel != 2 {
		t.Fatalf("panels %+v", p.Panels)
	}
	if p.Panels[0].Neighbours[0] != "w" {
		t.Fatalf("neighbours %v", p.Panels[0].Neighbours)
	}
}

func TestProposeSkipsAndOrders(t *testing.T) {
	t.Parallel()
	single := mosaics.TSProject{GUID: "s", Name: "M42", IsMosaic: true, Targets: []mosaics.TSTarget{{GUID: "m42", Name: "M42", RA: 83.8, Dec: -5.4}}}
	empty := mosaics.TSProject{GUID: "e", Name: "Empty", IsMosaic: true}
	unrelated := mosaics.TSProject{GUID: "u", Name: "Galaxies", Targets: []mosaics.TSTarget{
		{GUID: "m81", Name: "M81", RA: 148.9, Dec: 69.1},
		{GUID: "m101", Name: "M101", RA: 210.8, Dec: 54.3},
	}}
	twins := mosaics.TSProject{GUID: "tw", Name: "Odd Pair", Targets: []mosaics.TSTarget{
		{GUID: "a", Name: "Alpha", RA: 10, Dec: 10},
		{GUID: "b", Name: "Beta", RA: 10, Dec: 10},
	}}
	ps := mosaics.Propose([]mosaics.TSProject{rosette(), single, rho(), empty, unrelated, twins, markarian(), gappy()}, testRig())
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Project)
	}
	if got := strings.Join(names, "|"); got != "Markarian Chain|Pelican|Rho|Rosette" {
		t.Fatalf("proposals %s", got)
	}
	if ps := mosaics.Propose(nil, testRig()); len(ps) != 0 {
		t.Fatalf("proposals from nothing: %v", ps)
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	base := rho()
	fp := mosaics.Fingerprint(base)
	mutate := func(f func(p *mosaics.TSProject)) mosaics.TSProject {
		p := rho()
		f(&p)
		return p
	}
	cases := []struct {
		name string
		p    mosaics.TSProject
		same bool
	}{
		{"unchanged", rho(), true},
		{"reordered", mutate(func(p *mosaics.TSProject) { p.Targets[0], p.Targets[3] = p.Targets[3], p.Targets[0] }), true},
		{"tiny ra jitter", mutate(func(p *mosaics.TSProject) { p.Targets[2].RA += 1e-6 }), true},
		{"active flag ignored", mutate(func(p *mosaics.TSProject) { p.Targets[2].Active = false }), true},
		{"guid ignored for project", mutate(func(p *mosaics.TSProject) { p.GUID = "other" }), true},
		{"moved", mutate(func(p *mosaics.TSProject) { p.Targets[2].Dec += 1e-3 }), false},
		{"rotation changed", mutate(func(p *mosaics.TSProject) { p.Targets[2].Rotation = 90 }), false},
		{"renamed project", mutate(func(p *mosaics.TSProject) { p.Name = "Rho Ophiuchi" }), false},
		{"renamed target", mutate(func(p *mosaics.TSProject) { p.Targets[0].Name = "IC 4604 Panel 13" }), false},
		{"flag", mutate(func(p *mosaics.TSProject) { p.IsMosaic = false }), false},
		{"target removed", mutate(func(p *mosaics.TSProject) { p.Targets = p.Targets[1:] }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mosaics.Fingerprint(tc.p)
			if len(got) != 16 {
				t.Fatalf("fingerprint %q", got)
			}
			if (got == fp) != tc.same {
				t.Fatalf("fingerprint %s vs %s, same=%v", got, fp, tc.same)
			}
		})
	}
	a := mosaics.Propose([]mosaics.TSProject{base}, testRig())
	b := mosaics.Propose([]mosaics.TSProject{rho()}, testRig())
	if a[0].Fingerprint != b[0].Fingerprint || a[0].Fingerprint != fp {
		t.Fatalf("re-running changed the fingerprint")
	}
}
