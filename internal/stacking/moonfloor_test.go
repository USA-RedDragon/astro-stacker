package stacking

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const roleTableSQL = `CREATE TABLE ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`

func moonAt(t *testing.T, start time.Time) moon.Position {
	t.Helper()
	mid := start.Add(150 * time.Second)
	m := moon.At(mid)
	if alt := m.Altitude(mid, siteLat, siteLon); alt <= 0 {
		t.Fatalf("the Moon is at %.1f° at %s; the test needs it up", alt, mid)
	}
	return m
}

func TestMoonFloor(t *testing.T) {
	t.Parallel()
	start := fullMoonUp()
	m := moonAt(t, start)
	why := floorWhy(start, 300, m.RA, m.Dec+19.9, siteLat, siteLon)
	if why != "the Moon was 19.9° from the target at mid-exposure; the scheduler never images within 20° of the Moon" {
		t.Errorf("19.9°: %q", why)
	}
	if why := floorWhy(start, 300, m.RA, m.Dec+20.1, siteLat, siteLon); why != "" {
		t.Errorf("20.1° broke the floor: %q", why)
	}
	if why := floorWhy(start, 300, m.RA, m.Dec+14.2, siteLat, siteLon); why != "the Moon was 14° from the target at mid-exposure; the scheduler never images within 20° of the Moon" {
		t.Errorf("14°: %q", why)
	}
	down := newMoon()
	md := moon.At(down.Add(150 * time.Second))
	if alt := md.Altitude(down.Add(150*time.Second), siteLat, siteLon); alt > 0 {
		t.Fatalf("the Moon is up at %.1f°", alt)
	}
	if why := floorWhy(down, 300, md.RA, md.Dec, siteLat, siteLon); why != "" {
		t.Errorf("a Moon below the horizon broke the floor: %q", why)
	}
}

