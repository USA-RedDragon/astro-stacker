package stacking

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The observatory; 2025-11-05 02:00 UTC is a night of full moon with the Moon
// well up (about 30° high in the east), 2025-10-21 04:00 UTC the new moon,
// below the horizon.
const siteLat, siteLon = 31.546944, -99.382222

var (
	fullMoonUp = time.Date(2025, 11, 5, 2, 0, 0, 0, time.UTC)
	newMoon    = time.Date(2025, 10, 21, 4, 0, 0, 0, time.UTC)
)

func TestMoonRuleBroken(t *testing.T) {
	t.Parallel()
	down := moonRule{down: true}
	if !down.broken(fullMoonUp, 300, 0, 0, siteLat, siteLon) {
		t.Error("moon up passed a moon-must-be-down rule")
	}
	if down.broken(newMoon, 300, 0, 0, siteLat, siteLon) {
		t.Error("moon down broke a moon-must-be-down rule")
	}
	ha := moonRule{distance: 30, width: 7}
	// The full moon that night is near RA 2h40m, Dec +16.
	if !ha.broken(fullMoonUp, 600, 40, 16, siteLat, siteLon) {
		t.Error("a target beside the full moon passed 30° avoidance")
	}
	if ha.broken(fullMoonUp, 600, 220, -16, siteLat, siteLon) {
		t.Error("a target opposite the moon broke 30° avoidance")
	}
}

func TestMoonRulesTakeStrictest(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE exposuretemplate (name text, filtername text, moonavoidanceenabled int,
		moonavoidanceseparation real, moonavoidancewidth int, moonrelaxscale real, moonrelaxminaltitude real, moondownenabled int)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO exposuretemplate VALUES ('Red', 'Red', 1, 30, 7, 1, 0, 1)`,
		`INSERT INTO exposuretemplate VALUES ('R 600', 'Red', 1, 60, 7, 1, 0, 1)`,
		`INSERT INTO exposuretemplate VALUES ('H-a', 'H-a', 1, 30, 7, 0, -15, 0)`,
		`INSERT INTO exposuretemplate VALUES ('O-III', 'O-III', 1, 60, 7, 0, -15, 0)`,
		`INSERT INTO exposuretemplate VALUES ('Off', 'S-II', 0, 30, 7, 0, 0, 0)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	r, err := moonRules(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !r["Red"].down || r["Red"].distance != 60 {
		t.Errorf("Red %+v, want moon down, the 60° template", r["Red"])
	}
	if r["H-a"].down || r["H-a"].distance != 30 || r["O-III"].distance != 60 {
		t.Errorf("narrowband %+v %+v", r["H-a"], r["O-III"])
	}
	if _, ok := r["S-II"]; ok {
		t.Error("a template without moon avoidance made a rule")
	}
}

// The sweep finds added lights that break the rule, by their stack_frames
// row and master.
func TestMoonlitAdded(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	exp := 300.0
	// Master 8 is moon-only and keeps its light.
	for i, at := range []time.Time{fullMoonUp, newMoon, fullMoonUp} {
		stackID := []int{7, 7, 8}[i]
		d := at
		f := app.Frame{Key: []string{"a", "b", "c"}[i], Type: "LIGHT", Object: "Moon Test", Filter: "Red",
			Exposure: &exp, DateObs: &d, LastModified: at}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		// stack_frames ids differ from frame ids, as they do in production.
		if err := db.Create(&app.StackFrame{ID: 100 + f.ID, FrameID: f.ID, StackID: &stackID, Status: app.StackStatusAdded}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sites.Store("Moon Test", [2]float64{siteLat, siteLon})
	p := &Pipeline{db: db}
	check := &moonChecker{p: p, rules: map[string]moonRule{"Red": {down: true}},
		positions: map[string][2]float64{"Moon Test": {10, 10}}}
	ids, stacks, err := p.moonlitAdded(context.Background(), check)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 101 || !stacks[7] || len(stacks) != 1 {
		t.Errorf("moonlit %v in %v, want stack_frames 101 in master 7", ids, stacks)
	}
}

// Telescope.live's subs come calibrated and need no bias, dark or flat.
func TestPrecalibratedNeedsNoCalibration(t *testing.T) {
	t.Parallel()
	exp := 600.0
	night := time.Date(2021, 5, 23, 0, 0, 0, 0, time.UTC)
	for key, want := range map[string]string{
		"Telescope.live/Carina Nebula/CHI-1-CCD_2021-05-23T01-09-54_CarinaNebula_Halpha_600s_ID224182_cal.fits": "",
		"Carina Nebula/LIGHT/2021-05-23_01-09-54_H-a_-10.00_600.00s_0000.xisf":                                  app.StackStatusCalibration,
	} {
		f := app.Frame{Key: key, Object: "Carina Nebula", Filter: "H-a", Exposure: &exp, Night: &night}
		scores := map[string]quality.SubScore{path.Base(key): {Score: 1, TargetBest: 1}}
		if _, status := (&Pipeline{}).classify(f, scores, nil, nil); status != want {
			t.Errorf("%s: status %q, want %q", key, status, want)
		}
	}
}
