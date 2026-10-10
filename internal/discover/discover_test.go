package discover_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
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

func newService(t *testing.T) *discover.Service {
	t.Helper()
	sched := openDB(t, "sched")
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, state INTEGER, "isMosaic" INTEGER)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, ra REAL, dec REAL, projectid INTEGER)`,
		`INSERT INTO project VALUES (1, 'Garlic Nebula', 1, 0), (2, 'Cygnis Loop', 1, 1), (3, 'M13', 1, 0), (4, 'C/2025 R2', 1, 0)`,
		`INSERT INTO target VALUES (1, 'Garlic Nebula', 23.986944, 62.436667, 1), (2, 'Cygnis Loop Panel 1', 20.868, 31.537, 2),
			(3, 'Cygnis Loop Panel 2', 20.868, 29.760, 2), (4, 'M 13', 16.6949, 36.4613, 3),
			(5, 'C/2025 R2', 13.4, 10.2, 4)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	appDB := openDB(t, "app")
	if err := appDB.AutoMigrate(&app.Stack{}, &app.Frame{}, &app.ObjectXref{}); err != nil {
		t.Fatal(err)
	}
	ra, dec, night := 150.0, 20.0, time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	appDB.Create(&[]app.Stack{
		{Object: garlicName, Filter: hydrogen, EffectiveSeconds: 36000, Subs: 120},
		{Object: garlicName, Filter: "O-III", EffectiveSeconds: 18000, Subs: 60},
		{Object: "Cygnis Loop Panel 1", Filter: hydrogen, EffectiveSeconds: 7200, Subs: 24},
		{Object: leoTriplet, Filter: "L", EffectiveSeconds: 3600, Subs: 12},
	})
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
	if !g.HasPos || g.Hours[hydrogen] != 10 || g.Hours["O-III"] != 5 || g.Mosaic || g.StateName != "Active" {
		t.Errorf("garlic subject %+v", g)
	}
	if c := byKey[cygnisKey]; !c.Mosaic || len(c.Targets) != 2 || c.Radius < 0.8 {
		t.Errorf("cygnis subject %+v", c)
	}
	leo, ok := byKey["object:Leo Triplet"]
	if !ok || !leo.HasPos || leo.RA < 149.999 || leo.RA > 150.001 || leo.LastNight == nil {
		t.Errorf("stacker-only subject %+v", leo)
	}
	links, _ := s.Links(ctx, garlicKey)
	if len(links) == 0 || links[0].Object.ID != catalog.Key(garlicID) || links[0].Status != discover.StatusAuto {
		t.Fatalf("garlic links %+v", links)
	}
	cyg, _ := s.Links(ctx, cygnisKey)
	found := false
	for _, l := range cyg {
		if l.Object.Designation == "G074.0-08.5" {
			found = l.Status == discover.StatusSuggested && l.Method == discover.MethodSimilar
		}
	}
	if !found {
		t.Errorf("Cygnis Loop should be a suggested fuzzy match to the Cygnus Loop: %+v", cyg)
	}
	if comet := byKey[cometKey]; comet.NotCatalogue != catalog.ReasonComet || byKey[garlicKey].NotCatalogue != "" {
		t.Errorf("comet subject %+v", comet)
	}
	if cl, _ := s.Links(ctx, cometKey); slices.ContainsFunc(cl, func(l discover.Link) bool { return l.Status != discover.StatusInFrame }) {
		t.Errorf("comet links %+v", cl)
	}
	m13, _ := s.Links(ctx, "project:M13")
	if len(m13) == 0 || m13[0].Object.Designation != m13Name || m13[0].Method != discover.MethodDesignation {
		t.Errorf("M13 links %+v", m13)
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
	var cyg *discover.ReviewItem
	for i := range review {
		if review[i].Subject == cygnisKey && review[i].Object.Designation == "G074.0-08.5" {
			cyg = &review[i]
		}
	}
	if cyg == nil {
		t.Fatalf("no review item for the Cygnus Loop in %d items", len(review))
	}
	if slices.ContainsFunc(review, func(r discover.ReviewItem) bool { return r.Subject == cometKey }) {
		t.Error("a comet is in the review queue")
	}
	s.AppDB.Create(&app.ObjectXref{Subject: cygnisKey, ObjectID: cyg.Object.ID, Decision: app.XrefConfirmed})
	s.AppDB.Create(&app.ObjectXref{Subject: garlicKey, ObjectID: "M31", Decision: app.XrefConfirmed})
	s.Invalidate()
	links, _ := s.Links(ctx, cygnisKey)
	confirmed := false
	for _, l := range links {
		if l.Object.ID == cyg.Object.ID {
			confirmed = l.Status == discover.StatusConfirmed && l.Linked()
		}
	}
	if !confirmed {
		t.Errorf("confirmed decision not applied: %+v", links)
	}
	g, _ := s.Links(ctx, garlicKey)
	manual := false
	for _, l := range g {
		manual = manual || l.Object.ID == "M31" && l.Method == "manual"
	}
	if !manual {
		t.Error("a confirmed link outside the candidates was dropped")
	}
	decided, _ := s.Review(ctx, true)
	shown := false
	for _, d := range decided {
		shown = shown || d.Decided && d.Subject == cygnisKey && d.Status == discover.StatusConfirmed
	}
	if !shown {
		t.Errorf("the confirmed match is not in the decided review: %+v", decided)
	}
}

func TestCatalogues(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := context.Background()
	ov, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Night == nil || ov.Night.DarkHours < 8 || ov.SiteError != "" {
		t.Errorf("night %+v %s", ov.Night, ov.SiteError)
	}
	var messier discover.CatalogueSummary
	for _, c := range ov.Catalogues {
		if c.Key == "messier" {
			messier = c
		}
	}
	if messier.Total != 110 || messier.InProgress < 1 || messier.Done+messier.InProgress+messier.NotStarted != 110 || messier.UpTonight == 0 {
		t.Errorf("messier summary %+v", messier)
	}
	entries, err := s.Catalogue(ctx, "messier")
	if err != nil {
		t.Fatal(err)
	}
	m13 := entries[12]
	if m13.Label != m13Name || m13.Index != 13 || m13.Status != discover.CompletionInProgress || len(m13.Subjects) == 0 {
		t.Errorf("M 13 entry %+v", m13)
	}
	green, err := s.Catalogue(ctx, "green")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range green {
		if e.Object.ID == catalog.Key(garlicID) && (e.Status != discover.CompletionInProgress || e.Hours[hydrogen] != 10) {
			t.Errorf("garlic entry %+v", e)
		}
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
	if len(byPos) == 0 || byPos[0].Method != discover.MethodCoordinates {
		t.Errorf("coordinate resolve %+v", byPos)
	}
	for _, q := range []string{"23h59m12s +62d26m", "359.8 62.44", "RA 23:59:12 Dec +62:26", "23.9867h +62°26′"} {
		typed, err := s.Resolve(ctx, q, nil, 0)
		if err != nil || len(typed) == 0 || typed[0].Object.Designation != garlicID || typed[0].Method != discover.MethodCoordinates {
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
	if !o.Fits || o.Panels != 1 || o.Coverage < 0.3 || o.Progress["H"] != 12.5 || o.Summary == nil || o.Tonight == nil || len(o.Curve) == 0 {
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
