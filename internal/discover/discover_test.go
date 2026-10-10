package discover_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	garlicKey  = "project:Garlic Nebula"
	garlicID   = "G116.9+00.2"
	cygnisKey  = "project:Cygnis Loop"
	cometKey   = "project:C/2025 R2"
	garlicName = "Garlic Nebula"
	leoTriplet = "Leo Triplet"
	m13Name    = "M 13"
	hydrogen   = "H-a"
	testSiteLa = 31.5
)

func openDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%s?mode=memory&cache=shared", t.Name(), name)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func wcsCards(t *testing.T, ra, dec, arcsec float64, w, h int) string {
	t.Helper()
	d := arcsec / 3600
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	cards := []frameheader.Card{
		{Name: "CTYPE1", Value: "RA---TAN", Quoted: true}, {Name: "CTYPE2", Value: "DEC--TAN", Quoted: true},
		{Name: "CRVAL1", Value: f(ra)}, {Name: "CRVAL2", Value: f(dec)},
		{Name: "CRPIX1", Value: f(float64(w+1) / 2)}, {Name: "CRPIX2", Value: f(float64(h+1) / 2)},
		{Name: "CD1_1", Value: f(-d)}, {Name: "CD1_2", Value: "0"}, {Name: "CD2_1", Value: "0"}, {Name: "CD2_2", Value: f(d)},
	}
	b, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newService(t *testing.T) *discover.Service {
	t.Helper()
	sched := openDB(t, "sched")
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, "profileId" TEXT, name TEXT, description TEXT, state INTEGER, priority INTEGER, minimumtime INTEGER,
			minimumaltitude REAL, "maximumAltitude" REAL, "isMosaic" INTEGER, enablegrader INTEGER, guid TEXT)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, active INTEGER, ra REAL, dec REAL, rotation REAL, projectid INTEGER, guid TEXT)`,
		`CREATE TABLE exposureplan ("Id" INTEGER PRIMARY KEY, "profileId" TEXT, exposure REAL, desired INTEGER, acquired INTEGER, accepted INTEGER,
			targetid INTEGER, "exposureTemplateId" INTEGER, enabled INTEGER, guid TEXT)`,
		`CREATE TABLE exposuretemplate ("Id" INTEGER PRIMARY KEY, "profileId" TEXT, name TEXT, filtername TEXT, gain INTEGER, "offset" INTEGER, bin INTEGER,
			twilightlevel INTEGER, moonavoidanceenabled INTEGER, moonavoidanceseparation REAL, moonavoidancewidth INTEGER, maximumhumidity REAL,
			defaultexposure REAL, moonrelaxscale REAL, moondownenabled INTEGER, ditherevery INTEGER, guid TEXT)`,
		`CREATE TABLE ruleweight ("Id" INTEGER PRIMARY KEY, name TEXT, weight REAL, projectid INTEGER)`,
		`INSERT INTO project ("Id", "profileId", name, state, minimumaltitude, "isMosaic", enablegrader, guid) VALUES
			(1, 'p', 'Garlic Nebula', 1, 30, 0, 1, 'pg'), (2, 'p', 'Cygnis Loop', 1, 30, 1, 1, 'pc'), (3, 'p', 'M13', 1, 42, 0, 1, 'pm'),
			(4, 'p', 'C/2025 R2', 1, 30, 0, 1, 'pk'), (5, 'p', 'Andromeda Galaxy', 1, 30, 0, 1, 'pa'), (6, 'p', 'Pleiades', 0, 30, 0, 1, 'pp')`,
		`INSERT INTO target ("Id", name, active, ra, dec, rotation, projectid, guid) VALUES (1, 'Garlic Nebula', 1, 23.986944, 62.436667, 0, 1, 'tg'),
			(2, 'Cygnis Loop Panel 1', 1, 20.868, 31.537, 0, 2, 'tc1'), (3, 'Cygnis Loop Panel 2', 1, 20.868, 29.760, 0, 2, 'tc2'),
			(4, 'M 13', 1, 16.6949, 36.4613, 0, 3, 'tm'), (5, 'C/2025 R2', 1, 13.4, 10.2, 0, 4, 'tk'),
			(6, 'Andromeda Galaxy', 1, 23.986944, 62.436667, 0, 5, 'ta'), (7, 'Pleiades', 1, 3.7911, 24.1167, 0, 6, 'tp')`,
		`INSERT INTO exposuretemplate ("Id", "profileId", name, filtername, defaultexposure) VALUES (1, 'p', 'Ha', 'H-a', 600), (2, 'p', 'OIII', 'O-III', 600),
			(3, 'p', 'L', 'L', 300)`,
		`INSERT INTO exposureplan ("Id", "profileId", exposure, desired, acquired, accepted, targetid, "exposureTemplateId", enabled) VALUES
			(1, 'p', 600, 120, 130, 120, 1, 1, 1), (2, 'p', 600, 80, 60, 60, 1, 2, 1), (3, 'p', 300, 40, 50, 40, 4, 3, 1), (4, 'p', 300, 10, 0, 0, 4, 1, 0)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	appDB := openDB(t, "app")
	if err := appDB.AutoMigrate(&app.Stack{}, &app.Frame{}, &app.ObjectXref{}, &app.TargetReference{}, &app.GoalMeasurement{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	ra, dec, night := 170.06, 13.26, time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	appDB.Create(&[]app.Stack{
		{Object: garlicName, Filter: hydrogen, EffectiveSeconds: 36000, Subs: 120, Width: 6248, Height: 4176},
		{Object: garlicName, Filter: "O-III", EffectiveSeconds: 18000, Subs: 60, Width: 6248, Height: 4176},
		{Object: "Cygnis Loop Panel 1", Filter: hydrogen, EffectiveSeconds: 7200, Subs: 24},
		{Object: leoTriplet, Filter: "L", EffectiveSeconds: 3600, Subs: 12},
	})
	w := wcsCards(t, 359.804, 62.437, 1.915, 6248, 4176)
	appDB.Create(&app.TargetReference{Object: garlicName, FrameID: 1, ObjectKey: "ref", WCS: &w})
	appDB.Create(&app.Frame{Key: "a.fits", ETag: "e", Size: 1, LastModified: night, Type: "LIGHT", Object: leoTriplet, MountRA: &ra, MountDec: &dec, DateObs: &night})
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return &discover.Service{
		Catalog: ix, AppDB: appDB, SchedDB: sched,
		Site: func(context.Context) (sky.Site, error) { return sky.Site{Latitude: testSiteLa, Longitude: -99.4}, nil },
		Rig: discover.Rig{
			Frame: sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}, MinAltitude: 30, SkyBright: 21.4,
			Filters: discover.ParseFilterValues([]string{"L,R,G,B,H=3,O=3,S=3"}), Exposures: discover.ParseFilterValues([]string{"H=600,O=600,L=300"}),
		},
		Now: func() time.Time { return time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC) },
	}
}

