package coverage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type reqFixture struct {
	db  *gorm.DB
	now time.Time
	n   int
}

func (fx *reqFixture) add(t *testing.T, fr app.Frame, count int, start time.Time) {
	t.Helper()
	for i := range count {
		fx.n++
		fr.ID = 0
		fr.Key = fmt.Sprintf("frame-%d", fx.n)
		fr.ETag, fr.LastModified = "e", fx.now
		at := start.Add(time.Duration(i) * time.Minute)
		fr.DateObs = &at
		n := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		if fr.Type == typeLight {
			n = *day(requestNight)
		}
		fr.Night = &n
		if err := fx.db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
	}
}

const (
	requestNight = "2026-10-09"
	objM33       = "M 33"
	objNGC7000   = "NGC 7000"
	typeBias     = "BIAS"
)

func newReqFixture(t *testing.T) *reqFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := db.DB(); err == nil {
		raw.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	fx := &reqFixture{db: db, now: time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)}
	lightAt := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	fx.add(t, app.Frame{Type: typeLight, Object: objM31, Filter: filterRed, Exposure: f(300), Gain: f(100), Offset: f(50), SetTemp: f(-10), BinX: f(1)}, 4, lightAt)
	fx.add(t, app.Frame{Type: typeLight, Object: objM31, Filter: filterRed, Exposure: f(300), Gain: f(100), Offset: f(50), SetTemp: f(-10.5), BinX: f(1)}, 2, lightAt)
	fx.add(t, app.Frame{Type: typeLight, Object: objNGC7000, Filter: filterRed, Exposure: f(300), Gain: f(100), Offset: f(50), SetTemp: f(-20), BinX: f(1)}, 9, lightAt)
	fx.add(t, app.Frame{Type: typeLight, Object: objM33, Filter: filterRed, Exposure: f(600), Gain: f(100), Offset: f(50), SetTemp: f(-15), BinX: f(1)}, 3, lightAt)
	clean := darkcheck.StateClean
	fx.add(t, app.Frame{Type: typeDark, Exposure: f(600), Gain: f(100), Offset: f(50), SetTemp: f(-15), BinX: f(1), CalCheck: &clean}, 25,
		time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	created := fx.now.Add(-2 * time.Hour)
	for i, r := range []app.CalibrationRequest{
		{ID: "d31", Type: app.CalibrationDarks, Object: objM31, Status: app.CalibrationOpen},
		{ID: "d31b", Type: app.CalibrationDarks, Object: objM31, Status: app.CalibrationOpen},
		{ID: "d33", Type: app.CalibrationDarks, Object: objM33, Status: app.CalibrationOpen},
		{ID: "b31", Type: app.CalibrationBias, Object: objM31, Status: app.CalibrationOpen},
		{ID: "done", Type: app.CalibrationDarks, Object: objNGC7000, Status: app.CalibrationDone},
		{ID: "gone", Type: app.CalibrationBias, Object: objNGC7000, Status: app.CalibrationCancelled},
	} {
		r.Night, r.CreatedAt = *day(requestNight), created.Add(time.Duration(i)*time.Second)
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	fx.add(t, app.Frame{Type: typeDark, Exposure: f(600), Gain: f(100), Offset: f(50), SetTemp: f(-15), BinX: f(1), CalCheck: &clean}, 3, fx.now.Add(-time.Hour))
	fx.add(t, app.Frame{Type: typeBias, Gain: f(100), Offset: f(50), BinX: f(1)}, 4, fx.now.Add(-3*time.Hour))
	fx.add(t, app.Frame{Type: typeBias, Gain: f(100), Offset: f(50), BinX: f(1)}, 5, fx.now.Add(-time.Hour))
	return fx
}

func TestRequestsMergeIntoTheDarkBacklog(t *testing.T) {
	t.Parallel()
	fx := newReqFixture(t)
	b, err := coverage.BuildDarkBacklog(context.Background(), fx.db, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Combos) != 3 {
		t.Fatalf("combos %+v", b.Combos)
	}
	m31, m33, other := b.Combos[0], b.Combos[1], b.Combos[2]
	if m31.ComboKey != "300|100|50|1|-10" || m31.Priority != 0 || len(m31.Requests) != 2 || m31.Requests[0].ID != "d31" ||
		m31.Requests[0].Night != requestNight || m31.LightsScaled != 6 || m31.FramesNeeded != 10 {
		t.Errorf("a request merges into the combo covering its set temperature: %+v", m31)
	}
	if m33.ComboKey != "600|100|50|1|-15" || m33.Priority != 0 || len(m33.Requests) != 1 || m33.LightsBlocked != 0 ||
		m33.FramesHave != 3 || m33.FramesNeeded != 10 || m33.LatestNight != requestNight {
		t.Errorf("a request for lights with a full set adds its own combo: %+v", m33)
	}
	if other.SetTemp != -20 || other.Priority != 1 || other.Requests == nil || len(other.Requests) != 0 {
		t.Errorf("the combo nobody asked for: %+v", other)
	}
}

func TestRequestsMergeIntoTheBiasBacklog(t *testing.T) {
	t.Parallel()
	fx := newReqFixture(t)
	b, err := coverage.BuildBiasBacklog(context.Background(), fx.db, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Combos) != 1 {
		t.Fatalf("combos %+v", b.Combos)
	}
	c := b.Combos[0]
	if c.ComboKey != "bias|100|50|1" || c.Priority != 0 || len(c.Requests) != 1 || c.Requests[0].ID != "b31" || c.Requests[0].Object != objM31 ||
		c.LightsMissing != 0 || c.FramesHave != 5 {
		t.Errorf("bias combo %+v", c)
	}
}

func TestMeasureRequests(t *testing.T) {
	t.Parallel()
	fx := newReqFixture(t)
	var reqs []app.CalibrationRequest
	if err := fx.db.Order("id").Find(&reqs).Error; err != nil {
		t.Fatal(err)
	}
	prog, err := coverage.MeasureRequests(context.Background(), fx.db, reqs)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]coverage.RequestProgress{}
	for i, r := range reqs {
		byID[r.ID] = prog[i]
	}
	if p := byID["d31"]; len(p.Items) != 2 || p.Lights != 6 || p.Items[0].FramesNeeded != 10 {
		t.Errorf("darks for two set temperatures %+v", p)
	}
	if p := byID["b31"]; len(p.Items) != 1 || p.Items[0].FramesHave != 5 || p.Items[0].FramesNeeded != coverage.BiasFramesTarget || p.Done() {
		t.Errorf("bias %+v", p)
	}
}
