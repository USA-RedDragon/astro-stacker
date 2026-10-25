package vetting

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// flux41445 is the light the audit of 41445 measured at the spot: H-a on
// six nights of 2025-02 and the S-II and O-III frames of 2025-02-28, each
// filter's zero point set so its 5 sigma limit and continuum light match
// the audit's.
func flux41445(t *testing.T) *ownimage.FluxCurve {
	t.Helper()
	raw, err := os.ReadFile("testdata/flux_41445.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	var fc ownimage.FluxCurve
	if err := json.Unmarshal(text, &fc); err != nil {
		t.Fatal(err)
	}
	return &fc
}

// fieldChange is one change of a run: its source, signal to noise, subs
// that show it and whether a Gaia star sits there.
type fieldChange struct {
	source  int
	snr     float64
	present int
	star    bool
}

// rivals41445 are run 804's H-a changes of 2025-02-28 in the Rosette
// Nebula with their signal to noise, subs and whether a Gaia star sits
// there: 41445 itself at 9.29, then the field's other positive changes
// at 7 or more.
func rivals41445() []fieldChange {
	return []fieldChange{
		{41445, 9.29, 7, false}, {41417, 11.40, 8, true}, {41440, 11.23, 6, true}, {41437, 9.02, 7, false}, {41439, 8.04, 8, true},
		{41444, 8.00, 5, false}, {41433, 7.63, 7, false}, {63688, 7.30, 4, false}, {41442, 7.04, 7, true},
	}
}

// TestLineEmission41445 is 41445, light in H-a alone on 2025-02-28 with
// nothing in the S-II frames between the H-a ones where light of every
// colour would show at 10, as faint as three other star-free spots of
// the same frames. It must read as line emission only, take no ATLAS
// confirmation, and not be suggested real.
func TestLineEmission41445(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "line41445")
	if err := db.AutoMigrate(&app.DiscoveryCandidate{}); err != nil {
		t.Fatal(err)
	}
	night := nightDate("2025-02-28")
	var own app.DiscoveryCandidate
	for _, r := range rivals41445() {
		src := r.source
		c := app.DiscoveryCandidate{RunID: 804, Object: "Rosette Nebula SHO", Night: night, Group: "H-a", Kind: app.DiscoveryKindChange, Sign: 1, SNR: r.snr,
			PresentIn: r.present, SubsTested: 8, SourceID: &src}
		if r.star {
			g := 15.0
			c.GaiaG = &g
		}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
		if r.source == 41445 {
			mag := 17.26
			c.NightMag = &mag
			if err := db.Save(&c).Error; err != nil {
				t.Fatal(err)
			}
			own = c
		}
	}
	s := &Service{App: db}
	sum := discoverysource.Summary{Members: []app.DiscoveryCandidate{own}, Best: own}
	fx := s.transientFacts(t.Context(), surveys.Field{}, sum, &ownimage.Result{Flux: flux41445(t)}, false)
	if !fx.line.only {
		t.Fatalf("not line emission only: %s", fx.line.check.Evidence)
	}
	t.Logf("%s: %s", fx.line.check.Status, fx.line.check.Evidence)
	if !strings.Contains(fx.line.check.Evidence, "Line emission only") || !strings.Contains(fx.line.check.Evidence, "S-II") {
		t.Errorf("the line check does not say so in plain words: %s", fx.line.check.Evidence)
	}
	if fx.field.n != 3 {
		t.Errorf("counted %d rivals at 0.8 of 9.29 or more, want 3 (9.02, 8.00, 7.63)", fx.field.n)
	}
	var rows []surveys.ATLASRow
	for _, r := range atlasFixture(t, "atlas_41445.csv.zst") {
		if !r.ChiCut() {
			rows = append(rows, r)
		}
	}
	fx.atlas, fx.rows = surveys.Fetch{State: surveys.Done}, rows
	groups := []Group{
		{ID: check.GroupImage, Checks: []check.Check{check.Skipped(check.PSF, "Not measured yet.")}},
		{ID: check.GroupTransient, Checks: transientGroup(fx)},
		{ID: check.GroupMoving, Checks: []check.Check{check.New(check.Stays, check.Pass, "No known asteroid or comet was near it.")}},
	}
	if st := status(groups, check.TransATLAS); st == check.Pass {
		t.Errorf("ATLAS confirmed line emission: %s", evidence(groups, check.TransATLAS))
	}
	sug := transientSuggest(groups, fx)
	t.Logf("%s (%s): %s %s", sug.State, sug.Confidence, sug.Say, sug.Why)
	if sug.State == app.VetReal {
		t.Errorf("suggested real: %s", sug.Say)
	}
	if fx.line = emissionCheck(nil, nil); fx.line.only {
		t.Fatal("no curve read as line emission")
	}
}

// TestLineContinuum is light found in H-a that the night's S-II frames
// show too, at the share the zero points give: it is no line emitter.
func TestLineContinuum(t *testing.T) {
	t.Parallel()
	fc := flux41445(t)
	for i, n := range fc.Nights {
		if n.Night == "2025-02-28" && n.Filter == "S-II" {
			fc.Nights[i].Flux = 0.0055
		}
	}
	l := emissionCheck(fc, []string{"H-a"})
	t.Logf("%s: %s", l.check.Status, l.check.Evidence)
	if l.only || l.check.Status != check.Pass {
		t.Errorf("continuum light read as line emission: %s %s", l.check.Status, l.check.Evidence)
	}
	if l := emissionCheck(fc, []string{"broadband"}); l.only || l.check.Status != check.NotRun {
		t.Errorf("a broadband find was judged: %s %s", l.check.Status, l.check.Evidence)
	}
}

// TestLineSurvey is a survey check of line emission: a survey that saw
// it still confirms it, and one that saw nothing does not count.
func TestLineSurvey(t *testing.T) {
	t.Parallel()
	if c := lineSurvey(check.New(check.TransATLAS, check.Pass, "seen")); c.Status != check.Pass {
		t.Errorf("a survey that saw it no longer confirms: %s", c.Status)
	}
	if c := lineSurvey(check.New(check.TransATLAS, check.Fail, "nothing")); c.Status != check.NotRun || !strings.Contains(c.Evidence, "line emission only") {
		t.Errorf("a survey that saw nothing still counts: %s %s", c.Status, c.Evidence)
	}
}
