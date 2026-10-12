package bodies_test

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPublishOnPostgres(t *testing.T) {
	t.Parallel()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("sched"), postgres.WithUsername("sched"), postgres.WithPassword("sched"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db,
		`create table project ("Id" integer NOT NULL, name text NOT NULL, state integer, guid text, PRIMARY KEY ("Id"))`,
		`create table target ("Id" integer NOT NULL, name text NOT NULL, active integer NOT NULL, ra double precision, "dec" double precision, projectid integer, guid text, PRIMARY KEY ("Id"))`,
		`insert into project values (1, 'Comets', 1, 'p1')`,
		`insert into target values (1, 'C/2025 R2 (SWAN)', 1, 5, 27, 1, '`+cometGUID+`')`,
		bodies.TargetSchema, bodies.Schema,
		`insert into ts_target_body (target_guid, designation) values ('`+cometGUID+`', '`+swan+`')`)
	if err := db.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	s := &bodies.Service{Sched: db, Mode: bodies.ModeOn, Now: fakeNow,
		Horizons: &horizons.Client{BaseURL: f.srv.URL, Cache: db},
		Site:     func(context.Context) (sky.Site, error) { return testSite(), nil }}
	res, err := s.Refresh(ctx)
	if err != nil || res.PublishedAt == nil || count(t, db, "") != rowsPerNight(fakeNow()) {
		t.Fatalf("first publish %+v %v", res.Status, err)
	}
	var first bodies.PositionRow
	if err := db.Table(bodies.PositionTable).Order("at").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if sp := bodies.Spans(testSite(), fakeNow(), 1)[0]; !first.At.Equal(sp.Start) || first.Night != night12 {
		t.Errorf("first row at %v night %s", first.At, first.Night)
	}
	again, err := s.Refresh(ctx)
	if err != nil || again.Skipped != bodies.SkipUnchanged || f.hits.Load() != bodies.Nights {
		t.Fatalf("second refresh %+v %v, %d requests", again.Status, err, f.hits.Load())
	}
}