func linkTo(links []discover.Link, designation string) *discover.Link {
	for i := range links {
		if links[i].Object.Designation == designation {
			return &links[i]
		}
	}
	return nil
}

func TestSubjectsAndLinks(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	subs, err := s.Subjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]discover.Subject{}
	for _, sb := range subs {
		byKey[sb.Key] = sb
	}
	g := byKey[garlicKey]
	if !g.HasPos || g.Hours[hydrogen] != 10 || g.Hours["O-III"] != 5 || g.Mosaic || g.StateName != "Active" || g.Footprint != discover.FieldFrames {
		t.Errorf("garlic subject %+v", g)
	}
	if comet := byKey[cometKey]; comet.NotCatalogue != catalog.ReasonComet || byKey[garlicKey].NotCatalogue != "" {
		t.Errorf("comet subject %+v", comet)
	}
	if c := byKey[cygnisKey]; !c.Mosaic || len(c.Targets) != 2 || c.Radius < 0.8 || c.Footprint != discover.BasisTarget {
		t.Errorf("cygnis subject %+v", c)
	}
	leo, ok := byKey["object:Leo Triplet"]
	if !ok || !leo.HasPos || leo.RA < 170.059 || leo.RA > 170.061 || leo.LastNight == nil || leo.Footprint != discover.FieldPointing {
		t.Errorf("stacker-only subject %+v", leo)
	}
}

