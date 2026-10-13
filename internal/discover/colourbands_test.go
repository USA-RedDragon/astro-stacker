package discover_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
)

const (
	osc     = "OSC"
	oscDual = "OSC-Ha+OIII"
	oscRGB  = "OSC-RGB"
)

func collabWith(id string, r starfront.Requirements, goals map[string]float64) starfront.Project {
	return starfront.Project{ID: id, Name: id, Status: starfront.StatusOpen, Created: float64(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).Unix()),
		Payload: starfront.Payload{Kind: "single", Region: starfront.Region{RA: 10.68, Dec: 41.27, Width: 2, Height: 1.5}, Requirements: r, Goals: goals}}
}

func criterion(c discover.Collab, name string) (discover.Criterion, bool) {
	for _, x := range c.Criteria {
		if x.Name == name {
			return x, true
		}
	}
	return discover.Criterion{}, false
}

func TestColourBandNames(t *testing.T) {
	t.Parallel()
	for _, b := range []string{osc, oscRGB, oscDual, "OSC-SII+OIII", "OSC-Ha+SII", "OSC-Ha+OIII+SII", "osc-ha", " OSC-RGB "} {
		if !discover.IsColourBand(b) {
			t.Errorf("%q is a colour band", b)
		}
	}
	for _, b := range []string{"Ha", "H", "O3", oxygen, "L", "R", "OSCAR", ""} {
		if discover.IsColourBand(b) {
			t.Errorf("%q is not a colour band", b)
		}
	}
	for _, c := range [][2]string{{oscDual, oscDual}, {"  OSC-RGB", oscRGB}, {osc, osc}, {"Ha", "H"}, {"O3", "O"}, {"osc-ha+oiii", oscDual}, {"OSC rgb", oscRGB}, {"OSC-Foo", "OSC-Foo"}} {
		if got := discover.BandName(c[0]); got != c[1] {
			t.Errorf("BandName(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestColourBandsAreNotForAMonoRig(t *testing.T) {
	t.Parallel()
	s := newService(t)
	mixed := collabWith("mixed", starfront.Requirements{Filters: map[string]*float64{"H": nil, "O": nil, oscRGB: nil, oscDual: nil}},
		map[string]float64{"H": 10, "O": 10, oscRGB: 5, oscDual: 5})
	all := collabWith("all", starfront.Requirements{Filters: map[string]*float64{oscRGB: nil, oscDual: nil, "OSC-SII+OIII": nil, "OSC-Ha+SII": nil, "OSC-Ha+OIII+SII": nil}}, nil)
	dual := collabWith("dual", starfront.Requirements{Filters: map[string]*float64{oscDual: nil}}, map[string]float64{oscDual: 4})
	flags := collabWith("flags", starfront.Requirements{Filters: map[string]*float64{"H": nil}, AcceptOscRgb: ptr(true), AcceptOscNarrowband: ptr(false)}, nil)
	v, err := s.Collabs(context.Background(), fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{mixed, all, dual, flags}}})
	if err != nil || len(v.Open) != 4 {
		t.Fatalf("%v %d", err, len(v.Open))
	}
	by := map[string]discover.Collab{}
	for _, c := range v.Open {
		by[c.ID] = c
	}
	m := by["mixed"]
	f, _ := criterion(m, "Filters")
	if f.Result != discover.CriterionPass || !strings.HasPrefix(f.You, "H O") || !strings.Contains(f.You, "OSC-Ha+OIII, OSC-RGB need a colour camera") {
		t.Errorf("mixed bands on a mono rig: %+v", f)
	}
	if !slices.Equal(m.ColourOnly, []string{oscDual, oscRGB}) {
		t.Errorf("colour-only bands %v", m.ColourOnly)
	}
	for _, b := range []string{oscRGB, oscDual} {
		row, ok := criterion(m, b)
		if !ok || row.Scope != discover.ScopeInfo || row.Result != discover.CriterionNone || row.You != "mono camera: not for this rig" || !strings.Contains(row.Rule, "colour camera") {
			t.Errorf("%s row %+v", b, row)
		}
	}
	if m.Goals["H"] != 10 || m.Goals[oscDual] != 5 || m.Goals[oscRGB] != 5 || len(m.Goals) != 4 {
		t.Errorf("an OSC goal was merged into a mono filter: %+v", m.Goals)
	}
	if m.Verdict != discover.VerdictFits {
		t.Errorf("mixed verdict %s: %+v", m.Verdict, m.Criteria)
	}
	checkAllColourAndFlags(t, by)
}

func checkAllColourAndFlags(t *testing.T, by map[string]discover.Collab) {
	t.Helper()
	f, _ := criterion(by["all"], "Filters")
	if f.Result != discover.CriterionFail || by["all"].Verdict != discover.VerdictNoFit || len(by["all"].ColourOnly) != 5 {
		t.Errorf("every band colour-only on a mono rig: %+v %s", f, by["all"].Verdict)
	}
	f, _ = criterion(by["dual"], "Filters")
	if f.Result != discover.CriterionFail || strings.Contains(f.You, "H ") || by["dual"].Goals["H"] != 0 || by["dual"].Goals["O"] != 0 {
		t.Errorf("OSC-Ha+OIII mapped to mono H and O: %+v %+v", f, by["dual"].Goals)
	}
	row, ok := criterion(by["flags"], "Colour cameras")
	if !ok || row.Scope != discover.ScopeInfo || !strings.Contains(row.Rule, "(OSC-RGB): yes") || !strings.Contains(row.Rule, "narrowband subs: no") || row.You != "mono camera: does not apply" {
		t.Errorf("colour camera row %+v", row)
	}
	if len(by["flags"].ColourOnly) != 0 || by["flags"].ColourOnly == nil {
		t.Errorf("no colour bands %v", by["flags"].ColourOnly)
	}
}

func TestBortleFromMeasuredSky(t *testing.T) {
	t.Parallel()
	const worst = "Bortle 8 or 9"
	for mag, want := range map[float64]float64{22: 1, 21.7: 2, 21.4: 3, 21: 4, 20.5: 4.5, 19.5: 5, 19: 6, 18.2: 7, 17.5: 8} {
		if got := discover.Bortle(mag); got != want {
			t.Errorf("Bortle(%g) = %g, want %g", mag, got, want)
		}
	}
	mag := 21.42
	from, to := "2026-10-01", "2026-10-08"
	measured := skybright.Value{Mag: &mag, Basis: skybright.Basis{Source: skybright.SourceMeasured, Filter: "L", Band: skybright.Band, Nights: 4, Frames: 60, From: &from, To: &to}}
	cases := []struct {
		name   string
		value  skybright.Value
		max    int
		result string
		you    string
	}{
		{"measured, dark enough", measured, 5, discover.CriterionPass, "Bortle 3, from measured sky 21.42 mag/arcsec² in L subs over 4 nights"},
		{"measured, too bright", measured, 2, discover.CriterionFail, "Bortle 3"},
		{"not measured", skybright.None("no L, R, G or B master with a Gaia zero point has been measured yet"), 5, discover.CriterionOpen, "not measured: no L, R, G or B master"},
		{"set by hand", skybright.Configured(21.9), 5, discover.CriterionOpen, "not measured: the sky brightness is only set by hand"},
		{"brighter than 18, limit 8", skybright.Value{Mag: ptr(17.6), Basis: measured.Basis}, 8, discover.CriterionOpen, worst},
		{"brighter than 18, limit 9", skybright.Value{Mag: ptr(17.6), Basis: measured.Basis}, 9, discover.CriterionPass, worst},
		{"brighter than 18, limit 7", skybright.Value{Mag: ptr(17.6), Basis: measured.Basis}, 7, discover.CriterionFail, worst},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newService(t)
			v := c.value
			s.Sky = func(context.Context) skybright.Value { return v }
			p := collabWith("b", starfront.Requirements{MaxBortle: &c.max}, nil)
			out, err := s.Collabs(context.Background(), fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{p}}})
			if err != nil || len(out.Open) != 1 {
				t.Fatalf("%v %+v", err, out)
			}
			row, ok := criterion(out.Open[0], "Sky darkness")
			if !ok || row.Result != c.result || !strings.HasPrefix(row.You, c.you) || row.Scope != discover.ScopeRig {
				t.Errorf("row %+v", row)
			}
		})
	}
	s := newService(t)
	out, err := s.Collabs(context.Background(), fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{collabWith("n", starfront.Requirements{}, nil)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := criterion(out.Open[0], "Sky darkness"); ok {
		t.Error("a sky row without a Bortle limit")
	}
}
