package stacking

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const moonGolden = "testdata/moon_inert.golden.txt"

func moonSched(t *testing.T, extra ...string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{
		`CREATE TABLE exposuretemplate ("Id" integer primary key, name text, filtername text, moonavoidanceenabled int,
			moonavoidanceseparation real, moonavoidancewidth int, moonrelaxscale real, moonrelaxminaltitude real, moondownenabled int)`,
		`INSERT INTO exposuretemplate VALUES (1, 'Red', 'Red', 1, 30, 7, 0, 0, 0)`,
		`INSERT INTO exposuretemplate VALUES (2, 'R 600', 'Red', 1, 60, 7, 0, 0, 0)`,
		`INSERT INTO exposuretemplate VALUES (3, 'H-a', 'H-a', 1, 30, 7, 1, -15, 0)`,
		`INSERT INTO exposuretemplate VALUES (4, 'Luminance', 'Luminance', 1, 30, 7, 1, 0, 1)`,
		`INSERT INTO exposuretemplate VALUES (5, 'S-II', 'S-II', 0, 30, 7, 0, 0, 0)`,
	}, extra...)
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

func moonFrame(object, filter string, at time.Time, seconds float64) app.Frame {
	d, e := at, seconds
	return app.Frame{Key: object + at.String(), Type: frameTypeLight, Object: object, Filter: filter, Exposure: &e, DateObs: &d}
}

// moonDecisions is the reason, or none, for lights of every filter at moon
// separations from 0° to 90° on nights from new to full moon.
func moonDecisions(t *testing.T, db *gorm.DB) []byte {
	t.Helper()
	p := &Pipeline{sched: db}
	positions := map[string][2]float64{}
	var frames []app.Frame
	for day := 0; day <= 15; day += 3 {
		at := newMoon().Add(time.Duration(day) * 24 * time.Hour)
		for _, hour := range []int{0, 4, 8} {
			start := at.Add(time.Duration(hour-4) * time.Hour)
			m := moon.At(start.Add(150 * time.Second))
			for _, off := range []float64{0, 10, 19.9, 20.1, 25, 45, 70, 90} {
				obj := fmt.Sprintf("d%d h%d o%g", day, hour, off)
				positions[obj] = [2]float64{m.RA, max(-89, min(89, m.Dec+off))}
				p.sites.Store(obj, [2]float64{siteLat, siteLon})
				for _, f := range []string{filterRed, filterHa, filterLuminance, filterSII, filterOIII} {
					frames = append(frames, moonFrame(obj, f, start, 300))
				}
			}
		}
	}
	check, err := p.moonChecker(context.Background(), positions)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range frames {
		fmt.Fprintf(&b, "%s %s %s: %s\n", f.Object, f.Filter, f.DateObs.UTC().Format(time.RFC3339), check.why(context.Background(), f))
	}
	return []byte(b.String())
}

// With no ts_template_role table, or one without retired or managed rows,
// the sweep decides every light as it did before template roles.
func TestMoonSweepInertBeforeMigration(t *testing.T) {
	t.Parallel()
	for name, extra := range map[string][]string{
		"no table":    nil,
		"empty table": {`CREATE TABLE ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`},
	} {
		got := moonDecisions(t, moonSched(t, extra...))
		if os.Getenv("ASTRO_GOLDEN_WRITE") != "" {
			if err := os.WriteFile(moonGolden, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(moonGolden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: moon decisions differ from before template roles", name)
		}
	}
}