func TestFootprintLinks(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	links, _ := s.Links(ctx, garlicKey)
	gl := linkTo(links, garlicID)
	if gl == nil || links[0].Object.ID != catalog.Key(garlicID) || gl.Status != discover.StatusImaged || gl.Basis != discover.BasisFrames ||
		gl.Coverage == nil || *gl.Coverage != 1 || !gl.CentreInside || gl.NameMatch != discover.MethodName || !strings.Contains(gl.Why, "100% of it covered") {
		t.Fatalf("garlic links %+v", links)
	}
	cyg, _ := s.Links(ctx, cygnisKey)
	if l := linkTo(cyg, "G074.0-08.5"); l == nil || l.Status != discover.StatusImaged || l.Basis != discover.BasisTarget ||
		!slices.Contains(l.Targets, "Cygnis Loop Panel 1") || l.Similarity == nil {
		t.Errorf("the Cygnus Loop should be imaged by the Cygnis Loop frames: %+v", l)
	}
	if cl, _ := s.Links(ctx, cometKey); slices.ContainsFunc(cl, func(l discover.Link) bool { return l.Status != discover.StatusPlanned }) {
		t.Errorf("comet links %+v", cl)
	}
	m13, _ := s.Links(ctx, "project:M13")
	if len(m13) == 0 || m13[0].Object.Designation != m13Name || m13[0].Status != discover.StatusPlanned || m13[0].NameMatch != discover.MethodDesignation {
		t.Errorf("M13 links %+v", m13)
	}
	for _, l := range append(links, m13...) {
		if l.Separation != nil && *l.Separation < 0 {
			t.Errorf("negative separation %+v", l)
		}
	}
}

func TestReviewAndDecisions(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	review, err := s.Review(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range review {
		t.Logf("%s -> %s %s", r.SubjectName, r.Object.Designation, r.Why)
	}
	if len(review) != 1 || review[0].Subject != "project:Andromeda Galaxy" || review[0].Object.Designation != m31ID ||
		review[0].Rule != discover.RuleNameOutside || !strings.Contains(review[0].Why, "none of it is in your planned frames") {
		t.Fatalf("review %+v", review)
	}
	far := review[0]
	s.AppDB.Create(&app.ObjectXref{Subject: far.Subject, ObjectID: far.Object.ID, Decision: app.XrefConfirmed})
	s.AppDB.Create(&app.ObjectXref{Subject: garlicKey, ObjectID: "M33", Decision: app.XrefConfirmed, DecidedAt: time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)})
	s.AppDB.Create(&app.ObjectXref{Subject: garlicKey, ObjectID: "LBN576", Decision: app.XrefRejected})
	s.Invalidate()
	links, _ := s.Links(ctx, far.Subject)
	if l := linkTo(links, m31ID); l == nil || l.Status != discover.StatusConfirmed || !l.Linked() {
		t.Errorf("confirmed decision not applied: %+v", links)
	}
	g, _ := s.Links(ctx, garlicKey)
	if l := linkTo(g, "M 33"); l == nil || l.Method != discover.MethodManual || l.Why != "Confirmed by you on 2026-10-01." || l.Separation != nil {
		t.Errorf("a confirmed link outside the candidates %+v", l)
	}
	if l := linkTo(g, "LBN 576"); l == nil || l.Status != discover.StatusRejected || l.Linked() {
		t.Errorf("a rejected footprint link %+v", l)
	}
	decided, _ := s.Review(ctx, true)
	if !slices.ContainsFunc(decided, func(d discover.ReviewItem) bool {
		return d.Decided && d.Subject == far.Subject && d.Status == discover.StatusConfirmed
	}) {
		t.Errorf("the confirmed match is not in the decided review: %+v", decided)
	}
}

