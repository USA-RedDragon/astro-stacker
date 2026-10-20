package binaries

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"gorm.io/gorm"
)

type noCommands struct{}

func (noCommands) SubmitOnce(context.Context, string, schedcmd.Kind, json.RawMessage, string) (schedcmd.Record, error) {
	return schedcmd.Record{ID: "cmd"}, nil
}

func (noCommands) Status(context.Context, string) (schedcmd.Record, error) {
	return schedcmd.Record{}, nil
}

func (noCommands) Undo(context.Context, string, string) (schedcmd.Record, error) {
	return schedcmd.Record{}, nil
}

func TestSeasonCentresOnOpposition(t *testing.T) {
	t.Parallel()
	s := SeasonOf(0, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC))
	if s.Opposition.Month() != time.September || s.Opposition.Year() != 2026 || math.Abs(float64(s.Opposition.Day()-21)) > 3 {
		t.Errorf("a star at 0h is opposite the Sun on %s, want about Sep 21", s.Opposition)
	}
	if again := SeasonOf(0, time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC)); absDur(again.Opposition.Sub(s.Opposition)) > 48*time.Hour {
		t.Errorf("February 2027 is in the season of %s, not %s", again.Opposition, s.Opposition)
	}
	if next := SeasonOf(0, time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)); next.Opposition.Year() != 2027 {
		t.Errorf("April 2027 is in the season of %s", next.Opposition)
	}
	ra, _ := sky.SunPosition(s.Start)
	if d := math.Abs(math.Mod(ra-0+540, 360) - 180); d > 5 {
		t.Errorf("the season starts with the Sun %.1f degrees from the star, want behind it", 180-d)
	}
}

func TestQuotaGrowsWithFlags(t *testing.T) {
	t.Parallel()
	one := &period.Ephemeris{Period: 5, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{{Width: 0.02, Shape: 1, Depth: map[string]float64{keyZTFr: 0.3}}}}
	rep := &vetting.Report{Ephemeris: one, Groups: []vetting.Group{{Checks: []check.Check{check.New(check.Half, check.Pass, "fine"), check.New(check.Double, check.Pass, "twice the period does not fit")}}}}
	st := Star{Report: rep, Facts: &Facts{Ephemeris: one, Gaia: &GaiaFacts{RUWE: 1}}}
	if q := quota(st); q[KindPrimary] != 3 || q[KindSecondary] != 0 {
		t.Errorf("one dip, settled: %v", q)
	}
	rep.Groups[0].Checks[1] = check.New(check.Double, check.NotRun, period.SameDepthOddEven+".")
	if q := quota(st); q[KindPrimary] != 5 {
		t.Errorf("period could be twice this: %v", q)
	}
	two := twoDips(0.4, 0.2)
	st = Star{Report: &vetting.Report{Ephemeris: two, Groups: []vetting.Group{{Checks: []check.Check{check.New(check.Half, check.Pass, "fine")}}}},
		Facts: &Facts{Ephemeris: two, Gaia: &GaiaFacts{RUWE: 1}}}
	if q := quota(st); q[KindPrimary] != 2 || q[KindSecondary] != 1 {
		t.Errorf("two dips: %v", q)
	}
}

