package sublength

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func exec(t *testing.T, db *gorm.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

const (
	targetGUID = "g-m31"
	objectM31  = "M31"
	filterRed  = "Red"
)

func fixture(t *testing.T, withTable bool) (*gorm.DB, *gorm.DB) {
	t.Helper()
	sched, appDB := openDB(t), openDB(t)
	exec(t, sched,
		`CREATE TABLE project ("Id" integer, name text, state integer, guid text, "isMosaic" integer, minimumaltitude real)`,
		`CREATE TABLE target ("Id" integer, projectid integer, active integer, ra real, dec real, name text, guid text, rotation real)`,
		`CREATE TABLE exposuretemplate ("Id" integer, filtername text, gain integer, defaultexposure real)`,
		`CREATE TABLE exposureplan ("Id" integer, "exposureTemplateId" integer, targetid integer, enabled integer, exposure real)`,
		`CREATE TABLE acquiredimage ("Id" integer, "targetId" integer, "gradingStatus" integer, metadata text, rejectreason text)`,
		`INSERT INTO project VALUES (1, 'Andromeda', 1, 'p1', 0, 0), (2, 'Old', 3, 'p2', 0, 0)`,
		`INSERT INTO target VALUES (1, 1, 1, 0.712, 41.27, 'M31', '`+targetGUID+`', 0), (2, 2, 1, 5.5, -5, 'M42', 'g-m42', 0)`,
		`INSERT INTO exposuretemplate VALUES (1, 'Red', 0, 300), (2, 'Red Low', 0, 120), (3, 'H-a', -1, 600)`,
		`INSERT INTO exposureplan VALUES (1, 1, 1, 1, -1), (2, 1, 2, 1, -1), (3, 3, 1, 1, -1)`,
	)
	if withTable {
		exec(t, sched, Schema)
	}
	if err := appDB.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.CalibrationMaster{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	newMoon := time.Date(2025, 11, 20, 6, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	for i := range 20 {
		at := newMoon.Add(time.Duration(i) * 6 * time.Minute)
		sky := 506 + 0.1*300/0.763
		if err := appDB.Create(&app.Frame{Key: fmt.Sprintf("M31/LIGHT/r%d.xisf", i), Type: "LIGHT", Object: objectM31, Filter: filterRed, Exposure: f(300), Gain: f(0),
			Offset: f(50), SetTemp: f(-10), Camera: "ZWO ASI2600MM Pro", DateObs: &at, SkyADU: &sky, IndexedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	clean := "clean"
	for _, e := range []float64{300, 600} {
		for i := range 25 {
			if err := appDB.Create(&app.Frame{Key: fmt.Sprintf("DARK/%g_%d.xisf", e, i), Type: "DARK", Exposure: f(e), Gain: f(0), Offset: f(50),
				SetTemp: f(-5), CalCheck: &clean, IndexedAt: newMoon}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return sched, appDB
}

func fixedNow() time.Time { return time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC) }

func site(context.Context) (sky.Site, error) { return sky.Site{Latitude: 33, Longitude: -97}, nil }

func TestComputeFromRealShapes(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, false)
	s := &Service{App: appDB, Sched: sched, Site: site, Now: fixedNow, Pedestal: 506}
	res, err := s.Compute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows %+v, want one: M31 Red gain 0 dark (no moonlit subs, the H-a plan has no fixed gain, M42's project is closed)", res.Rows)
	}
	r := res.Rows[0]
	if r.TargetGUID != targetGUID || r.Filter != filterRed || r.Gain != 0 || r.Bucket != BucketDark || r.Object != objectM31 {
		t.Errorf("row %+v", r)
	}
	if r.Current != 300 || r.SkySubs != 20 || r.SkySource != SourceTarget || r.SkyRate < 0.0999 || r.SkyRate > 0.1001 {
		t.Errorf("current %v subs %d source %q sky %v", r.Current, r.SkySubs, r.SkySource, r.SkyRate)
	}
	if r.Exposure != 600 {
		t.Errorf("on a 0.1 e⁻/s sky with 300 and 600 s darks: %v s, want 600", r.Exposure)
	}
	if len(res.Site) != 1 || res.Site[0].Filter != filterRed || res.Site[0].SkySource != SourceSite || res.Site[0].Exposure != 600 {
		t.Errorf("site rows %+v", res.Site)
	}
	g0, g100 := 0, 100
	if len(res.SiteLengths("R", &g0)) != 1 || len(res.SiteLengths(filterRed, &g100)) != 0 || len(res.SiteLengths(filterRed, nil)) != 1 {
		t.Error("site lengths by filter and gain")
	}
	if got := res.DarkLengths["0"]; len(got) != 2 || got[0] != 300 || got[1] != 600 {
		t.Errorf("dark lengths %v", got)
	}
}

func TestPublish(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, true)
	s := &Service{App: appDB, Sched: sched, Site: site, Now: fixedNow, Pedestal: 506, Mode: ModeDryRun}
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	sched.Table(Table).Count(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}
	if st := s.Latest().Publish; st.Changed != 1 || st.Mode != ModeDryRun {
		t.Errorf("dry run status %+v", st)
	}
	exec(t, sched, `INSERT INTO ts_sub_length (target_guid, filter, gain, bucket, exposure, computed_at) VALUES ('gone', 'Red', 0, 'dark', 300, '2026-01-01')`)
	s.Mode = ModeOn
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []TableRow
	sched.Table(Table).Find(&rows)
	if len(rows) != 1 || rows[0].TargetGUID != targetGUID || rows[0].Exposure != 600 || rows[0].Bucket != "dark" || rows[0].Basis == nil {
		t.Fatalf("published %+v", rows)
	}
	if st := s.Latest().Publish; st.Changed != 1 || st.Deleted != 1 || st.PublishedAt == nil {
		t.Errorf("status %+v", st)
	}
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Latest().Publish; st.Skipped != SkipUnchanged {
		t.Errorf("second publish %+v, want unchanged", st)
	}
}

func TestPublishWithoutTable(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, false)
	s := &Service{App: appDB, Sched: sched, Site: site, Now: fixedNow, Pedestal: 506, Mode: ModeOn}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Latest().Publish; st.Skipped != SkipNoTable {
		t.Errorf("status %+v", st)
	}
	if s.Proposed(context.Background()) != nil {
		t.Error("proposals without new lengths allowed")
	}
}

func TestResultFor(t *testing.T) {
	t.Parallel()
	r := &Result{Rows: []Row{{TargetGUID: "a", Target: "IC 4604 Panel 1", Objects: []string{"Rho Ophiuchi"}}, {TargetGUID: "b", Target: objectM31}}}
	if got := r.For("", ""); len(got.Rows) != 2 {
		t.Errorf("no filter: %d rows", len(got.Rows))
	}
	if got := r.For("Rho Ophiuchi", ""); len(got.Rows) != 1 || got.Rows[0].TargetGUID != "a" {
		t.Errorf("by stacker object: %+v", got.Rows)
	}
	if got := r.For("", "b"); len(got.Rows) != 1 || got.Rows[0].Target != objectM31 {
		t.Errorf("by guid: %+v", got.Rows)
	}
	if got := r.For("nothing", ""); got.Rows == nil || len(got.Rows) != 0 {
		t.Errorf("no match: %+v", got.Rows)
	}
}

func TestOldSubsAreLeftOut(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, false)
	s := &Service{App: appDB, Sched: sched, Site: site, Pedestal: 506, Now: func() time.Time { return fixedNow().Add(SkyHistory) }}
	res, err := s.Compute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("subs older than %v still measured the sky: %+v", SkyHistory, res.Rows)
	}
}

func TestUnknownSiteKeepsThePublishedLengths(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, true)
	s := &Service{App: appDB, Sched: sched, Site: site, Now: fixedNow, Pedestal: 506, Mode: ModeOn}
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s.Site = func(context.Context) (sky.Site, error) { return sky.Site{}, errors.New("s3 down") }
	if _, err := s.Refresh(ctx); !errors.Is(err, ErrNoSite) {
		t.Fatalf("refresh with no site: %v, want ErrNoSite", err)
	}
	var n int64
	sched.Table(Table).Count(&n)
	if n != 1 || s.Latest() == nil || len(s.Latest().Rows) != 1 {
		t.Errorf("a failed site lookup changed the published rows (%d) or the served result", n)
	}
}

// A plan moved from its own 600 s template to the filter's 300 s managed
// template keeps imaging at 600 s in the estimates while its migration is
// open.
func TestCurrentKeepsTheOldTemplateLength(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t, false)
	exec(t, sched, `INSERT INTO exposuretemplate VALUES (4, 'Red', 0, 600), (5, 'Red', 0, 150)`,
		`UPDATE exposureplan SET "exposureTemplateId" = 5 WHERE "Id" = 1`)
	s := &Service{App: appDB, Sched: sched, Site: site, Now: fixedNow, Pedestal: 506}
	current := func() float64 {
		t.Helper()
		groups, err := s.plans(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g.guid == targetGUID && g.filter == filterRed {
				return g.current
			}
		}
		t.Fatal("no M31 Red group")
		return 0
	}
	if c := current(); c != 150 {
		t.Fatalf("current %v, want the template default 150 before any migration row", c)
	}
	exec(t, sched, `CREATE TABLE ts_template_migration (plan_id integer, profile_id text, filter text, old_template_id integer, new_template_id integer, migrated_at text, reverted_at text)`,
		`INSERT INTO ts_template_migration VALUES (1, 'p', 'Red', 4, 5, '2026-10-12T03:00:00Z', null)`)
	if c := current(); c != 600 {
		t.Errorf("current %v, want the old template's 600 s while the migration is open", c)
	}
	exec(t, sched, `UPDATE ts_template_migration SET reverted_at = '2026-10-13T03:00:00Z'`)
	if c := current(); c != 150 {
		t.Errorf("current %v after the revert, want the plan's template default", c)
	}
}
