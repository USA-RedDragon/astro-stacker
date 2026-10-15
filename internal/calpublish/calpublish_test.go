package calpublish_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calpublish"
	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	m31   = "M 31"
	night = "2026-10-09"
)

func fp(v float64) *float64 { return &v }

type fixture struct {
	db  *gorm.DB
	now time.Time
	n   int
}

func (fx *fixture) add(t *testing.T, f app.Frame, count int, start time.Time) {
	t.Helper()
	for i := range count {
		fx.n++
		f.ID = 0
		f.Key = fmt.Sprintf("f-%d", fx.n)
		f.ETag, f.LastModified = "e", fx.now
		at := start.Add(time.Duration(i) * time.Minute)
		f.DateObs = &at
		if f.Night == nil || f.Type != "LIGHT" {
			n := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
			f.Night = &n
		}
		if err := fx.db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func seed(t *testing.T, db *gorm.DB) *fixture {
	t.Helper()
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{db: db, now: time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)}
	lightNight := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for _, filter := range []string{"Red", "Green"} {
		fx.add(t, app.Frame{Type: "LIGHT", Object: m31, Filter: filter, Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(-10),
			BinX: fp(1), Night: &lightNight}, 3, time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC))
	}
	return fx
}

func publishers(t *testing.T, db, sched *gorm.DB, fx *fixture, mode string) (*calpublish.BiasPublisher, *calpublish.FlatPublisher) {
	t.Helper()
	for _, s := range []string{calpublish.BiasSchema, calpublish.FlatSchema} {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	bias := calpublish.NewBias(db, sched, mode)
	flat := calpublish.NewFlat(db, sched, mode)
	bias.Now = func() time.Time { return fx.now }
	flat.Now = bias.Now
	return bias, flat
}

func checkPublish(t *testing.T, db, sched *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	fx := seed(t, db)
	bias, flat := publishers(t, db, sched, fx, darkneed.PublishOn)
	id := 12
	req, err := calreq.Open(ctx, db, calreq.Payload{Type: app.CalibrationFlats, Object: m31, TargetID: &id, Night: night}, "r1", "jacob", fx.now)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := bias.Publish(ctx); err != nil || s.Changed != 1 || s.PublishedAt == nil {
		t.Fatalf("bias publish %+v %v", s, err)
	}
	var rows []calpublish.BiasRow
	if err := sched.Table(calpublish.BiasTable).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ComboKey != "bias|100|50|1" || rows[0].Priority != 1 || rows[0].FramesNeeded != 30 || rows[0].CameraOffset != 50 {
		t.Fatalf("bias rows %+v", rows)
	}
	if s, err := flat.Publish(ctx); err != nil || s.Changed != 1 || s.Rows != 1 {
		t.Fatalf("flat publish %+v %v", s, err)
	}
	var flats []calpublish.FlatRow
	if err := sched.Table(calpublish.FlatTable).Find(&flats).Error; err != nil {
		t.Fatal(err)
	}
	if len(flats) != 1 || flats[0].RequestID != req.ID || flats[0].TargetID != 12 || flats[0].TargetName != m31 || flats[0].Night != night ||
		flats[0].Filters == nil || *flats[0].Filters != "Green,Red" || !flats[0].RequestedAt.Equal(fx.now.Truncate(time.Microsecond)) {
		t.Fatalf("flat rows %+v", flats)
	}
	checkGone(t, db, sched, bias, flat, fx)
}

func checkGone(t *testing.T, db, sched *gorm.DB, bias *calpublish.BiasPublisher, flat *calpublish.FlatPublisher, fx *fixture) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []interface {
		Publish(context.Context) (calpublish.Status, error)
	}{bias, flat} {
		if s, err := p.Publish(ctx); err != nil || s.Skipped != darkneed.SkipUnchanged {
			t.Fatalf("second publish %+v %v", s, err)
		}
	}
	fx.add(t, app.Frame{Type: "BIAS", Gain: fp(100), Offset: fp(50), BinX: fp(1)}, 30, fx.now.Add(-time.Hour))
	if s, err := bias.Publish(ctx); err != nil || s.Deleted != 1 || s.Rows != 0 {
		t.Fatalf("bias publish after a full set %+v %v", s, err)
	}
	if _, err := calreq.Cancel(ctx, db, calreq.Payload{Type: app.CalibrationFlats, Object: m31, Night: night}, "jacob", fx.now); err != nil {
		t.Fatal(err)
	}
	if s, err := flat.Publish(ctx); err != nil || s.Deleted != 1 || s.Rows != 0 {
		t.Fatalf("flat publish after cancel %+v %v", s, err)
	}
	var left int64
	sched.Table(calpublish.FlatTable).Count(&left)
	if left != 0 {
		t.Errorf("%d flat rows left", left)
	}
}

func sqliteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := db.DB(); err == nil {
		raw.SetMaxOpenConns(1)
	}
	return db
}

func TestPublishOnSQLite(t *testing.T) {
	t.Parallel()
	checkPublish(t, sqliteDB(t), sqliteDB(t))
}

func TestPublishOnPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "stacker", "stacker")
	checkPublish(t, db, db)
}

func TestDryRunWritesNothingAndMissingTablesSkip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := sqliteDB(t)
	fx := seed(t, db)
	empty := sqliteDB(t)
	if s, err := calpublish.NewBias(db, empty, darkneed.PublishOn).Publish(ctx); err != nil || s.Skipped != "no ts_bias_need table" {
		t.Errorf("no table %+v %v", s, err)
	}
	if s, err := calpublish.NewFlat(db, nil, darkneed.PublishOn).Publish(ctx); err != nil || s.Skipped != darkneed.SkipNoScheduler {
		t.Errorf("no scheduler %+v %v", s, err)
	}
	if s, err := calpublish.NewFlat(db, empty, darkneed.PublishOff).Publish(ctx); err != nil || s.Skipped != darkneed.SkipOff {
		t.Errorf("off %+v %v", s, err)
	}
	var nilPub *calpublish.BiasPublisher
	if s := nilPub.Status(); s.Mode != darkneed.PublishOff {
		t.Errorf("nil publisher %+v", s)
	}
	sched := sqliteDB(t)
	bias, _ := publishers(t, db, sched, fx, darkneed.PublishDryRun)
	if s, err := bias.Publish(ctx); err != nil || s.Changed != 1 || s.PublishedAt != nil {
		t.Errorf("dry run %+v %v", s, err)
	}
	var n int64
	sched.Table(calpublish.BiasTable).Count(&n)
	if n != 0 {
		t.Errorf("a dry run wrote %d rows", n)
	}
	if s := bias.Status(); s.Mode != darkneed.PublishDryRun || s.Rows != 1 {
		t.Errorf("status %+v", s)
	}
}
