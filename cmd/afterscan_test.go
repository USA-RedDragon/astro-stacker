package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/cmd"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestAfterScanMarksRequestsDoneAndPublishes(t *testing.T) {
	t.Parallel()
	db := memDB(t)
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	created := time.Now().UTC().Add(-time.Hour)
	gain, offset, bin := 100.0, 50.0, 1.0
	if err := db.Create(&app.Frame{Key: "l", ETag: "e", Type: "LIGHT", Object: "M 31", Gain: &gain, Offset: &offset, BinX: &bin, Night: &night}).Error; err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		at := created.Add(time.Duration(i+1) * time.Second)
		if err := db.Create(&app.Frame{Key: "b" + at.String(), ETag: "e", Type: "BIAS", Gain: &gain, Offset: &offset, BinX: &bin, DateObs: &at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.CalibrationRequest{ID: "r1", Type: app.CalibrationBias, Object: "M 31", Night: night, Status: app.CalibrationOpen,
		CreatedAt: created}).Error; err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker()
	_, ch, cancel := broker.Subscribe(0)
	defer cancel()
	ran := 0
	cmd.AfterScan(db, broker, func(context.Context) { ran++ }, func(context.Context) { ran++ })(context.Background())
	var r app.CalibrationRequest
	if err := db.Take(&r, "id = ?", "r1").Error; err != nil || r.Status != app.CalibrationDone || r.DoneAt == nil {
		t.Fatalf("request %+v %v", r, err)
	}
	if ran != 2 {
		t.Errorf("%d publishers ran", ran)
	}
	select {
	case msg := <-ch:
		if !strings.Contains(string(msg), `"type":"calibration"`) || !strings.Contains(string(msg), `"status":"done"`) {
			t.Errorf("event %s", msg)
		}
	default:
		t.Error("no calibration event")
	}
}