func garlicEntry(entries []discover.CatalogueEntry) discover.CatalogueEntry {
	for _, e := range entries {
		if e.Object.ID == catalog.Key(garlicID) {
			return e
		}
	}
	return discover.CatalogueEntry{}
}

func TestCatalogues(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	ov, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Night == nil || ov.Night.DarkHours < 8 || ov.SiteError != "" || ov.Night.UpTonightHours != 1 || ov.Night.AltitudeSource == "" {
		t.Errorf("night %+v %s", ov.Night, ov.SiteError)
	}
	if ov.Backfill == nil || ov.Backfill.Total != 4 || ov.Backfill.Measured != 0 || ov.Backfill.State != goals.BackfillOff {
		t.Errorf("backfill %+v", ov.Backfill)
	}
	var messier discover.CatalogueSummary
	for _, c := range ov.Catalogues {
		if c.Key == "messier" {
			messier = c
		}
	}
	scheduled := 0
	for _, c := range ov.Catalogues {
		scheduled += c.Scheduled
	}
	if scheduled == 0 {
		t.Error("nothing counted as scheduled with nothing captured")
	}
	if messier.Total != 110 || messier.Done != 1 || messier.DoneCounts != 1 ||
		messier.Done+messier.InProgress+messier.Measuring+messier.NotStarted != 110 || messier.UpTonight == 0 {
		t.Errorf("messier summary %+v", messier)
	}
	entries, err := s.Catalogue(ctx, "messier")
	if err != nil {
		t.Fatal(err)
	}
	m13 := entries[12]
	if m13.Label != m13Name || m13.Index != 13 || m13.Status != discover.CompletionDone || m13.ByGoal ||
		!strings.Contains(m13.Judged, "100% complete: L by accepted subs against desired") || len(m13.Subjects) == 0 ||
		m13.Tonight == nil || m13.Tonight.MinAltitude != 42 || !strings.Contains(m13.Tonight.AltitudeSource, "M13") {
		t.Errorf("M 13 entry %+v %+v", m13, m13.Tonight)
	}
}