// seasonalDB stores one confirmed binary seen well at night from site
// around now.
func seasonalDB(t *testing.T, site sky.Site, now time.Time) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "seasonal")
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.BinaryFacts{}, &app.BinaryBook{}, &app.ShareImage{},
		&app.EclipseRequest{}, &app.EclipseTiming{}, &app.Frame{}); err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(site.Longitude / 15 * float64(time.Hour)))
	ra, dec := sky.LST(midnight, site.Longitude), site.Latitude
	src := app.DiscoverySource{Object: "Season field", Kind: app.DiscoveryKindChange, RA: ra, Dec: dec, Night: now.AddDate(-1, 0, 0), Vetting: app.VetReal}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	e := &period.Ephemeris{Period: 1.37, PeriodErr: 1e-6, T0: 2460000.3, T0Err: 0.0004, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.025, Shape: 1, Depth: map[string]float64{keyZTFr: 0.5}},
			{Phase: 0.5, Width: 0.025, Shape: 1, Depth: map[string]float64{keyZTFr: 0.3}},
		}}
	ran := now.Add(-48 * time.Hour).Truncate(time.Microsecond)
	rep := vetting.Report{SourceID: src.ID, RanAt: ran, Star: "Gaia DR3 7", Ephemeris: e, Suggestion: ebSuggestion,
		Groups: []vetting.Group{{Checks: []check.Check{check.New(check.Half, check.Pass, "fine"), check.New(check.Echo, check.Pass, "stands out")}}}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DiscoveryVet{SourceID: src.ID, State: "done", Report: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	f, err := json.Marshal(Facts{Ephemeris: e, Gaia: &GaiaFacts{ID: 7, RA: ra, Dec: dec, G: 14, RUWE: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.BinaryFacts{SourceID: src.ID, Version: FactsVersion, VetRanAt: ran, State: FactsReady, Facts: string(f)}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAutoRequestsFollowTheSeason(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db := seasonalDB(t, site, now)
	svc := &Service{DB: db, Commands: noCommands{}, Now: func() time.Time { return now },
		Site:     func(context.Context) (sky.Site, error) { return site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	ctx := context.Background()
	run := func() []app.EclipseRequest {
		t.Helper()
		stars, err := svc.Binaries(ctx)
		if err != nil || len(stars) != 1 {
			t.Fatalf("stars %d %v", len(stars), err)
		}
		if err := svc.autoRequests(ctx, stars); err != nil {
			t.Fatal(err)
		}
		var reqs []app.EclipseRequest
		if err := db.Order("id").Find(&reqs).Error; err != nil {
			t.Fatal(err)
		}
		return reqs
	}
	reqs := run()
	if len(reqs) != 1 || !reqs[0].Auto || reqs[0].State != app.EclipseFailed {
		t.Fatalf("first look: %+v", reqs)
	}
	r := reqs[0]
	if r.Mid.Before(now.Add(autoLead)) || r.Mid.After(now.Add(autoAhead)) {
		t.Errorf("asked for %s, outside the next two weeks", r.Mid)
	}
	stars, err := svc.Binaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ra, dec := stars[0].Facts.Gaia.Now(r.Mid)
	if ok, why := Seen(site, ra, dec, r.Mid); !ok {
		t.Errorf("asked for an eclipse the site cannot see: %s", why)
	}
	reqs = run()
	if len(reqs) != 2 || reqs[1].Mid.Equal(reqs[0].Mid) {
		t.Fatalf("second look: %+v", reqs)
	}
	reqs = run()
	if len(reqs) != 2 {
		t.Errorf("asked again after two failures this season: %d requests", len(reqs))
	}
	rec, err := svc.Timings(ctx, stars[0].Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Season == nil || rec.Season.Stopped == "" || len(rec.Season.Requests) != 2 {
		t.Errorf("season view %+v", rec.Season)
	}
}

func TestAutoRequestsStopAfterCancel(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db := seasonalDB(t, site, now)
	var src app.DiscoverySource
	if err := db.First(&src).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.EclipseRequest{SourceID: src.ID, Kind: KindPrimary, Mid: now.Add(-72 * time.Hour), State: app.EclipseCancel, Auto: true}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: db, Commands: noCommands{}, Now: func() time.Time { return now },
		Site:     func(context.Context) (sky.Site, error) { return site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	ctx := context.Background()
	stars, err := svc.Binaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.autoRequests(ctx, stars); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&app.EclipseRequest{}).Count(&n).Error; err != nil || n != 1 {
		t.Errorf("asked after the user cancelled: %d requests, %v", n, err)
	}
}

func TestAutoRequestsSkipTimedSeasons(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db := seasonalDB(t, site, now)
	var src app.DiscoverySource
	if err := db.First(&src).Error; err != nil {
		t.Fatal(err)
	}
	for i, k := range []string{KindPrimary, KindPrimary, KindSecondary} {
		if err := db.Create(&app.EclipseTiming{SourceID: src.ID, Kind: k, Predicted: now.AddDate(0, 0, -10*(i+1)), State: app.TimingGood, BJD: 2461000, Err: 0.0005}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{DB: db, Commands: noCommands{}, Now: func() time.Time { return now },
		Site:     func(context.Context) (sky.Site, error) { return site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	ctx := context.Background()
	stars, err := svc.Binaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.autoRequests(ctx, stars); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&app.EclipseRequest{}).Count(&n).Error; err != nil || n != 0 {
		t.Errorf("asked with the season's timings done: %d requests, %v", n, err)
	}
}