func TestManagedTemplatesMakeNoMoonRule(t *testing.T) {
	t.Parallel()
	db := moonSched(t, roleTableSQL,
		`INSERT INTO exposuretemplate VALUES (13, 'Luminance (auto)', 'Luminance', 1, 120, 7, 0, 0, 0)`,
		`INSERT INTO exposuretemplate VALUES (14, 'O-III (auto)', 'O-III', 1, 120, 7, 0, 0, 0)`,
		`INSERT INTO ts_template_role VALUES (13, 'm13', 'p', 'Luminance', 'managed', '2026-10-12T03:00:00Z'), (14, 'm14', 'p', 'O-III', 'managed', '2026-10-12T03:00:00Z')`)
	r, err := moonRules(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r[filterOIII]; ok {
		t.Errorf("a managed template made a rule: %+v", r[filterOIII])
	}
	if l := r[filterLuminance]; !l.down || l.distance != 30 {
		t.Errorf("Luminance %+v, want only the unmanaged template's rule", l)
	}
}

const (
	planTables = `CREATE TABLE exposureplan ("Id" integer primary key, guid text, "exposureTemplateId" integer);
		CREATE TABLE ts_template_migration (plan_id integer not null, plan_guid text, target_id integer, profile_id text not null, filter text not null,
			old_template_id integer not null, old_template_guid text, old_template_name text, new_template_id integer not null, migrated_at text not null,
			reverted_at text, PRIMARY KEY (plan_id, migrated_at))`
	floorWhyText = "the scheduler never images within 20° of the Moon"
)

func floorSched(t *testing.T, extra ...string) *gorm.DB {
	t.Helper()
	stmts := []string{roleTableSQL}
	stmts = append(stmts, strings.Split(planTables, ";")...)
	stmts = append(stmts,
		`INSERT INTO exposuretemplate VALUES (13, 'Red (auto)', 'Red', 0, 0, 0, 0, 0, 0)`,
		`INSERT INTO ts_template_role VALUES (13, 'm13', 'p', 'Red', 'managed', '2026-10-12T03:00:00Z'), (1, 't1', 'p', 'Red', 'retired', '2026-10-12T03:00:00Z')`)
	return moonSched(t, append(stmts, extra...)...)
}

func linkedFrame(obj, plan string, at time.Time) app.Frame {
	f := moonFrame(obj, filterRed, at)
	if plan != "" {
		f.TSExposurePlan = &plan
	}
	return f
}

// Each light keeps the floor when its plan took it on a managed template:
// inside a migration period, or on a plan born managed. Other lights keep
// the templates' rules.
func TestMoonCheckerDecidesPerFrame(t *testing.T) {
	t.Parallel()
	base := fullMoonUp()
	migrated, reverted := base.Add(time.Hour), base.Add(3*time.Hour)
	db := floorSched(t,
		`INSERT INTO exposureplan VALUES (40, 'e40', 13), (41, 'e41', 1), (42, 'e42', 13), (43, 'e43', 13), (44, 'e44', 1)`,
		`INSERT INTO ts_template_migration (plan_id, profile_id, filter, old_template_id, new_template_id, migrated_at, reverted_at) VALUES
			(40, 'p', 'Red', 1, 13, '`+migrated.Format(time.RFC3339)+`', null),
			(41, 'p', 'Red', 1, 13, '`+migrated.Format(time.RFC3339)+`', '`+reverted.Format(time.RFC3339)+`'),
			(43, 'p', 'Red', 1, 13, '`+migrated.Format(time.RFC3339)+`', '`+reverted.Format(time.RFC3339)+`')`)
	p := &Pipeline{sched: db}
	check, err := p.moonChecker(context.Background(), map[string][2]float64{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, plan string
		at         time.Time
		floor      bool
	}{
		{"migrated plan before its migration", "e40", base, false},
		{"migrated plan after its migration", "e40", base.Add(2 * time.Hour), true},
		{"reverted plan inside the period", "e41", base.Add(2 * time.Hour), true},
		{"reverted plan after the revert", "e41", base.Add(4 * time.Hour), false},
		{"plan born managed", "e42", base, true},
		{"managed again after a period", "e43", base.Add(4 * time.Hour), true},
		{"managed again before its first period", "e43", base, false},
		{"plan never migrated", "e44", base.Add(2 * time.Hour), false},
		{"no plan link", "", base.Add(2 * time.Hour), false},
		{"unknown plan", "nope", base.Add(2 * time.Hour), false},
	}
	for _, c := range cases {
		m := moonAt(t, c.at)
		obj := c.name
		p.sites.Store(obj, [2]float64{siteLat, siteLon})
		check.positions[obj] = [2]float64{m.RA, m.Dec + 15}
		got := check.why(context.Background(), linkedFrame(obj, c.plan, c.at))
		if strings.Contains(got, floorWhyText) != c.floor || got == "" {
			t.Errorf("%s: %q, want floor %v", c.name, got, c.floor)
		}
	}
}

// Lights matched to a plan through Target Scheduler's acquired images, by
// target and file name, are decided the same way.
func TestMoonCheckerLinksPlansByFileName(t *testing.T) {
	t.Parallel()
	at := fullMoonUp().Add(2 * time.Hour)
	db := floorSched(t,
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, enablegrader INTEGER)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, projectid INTEGER)`,
		`CREATE TABLE acquiredimage ("Id" INTEGER PRIMARY KEY, "projectId" INTEGER, "targetId" INTEGER, acquireddate INTEGER,
			filtername TEXT, "gradingStatus" INTEGER, metadata TEXT, rejectreason TEXT, "profileId" TEXT, "exposureId" INTEGER, guid TEXT)`,
		`INSERT INTO project VALUES (1, 'P', 1)`,
		`INSERT INTO target VALUES (87, 'Bode', 1)`,
		`INSERT INTO exposureplan VALUES (42, 'e42', 13), (44, 'e44', 1)`,
		`INSERT INTO acquiredimage VALUES (10, 1, 87, 0, 'Red', 0, '{"FileName":"A:\\NINA\\Bode\\LIGHT\\r1.xisf"}', '', 'p', 42, 'g10'),
			(11, 1, 87, 0, 'Red', 0, '{"FileName":"A:\\NINA\\Bode\\LIGHT\\r2.xisf"}', '', 'p', 44, 'g11')`)
	p := &Pipeline{sched: db}
	m := moonAt(t, at)
	p.sites.Store("Bode", [2]float64{siteLat, siteLon})
	check, err := p.moonChecker(context.Background(), map[string][2]float64{"Bode": {m.RA, m.Dec + 15}})
	if err != nil {
		t.Fatal(err)
	}
	for file, floor := range map[string]bool{"r1.xisf": true, "r2.xisf": false} {
		f := moonFrame("Bode", filterRed, at)
		f.Key = "Bode/LIGHT/" + file
		if got := check.why(context.Background(), f); strings.Contains(got, floorWhyText) != floor || got == "" {
			t.Errorf("%s: %q, want floor %v", file, got, floor)
		}
	}
}

func TestMoonFloorNeedsTheMoonUp(t *testing.T) {
	t.Parallel()
	night := newMoon()
	db := floorSched(t, `INSERT INTO exposureplan VALUES (42, 'e42', 13)`)
	p := &Pipeline{sched: db}
	md := moon.At(night.Add(150 * time.Second))
	p.sites.Store("moon down", [2]float64{siteLat, siteLon})
	check, err := p.moonChecker(context.Background(), map[string][2]float64{"moon down": {md.RA, md.Dec}})
	if err != nil {
		t.Fatal(err)
	}
	if got := check.why(context.Background(), linkedFrame("moon down", "e42", night)); got != "" {
		t.Errorf("a light under a Moon below the horizon broke the floor: %q", got)
	}
}

// The schema here has no exposureplan or acquiredimage table, so any read
// of them fails the test.
func TestNoMigrationReadsNoPlans(t *testing.T) {
	t.Parallel()
	db := moonSched(t, roleTableSQL, strings.Split(planTables, ";")[1])
	fp, err := (&Pipeline{sched: db}).loadFloorPlans(context.Background())
	if err != nil || len(fp.periods)+len(fp.managed)+len(fp.byGUID)+len(fp.byFile) != 0 {
		t.Errorf("floor plans %+v %v, want none before any plan imaged on a managed template", fp, err)
	}
}

func TestChangedAtReadsTimestamps(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"2026-10-12T03:00:00Z", "2026-10-12 03:00:00+00:00", "2026-10-12T03:00:00.000000Z", "2026-10-12 03:00:00"} {
		got, err := parseChangedAt(v)
		if err != nil || !got.Equal(time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)) {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	if _, err := parseChangedAt("yesterday"); err == nil {
		t.Error("parsed a bad time")
	}
}

// An event window with ignore_moon lets its target image beside the Moon:
// neither the templates' rules nor the floor take its lights out.
func TestMoonCheckerHonorsIgnoreMoon(t *testing.T) {
	t.Parallel()
	before := fullMoonUp()
	after := before.Add(2 * time.Hour)
	db := floorSched(t,
		`CREATE TABLE target ("Id" integer primary key, name text, guid text)`,
		`CREATE TABLE ts_target_window (target_guid text primary key, start_utc text, end_utc text, priority text, event_id text, ignore_moon integer, updated_at text)`,
		`INSERT INTO target VALUES (1, 'Moon and Pleiades', 'gm'), (2, 'Mars and M 44', 'gx')`,
		`INSERT INTO ts_target_window VALUES ('gm', '2025-11-04T00:00:00Z', '2025-11-06T00:00:00Z', 'high', 'ev-moon', 1, null), ('gx', '2025-11-04T00:00:00Z', '2025-11-06T00:00:00Z', 'high', 'ev-mars', 0, null)`,
		`INSERT INTO exposureplan VALUES (42, 'e42', 13), (44, 'e44', 1)`)
	p := &Pipeline{sched: db}
	mb, ma := moonAt(t, before), moonAt(t, after)
	check, err := p.moonChecker(context.Background(), map[string][2]float64{})
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []string{"Moon and Pleiades", "Mars and M 44"} {
		p.sites.Store(obj, [2]float64{siteLat, siteLon})
		for _, c := range []struct {
			at   time.Time
			plan string
			pos  [2]float64
		}{{before, "e44", [2]float64{mb.RA, mb.Dec + 5}}, {after, "e42", [2]float64{ma.RA, ma.Dec + 5}}} {
			check.positions[obj] = c.pos
			got := check.why(context.Background(), linkedFrame(obj, c.plan, c.at))
			if (obj == "Moon and Pleiades") != (got == "") {
				t.Errorf("%s at %s: %q", obj, c.at.Format(time.RFC3339), got)
			}
		}
	}
}
