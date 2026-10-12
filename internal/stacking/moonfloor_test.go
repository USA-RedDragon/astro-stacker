package stacking

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const roleTableSQL = `CREATE TABLE ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`

const (
	nearBefore = "near before"
	nearAfter  = "near after"
	wideAfter  = "wide after"
	ruleWhy    = "the filter's moon avoidance needed"
)

func moonAt(t *testing.T, start time.Time, seconds float64) moon.Position {
	t.Helper()
	mid := start.Add(time.Duration(seconds / 2 * float64(time.Second)))
	m := moon.At(mid)
	if alt := m.Altitude(mid, siteLat, siteLon); alt <= 0 {
		t.Fatalf("the Moon is at %.1f° at %s; the test needs it up", alt, mid)
	}
	return m
}

func TestMoonFloor(t *testing.T) {
	t.Parallel()
	start := fullMoonUp()
	m := moonAt(t, start, 300)
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
	at, err := retiredAt(context.Background(), db)
	if err != nil || at != nil {
		t.Errorf("managed rows alone set a retirement time: %v %v", at, err)
	}
}

// Lights from before the templates were retired keep the templates' rules;
// lights from then on keep the moon floor.
func TestMoonCheckerSplitsAtRetirement(t *testing.T) {
	t.Parallel()
	before := fullMoonUp()
	retired := before.Add(time.Hour)
	after := before.Add(2 * time.Hour)
	db := moonSched(t, roleTableSQL,
		`INSERT INTO ts_template_role VALUES (1, 't1', 'p', 'Red', 'retired', '`+retired.Add(time.Hour).Format(time.RFC3339)+`')`,
		`INSERT INTO ts_template_role VALUES (2, 't2', 'p', 'Red', 'retired', '`+retired.Format(time.RFC3339)+`')`,
		`INSERT INTO ts_template_role VALUES (5, 't5', 'p', 'S-II', 'retired', '`+retired.Add(2*time.Hour).Format(time.RFC3339)+`')`)
	p := &Pipeline{sched: db}
	mb, ma := moonAt(t, before, 300), moonAt(t, after, 300)
	positions := map[string][2]float64{
		nearBefore:   {mb.RA, mb.Dec + 15},
		"far before": {mb.RA, mb.Dec + 25},
		nearAfter:    {ma.RA, ma.Dec + 15},
		"far after":  {ma.RA, ma.Dec + 25},
		"edge after": {ma.RA, ma.Dec + 19.9},
		wideAfter:    {ma.RA, ma.Dec + 20.1},
	}
	for obj := range positions {
		p.sites.Store(obj, [2]float64{siteLat, siteLon})
	}
	check, err := p.moonChecker(context.Background(), positions)
	if err != nil {
		t.Fatal(err)
	}
	if check.retiredAt == nil || !check.retiredAt.Equal(retired) {
		t.Fatalf("retired at %v, want the earliest retired row %v", check.retiredAt, retired)
	}
	floor := "the scheduler never images within 20° of the Moon"
	cases := []struct {
		obj, filter string
		at          time.Time
		want        string
	}{
		{nearBefore, filterRed, before, ruleWhy},
		{"far before", filterRed, before, ruleWhy},
		{nearBefore, filterSII, before, ""},
		{nearAfter, filterRed, after, "the Moon was 15° from the target at mid-exposure; " + floor},
		{"far after", filterRed, after, ""},
		{nearAfter, filterSII, after, floor},
		{"edge after", filterHa, after, "19.9°"},
		{wideAfter, filterHa, after, ""},
		{wideAfter, filterRed, retired, ""},
		{nearAfter, filterRed, retired.Add(-time.Second), ruleWhy},
	}
	for _, c := range cases {
		got := check.why(context.Background(), moonFrame(c.obj, c.filter, c.at, 300))
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %s at %s: %q, want %q", c.obj, c.filter, c.at.Format(time.RFC3339), got, c.want)
		}
	}
	night := newMoon()
	md := moon.At(night.Add(150 * time.Second))
	p.sites.Store("moon down", [2]float64{siteLat, siteLon})
	check.positions["moon down"] = [2]float64{md.RA, md.Dec}
	db.Exec(`UPDATE ts_template_role SET changed_at = ?`, night.Add(-time.Hour).Format(time.RFC3339))
	check, err = p.moonChecker(context.Background(), check.positions)
	if err != nil {
		t.Fatal(err)
	}
	if got := check.why(context.Background(), moonFrame("moon down", filterRed, night, 300)); got != "" {
		t.Errorf("a light under a Moon below the horizon broke the floor: %q", got)
	}
}

func TestRetiredAtReadsTimestamps(t *testing.T) {
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
