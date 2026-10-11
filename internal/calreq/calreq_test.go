package calreq_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	m31     = "M 31"
	night   = "2026-10-09"
	author  = "jacob"
	typeRed = "Red"
	green   = "Green"
	flat    = "FLAT"
	dark    = "DARK"
)

func fp(v float64) *float64 { return &v }

type fixture struct {
	db  *gorm.DB
	now time.Time
	n   int
}

func newFixture(t *testing.T) *fixture {
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
	fx := &fixture{db: db, now: time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)}
	n, _ := time.Parse(time.DateOnly, night)
	for _, filter := range []string{typeRed, green} {
		fx.add(t, app.Frame{Type: "LIGHT", Object: m31, Filter: filter, Exposure: fp(300), Gain: fp(100), Offset: fp(50),
			SetTemp: fp(-10), BinX: fp(1), Night: &n}, 4)
	}
	fx.add(t, app.Frame{Type: "LIGHT", Object: "NGC 7000", Filter: "Ha", Exposure: fp(600), Night: &n}, 2)
	return fx
}

func (fx *fixture) add(t *testing.T, f app.Frame, count int) {
	t.Helper()
	for range count {
		fx.n++
		f.ID = 0
		f.Key = fmt.Sprintf("f-%d", fx.n)
		f.ETag, f.LastModified = "e", fx.now
		if f.DateObs != nil {
			at := f.DateObs.Add(time.Second)
			f.DateObs = &at
		}
		if err := fx.db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func payload(typ, object string, target *int) calreq.Payload {
	return calreq.Payload{Type: typ, Object: object, TargetID: target, Night: night}
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	var r calreq.RefusalError
	if !errors.As(err, &r) || !strings.Contains(r.Msg, want) {
		t.Errorf("got %v, want a refusal saying %q", err, want)
	}
}

func TestOpenRefusesWhatCannotBeTaken(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	id := 12
	_, err := calreq.Open(ctx, fx.db, payload(app.CalibrationFlats, m31, nil), "r0", author, fx.now)
	refused(t, err, "target_id")
	_, err = calreq.Open(ctx, fx.db, payload(app.CalibrationDarks, "M 33", nil), "r0", author, fx.now)
	refused(t, err, "no lights of M 33")
	_, err = calreq.Open(ctx, fx.db, payload(app.CalibrationBias, "NGC 7000", nil), "r0", author, fx.now)
	refused(t, err, "no GAIN or OFFSET")
	_, err = calreq.Open(ctx, fx.db, calreq.Payload{Type: "lights", Object: m31, Night: night}, "r0", author, fx.now)
	refused(t, err, "type must be")
	r, err := calreq.Open(ctx, fx.db, payload(app.CalibrationFlats, m31, &id), "r1", author, fx.now)
	if err != nil || r.Status != app.CalibrationOpen || r.CreatedBy != author || *r.TargetID != 12 {
		t.Fatalf("open %+v %v", r, err)
	}
	_, err = calreq.Open(ctx, fx.db, payload(app.CalibrationFlats, m31, &id), "r2", author, fx.now)
	refused(t, err, "an open flats request for M 31 on the night of 2026-10-09 already exists")
	if _, err := calreq.Open(ctx, fx.db, payload(app.CalibrationDarks, m31, nil), "r3", author, fx.now); err != nil {
		t.Errorf("a darks request beside the flats one: %v", err)
	}
}

func TestCancel(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	_, err := calreq.Cancel(ctx, fx.db, payload(app.CalibrationBias, m31, nil), author, fx.now)
	refused(t, err, "there is no open bias request")
	if _, err := calreq.Open(ctx, fx.db, payload(app.CalibrationBias, m31, nil), "r1", author, fx.now); err != nil {
		t.Fatal(err)
	}
	r, err := calreq.Cancel(ctx, fx.db, payload(app.CalibrationBias, m31, nil), "someone", fx.now.Add(time.Hour))
	if err != nil || r.Status != app.CalibrationCancelled || r.CancelledBy == nil || *r.CancelledBy != "someone" || r.CancelledAt == nil {
		t.Fatalf("cancel %+v %v", r, err)
	}
	if _, err := calreq.Open(ctx, fx.db, payload(app.CalibrationBias, m31, nil), "r2", author, fx.now); err != nil {
		t.Errorf("a new request after a cancel: %v", err)
	}
	list, err := calreq.List(ctx, fx.db, m31, calreq.StatusAll)
	if err != nil || len(list) != 2 || list[0].ID != "r1" && list[0].ID != "r2" {
		t.Errorf("list %+v %v", list, err)
	}
	if open, err := calreq.List(ctx, fx.db, "", app.CalibrationOpen); err != nil || len(open) != 1 || open[0].ID != "r2" {
		t.Errorf("open %+v %v", open, err)
	}
	if _, err := calreq.List(ctx, fx.db, "", "maybe"); !errors.Is(err, calreq.ErrStatus) {
		t.Errorf("bad status: %v", err)
	}
}

func TestRefreshMarksRequestsDoneFromTheIndex(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	id := 12
	for i, typ := range []string{app.CalibrationFlats, app.CalibrationDarks, app.CalibrationBias} {
		if _, err := calreq.Open(ctx, fx.db, payload(typ, m31, &id), fmt.Sprintf("r%d", i), author, fx.now); err != nil {
			t.Fatal(err)
		}
	}
	before := fx.now.Add(-time.Hour)
	after := fx.now.Add(time.Hour)
	fx.add(t, app.Frame{Type: flat, Object: m31, Filter: typeRed, DateObs: &before}, 5)
	fx.add(t, app.Frame{Type: flat, Object: m31, Filter: typeRed, DateObs: &after}, 3)
	fx.add(t, app.Frame{Type: flat, Object: m31, Filter: green, DateObs: &after}, 2)
	clean, leak := darkcheck.StateClean, darkcheck.StateLeak
	fx.add(t, app.Frame{Type: dark, Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(-10.5), BinX: fp(1), DateObs: &after, CalCheck: &clean}, 24)
	fx.add(t, app.Frame{Type: dark, Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(-10), BinX: fp(1), DateObs: &after, CalCheck: &leak}, 3)
	fx.add(t, app.Frame{Type: dark, Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(-12), BinX: fp(1), DateObs: &after}, 3)
	fx.add(t, app.Frame{Type: "BIAS", Gain: fp(100), Offset: fp(50), BinX: fp(1), DateObs: &after}, 30)
	done, err := calreq.Refresh(ctx, fx.db, after)
	if err != nil || len(done) != 1 || done[0].Type != app.CalibrationBias || done[0].DoneAt == nil {
		t.Fatalf("first refresh %+v %v", done, err)
	}
	fx.add(t, app.Frame{Type: flat, Object: m31, Filter: green, DateObs: &after}, 1)
	fx.add(t, app.Frame{Type: dark, Exposure: fp(300), Gain: fp(100), Offset: fp(50), SetTemp: fp(-9.5), BinX: fp(1), DateObs: &after}, 1)
	done, err = calreq.Refresh(ctx, fx.db, after)
	if err != nil || len(done) != 2 {
		t.Fatalf("second refresh %+v %v", done, err)
	}
	if done, err := calreq.Refresh(ctx, fx.db, after); err != nil || len(done) != 0 {
		t.Errorf("third refresh %+v %v", done, err)
	}
}