func TestObjectEntries(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	green, err := s.Catalogue(ctx, "green")
	if err != nil {
		t.Fatal(err)
	}
	if e := garlicEntry(green); e.Status != discover.CompletionInProgress || e.Hours[hydrogen] != 10 || e.Tally.Filters != 2 || e.Tally.Measured != 0 ||
		!strings.Contains(e.Judged, "Garlic Nebula 87% complete") || !strings.Contains(e.Judged, "by accepted subs against desired") {
		t.Errorf("garlic entry %+v", e)
	}
	if _, err := s.Catalogue(ctx, "nope"); err == nil {
		t.Error("unknown catalogue accepted")
	}
	d, err := s.Object(ctx, garlicID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Fit.Panels != 1 || d.Tonight == nil || len(d.Links) == 0 || d.Months[9] == 0 {
		t.Errorf("garlic detail %+v", d)
	}
	if _, err := s.Object(ctx, "nothing-here"); err == nil {
		t.Error("missing object found")
	}
}

func TestCompletionBuckets(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE ts_goal (target_guid TEXT, filter TEXT, kind INTEGER, snr_goal REAL, depth_goal REAL, plateau_stop INTEGER, region TEXT, updated_at TEXT)`,
		`INSERT INTO ts_goal VALUES ('tg', 'H-a', 0, 10, NULL, 0, NULL, NULL), ('tg', 'O-III', 0, 10, NULL, 0, NULL, NULL)`,
	} {
		if err := s.SchedDB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	green, _ := s.Catalogue(ctx, "green")
	if e := garlicEntry(green); e.Status != discover.CompletionMeasuring || !strings.Contains(e.Judged, "2 filters with a goal not measured yet") {
		t.Errorf("goals set but nothing measured %+v", e)
	}
	s.AppDB.Create(&[]app.GoalMeasurement{
		{Object: garlicName, Filter: hydrogen, MethodRevision: goals.MethodRevision, Subs: 120, SNR: 12, GainPerHourPct: 3, EffectiveHours: 10},
		{Object: garlicName, Filter: "O-III", MethodRevision: goals.MethodRevision, Subs: 60, SNR: 4, GainPerHourPct: 6, EffectiveHours: 5},
	})
	s.Invalidate()
	green, _ = s.Catalogue(ctx, "green")
	if e := garlicEntry(green); e.Status != discover.CompletionInProgress || e.Tally.Measured != 2 || !strings.Contains(e.Judged, "by its goal") {
		t.Errorf("one filter short of its goal %+v", e)
	}
	s.AppDB.Model(&app.GoalMeasurement{}).Where("filter = ?", "O-III").Update("snr", 11)
	s.Invalidate()
	green, _ = s.Catalogue(ctx, "green")
	if e := garlicEntry(green); e.Status != discover.CompletionDone || !e.ByGoal {
		t.Errorf("every filter at its goal %+v", e)
	}
	ov, _ := s.Overview(ctx)
	if ov.Backfill.Measured != 2 || ov.Backfill.Current != 2 {
		t.Errorf("backfill %+v", ov.Backfill)
	}
}

func TestFinder(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	res, err := s.Finder(ctx, discover.FinderQuery{Fits: []string{sky.FitOne}, Types: []string{discover.GroupEmission}, MinFill: 0.3, Months: []int{10}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total == 0 || len(res.Rows) == 0 || res.SiteError != "" {
		t.Fatalf("finder %+v", res)
	}
	for _, r := range res.Rows {
		if r.Fit.Category != sky.FitOne || r.Group != discover.GroupEmission || r.Fit.Fill < 0.3 || r.Imaged {
			t.Errorf("row breaks the filters: %+v", r)
		}
	}
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i].Score > res.Rows[i-1].Score {
			t.Errorf("rows not sorted by score at %d", i)
		}
	}
	shown, _ := s.Finder(ctx, discover.FinderQuery{Imaged: true, Types: []string{discover.GroupRemnant}, Limit: 500, Sort: discover.SortNow})
	imaged := false
	for _, r := range shown.Rows {
		imaged = imaged || r.Object.ID == catalog.Key(garlicID) && r.Imaged
	}
	if !imaged {
		t.Error("the Garlic Nebula is not marked imaged")
	}
	if fill, _ := s.Finder(ctx, discover.FinderQuery{Sort: discover.SortFill, Limit: 5}); len(fill.Rows) != 5 {
		t.Errorf("fill sort returned %d", len(fill.Rows))
	}
}

func TestNoSite(t *testing.T) {
	t.Parallel()
	s := newService(t)
	s.Site = nil
	ov, err := s.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ov.SiteError == "" || ov.Night != nil {
		t.Error("no site error without a site")
	}
	f, err := s.Finder(context.Background(), discover.FinderQuery{})
	if err != nil || f.SiteError == "" {
		t.Errorf("finder without a site: %v %+v", err, f.SiteError)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	links, err := s.Resolve(ctx, garlicName, &discover.Position{RA: 359.80, Dec: 62.44}, 0)
	if err != nil || len(links) == 0 || links[0].Object.Designation != garlicID {
		t.Errorf("resolve %v %+v", err, links)
	}
	if _, err := s.Resolve(ctx, "", nil, 0); err == nil {
		t.Error("empty resolve accepted")
	}
	byPos, _ := s.Resolve(ctx, "", &discover.Position{RA: 359.80, Dec: 62.44}, 0)
	if len(byPos) == 0 || byPos[0].Object.Designation != garlicID || byPos[0].Method != discover.MethodFootprint || byPos[0].Status != discover.StatusPlanned {
		t.Errorf("coordinate resolve %+v", byPos)
	}
	for _, q := range []string{"23h59m12s +62d26m", "359.8 62.44", "RA 23:59:12 Dec +62:26", "23.9867h +62°26′"} {
		typed, err := s.Resolve(ctx, q, nil, 0)
		if err != nil || len(typed) == 0 || typed[0].Object.Designation != garlicID || typed[0].Method != discover.MethodFootprint {
			t.Errorf("coordinates typed as a name %q: %v %+v", q, err, typed)
		}
	}
}

type fakeCollabs struct{ st starfront.State }

func (f fakeCollabs) State() starfront.State { return f.st }

func ptr[T any](v T) *T { return &v }

func TestCollabs(t *testing.T) {
	t.Parallel()
	s := newService(t)
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	open := starfront.Project{ID: "a", Name: "M31 test", Coordinator: "c", Status: starfront.StatusOpen, Created: float64(now.Unix()),
		Payload: starfront.Payload{Kind: "single", Region: starfront.Region{RA: 10.675, Dec: 41.267, Width: 5.37, Height: 3.58, Rotation: 275.8},
			Requirements: starfront.Requirements{MinFocalLength: ptr(100.0), MaxFocalLength: ptr(3000.0), Filters: map[string]*float64{"H": nil, "O": nil, "L": nil}},
			Goals:        map[string]float64{"H": 500}}}
	closed := starfront.Project{ID: "b", Name: "m42 closeup", Status: "closed", Created: float64(now.Add(-48 * time.Hour).Unix()),
		Payload: starfront.Payload{Kind: "mosaic", Region: starfront.Region{RA: 83.8, Dec: -5.4, Width: 1.66, Height: 1.48},
			Requirements: starfront.Requirements{MinFocalLength: ptr(1500.0), MaxFocalLength: ptr(3000.0), AcceptColour: ptr(true), Filters: map[string]*float64{"H": ptr(1.0)}}}}
	old := closed
	old.ID, old.Created = "c", float64(now.Add(-90*24*time.Hour).Unix())
	src := fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{open, closed, old},
		Summaries: map[string]starfront.Summary{"a": {Joined: 3, Hours: map[string]float64{"H": 12.5}}}}}
	v, err := s.Collabs(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 || len(v.Closed) != 1 || v.ClosedAll != 2 {
		t.Fatalf("open %d closed %d of %d", len(v.Open), len(v.Closed), v.ClosedAll)
	}
	o := v.Open[0]
	if !o.Fits || o.Panels != 1 || o.Coverage == nil || *o.Coverage < 0.3 || o.Progress["H"] != 12.5 || o.Summary == nil || o.Tonight == nil || len(o.Curve) == 0 {
		t.Errorf("open collab %+v", o)
	}
	c := v.Closed[0]
	if c.Fits {
		t.Errorf("a 1500 mm collab fits a 405 mm rig: %+v", c.Criteria)
	}
	failed := map[string]bool{}
	for _, cr := range c.Criteria {
		if cr.Result == discover.CriterionFail {
			failed[cr.Name] = true
		}
	}
	if !failed["Focal length"] || !failed["Filters"] {
		t.Errorf("closed criteria %+v", c.Criteria)
	}
	empty, err := s.Collabs(context.Background(), nil)
	if err != nil || empty.Enabled || len(empty.Open) != 0 {
		t.Errorf("no source: %+v %v", empty, err)
	}
	if discover.CanonicalFilter("H-a") != "H" || discover.CanonicalFilter("O-III") != "O" || discover.CanonicalFilter("Lum") != "L" {
		t.Error("filter canonicalisation")
	}
}

func TestCollabFilters(t *testing.T) {
	t.Parallel()
	s := newService(t)
	closed := starfront.Project{Name: "closed", Status: "done", Created: float64(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).Unix()),
		Payload: starfront.Payload{Kind: "mosaic", Region: starfront.Region{RA: 83.8, Dec: -5.4, Width: 1.66, Height: 1.48}}}
	ha := closed
	ha.ID, ha.Payload.Requirements = "d", starfront.Requirements{Filters: map[string]*float64{"Ha": nil}, MinFramesPerVisit: ptr(10), RequireCalibrated: true,
		MinAltitude: ptr(20.0), MaxMoonIllumination: ptr(0.5), MinMoonSeparation: ptr(30.0)}
	ha.Payload.Goals = map[string]float64{"Ha": 15}
	osc := closed
	osc.ID, osc.Payload.Requirements = "e", starfront.Requirements{Filters: map[string]*float64{"OSC": nil}}
	osc.Payload.Goals = map[string]float64{"OSC": 35}
	nm := closed
	nm.ID, nm.Payload.Requirements = "f", starfront.Requirements{Filters: map[string]*float64{"L": ptr(100.0)}}
	v, err := s.Collabs(context.Background(), fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{ha, osc, nm}}})
	if err != nil || len(v.Closed) != 3 {
		t.Fatalf("%v %d", err, len(v.Closed))
	}
	byID := map[string]discover.Collab{}
	for _, c := range v.Closed {
		byID[c.ID] = c
	}
	filterRow := func(c discover.Collab) discover.Criterion {
		for _, cr := range c.Criteria {
			if cr.Name == "Filters" {
				return cr
			}
		}
		return discover.Criterion{}
	}
	for _, cr := range byID["d"].Criteria {
		if strings.Contains(cr.You, "not checked") || strings.Contains(cr.You, "can meet") || strings.Contains(cr.You, "per cell") || strings.Contains(cr.You, "dealt") {
			t.Errorf("criterion not computed: %+v", cr)
		}
	}
	if f := filterRow(byID["d"]); f.Result != discover.CriterionPass || f.Rule != "H, any bandpass" || byID["d"].Goals["H"] != 15 {
		t.Errorf("Ha not canonicalised: %+v goals %+v", f, byID["d"].Goals)
	}
	if f := filterRow(byID["e"]); f.Result != discover.CriterionFail || !strings.Contains(f.You, "colour camera") || byID["e"].Verdict != discover.VerdictNoFit {
		t.Errorf("OSC on a mono rig: %+v", f)
	}
	if f := filterRow(byID["f"]); f.Result != discover.CriterionOpen || !strings.Contains(f.You, "bandpass not set") || byID["f"].Verdict != discover.VerdictUnchecked || byID["f"].Unchecked == 0 {
		t.Errorf("unknown bandpass passed silently: %+v %s", f, byID["f"].Verdict)
	}
}

func TestUnknownRigIsNotInvented(t *testing.T) {
	t.Parallel()
	s := newService(t)
	s.Measure = func(context.Context) rigsource.Rig { return rigsource.Empty() }
	ctx := context.Background()
	res, err := s.Finder(ctx, discover.FinderQuery{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.RigError == nil || len(res.Rows) != 0 || res.Frame.Scale != 0 || res.Rig.FocalLength != nil || res.Rig.Basis.Source != rigsource.SourceNone {
		t.Fatalf("finder with no rig %+v", res)
	}
	d, err := s.Object(ctx, "M31")
	if err != nil || d.Fit != nil {
		t.Fatalf("object fit without a rig: %+v %v", d.Fit, err)
	}
	src := fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{{ID: "a", Status: starfront.StatusOpen,
		Payload: starfront.Payload{Kind: "single", Region: starfront.Region{RA: 10.6, Dec: 41.2, Width: 2, Height: 1},
			Requirements: starfront.Requirements{MinFocalLength: ptr(100.0), MaxHFR: ptr(3.0)}}}}}}
	v, err := s.Collabs(ctx, src)
	if err != nil || len(v.Open) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	for _, c := range v.Open[0].Criteria {
		if c.Result == discover.CriterionFail {
			t.Errorf("an unmeasured rig failed %+v", c)
		}
	}
	if v.Open[0].Coverage != nil {
		t.Errorf("coverage without a rig")
	}

	fl, px, w, h, rms := 405.0, 3.76, 6248, 4176, 0.6
	s.Measure = func(context.Context) rigsource.Rig {
		r := rigsource.Empty()
		r.FocalLength, r.PixelSize, r.WidthPx, r.HeightPx, r.TypicalGuideRMS = &fl, &px, &w, &h, &rms
		r.Filters = []string{"H"}
		return r
	}
	res, err = s.Finder(ctx, discover.FinderQuery{Limit: 5})
	if err != nil || res.RigError != nil || len(res.Rows) == 0 || res.Frame.Scale == 0 {
		t.Fatalf("finder with a measured rig %+v %v", res.RigError, err)
	}
}

func TestBrightnessIsLabelledNotScored(t *testing.T) {
	t.Parallel()
	s := newService(t)
	s.Sky = func(context.Context) skybright.Value {
		r := "no L master with a Gaia zero point has been measured yet"
		return skybright.Value{Basis: skybright.Basis{Source: skybright.SourceNone, PerNight: []skybright.Night{}, Reason: &r}}
	}
	res, err := s.Finder(context.Background(), discover.FinderQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mag != nil || res.Basis.Reason == nil {
		t.Fatalf("sky %+v", res.Value)
	}
	kinds := map[string]int{}
	for _, r := range res.Rows {
		kinds[r.Brightness.Kind]++
		if r.Brightness.Kind == discover.BrightComputed && (r.Brightness.Value == nil || !strings.Contains(r.Brightness.Text, "computed")) {
			t.Errorf("computed brightness not marked: %+v", r.Brightness)
		}
		for _, term := range r.Terms {
			if term.Key == "brightness" {
				t.Errorf("brightness in the sort key: %+v", r.Terms)
			}
		}
	}
	if kinds[discover.BrightNone]+kinds[discover.BrightComputed]+kinds[discover.BrightCatalogued]+kinds[discover.BrightClass] != len(res.Rows) {
		t.Errorf("brightness kinds %+v", kinds)
	}
	s.Sky = func(context.Context) skybright.Value { return skybright.Configured(21.2) }
	v, err := s.Collabs(context.Background(), nil)
	if err != nil || v.Mag == nil || *v.Mag != 21.2 || v.Basis.Source != skybright.SourceConfig {
		t.Fatalf("collabs sky %+v %v", v.Value, err)
	}
}

func TestFinderUsesTheHAlphaMap(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	res, err := s.Finder(ctx, discover.FinderQuery{Types: []string{discover.GroupEmission}, Limit: 20})
	if err != nil || len(res.Rows) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, r := range res.Rows {
		if r.HAlpha != nil {
			t.Fatalf("H-α without a map: %+v", r)
		}
		for _, term := range r.Terms {
			if term.Key == discover.TermHAlpha && term.Value != nil {
				t.Fatalf("H-α term scored without a map: %+v", term)
			}
		}
	}
	if res.HAlphaMap.State != halpha.StateOff {
		t.Errorf("map state %+v", res.HAlphaMap)
	}
	m := &halpha.Map{W: 360, H: 180, CRPix1: 180, CRPix2: 90, CDelt1: -1, CDelt2: 1, CRVal1: 180, Data: make([]float32, 360*180), FetchedAt: time.Now()}
	for i := range m.Data {
		m.Data[i] = 50
	}
	s.HAlpha = func() (*halpha.Map, halpha.Status) { return m, halpha.Status{State: halpha.StateReady} }
	withMap, err := s.Finder(ctx, discover.FinderQuery{Types: []string{discover.GroupEmission}, Limit: 20})
	if err != nil || len(withMap.Rows) == 0 {
		t.Fatalf("%+v %v", withMap, err)
	}
	for _, r := range withMap.Rows {
		if r.HAlpha == nil || r.HAlpha.Rayleigh != 50 {
			t.Fatalf("row %+v", r)
		}
	}
	if d, err := s.Object(ctx, "M42"); err != nil || d.HAlpha == nil || d.HAlpha.Rayleigh != 50 {
		t.Errorf("object H-α %+v %v", d.HAlpha, err)
	}
	for _, r := range withMap.Rows {
		var sum float64
		for _, term := range r.Terms {
			sum += term.Points
		}
		if math.Abs(sum-r.Score) > 0.002 {
			t.Errorf("score %v is not the sum of its terms %+v", r.Score, r.Terms)
		}
	}
	if withMap.Rows[0].Score <= res.Rows[0].Score {
		t.Errorf("H-α did not raise the emission score: %v <= %v", withMap.Rows[0].Score, res.Rows[0].Score)
	}
}
