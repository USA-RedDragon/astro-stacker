package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestCalibrationRequestSavesAndUndoCancels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newService(t)
	db := svc.AppDB
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	n := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	gain, offset, bin := 100.0, 50.0, 1.0
	if err := db.Create(&app.Frame{Key: "l1", ETag: "e", Type: lightFrame, Object: "M 31", Filter: "Red", Gain: &gain, Offset: &offset,
		BinX: &bin, Night: &n}).Error; err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"type":"flats","object":"M 31","target_id":12,"night":"2026-10-09"}`)
	r, err := svc.Submit(ctx, schedcmd.KindCalibrationRequest, payload, "jacob")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusSaved || r.Category != schedcmd.CategoryCalibration || r.Title != "Request flats for M 31 · night of 2026-10-09" ||
		len(r.Objects) != 1 || r.Objects[0].ID != 12 || r.Objects[0].Entity != "target" {
		t.Fatalf("record %+v", r)
	}
	var req app.CalibrationRequest
	if err := db.Take(&req).Error; err != nil || req.Status != app.CalibrationOpen || req.CreatedBy != "jacob" {
		t.Fatalf("request %+v %v", req, err)
	}
	if _, err := svc.Submit(ctx, schedcmd.KindCalibrationRequest, payload, "jacob"); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("a duplicate request: %v", err)
	}
	u, err := svc.Undo(ctx, r.ID, "other")
	if err != nil || u.Kind != schedcmd.KindCalibrationCancel || u.Title != "Undo: Cancel the flats request for M 31 · night of 2026-10-09" {
		t.Fatalf("undo %+v %v", u, err)
	}
	if err := db.Take(&req).Error; err != nil || req.Status != app.CalibrationCancelled || req.CancelledBy == nil || *req.CancelledBy != "other" {
		t.Fatalf("after undo %+v %v", req, err)
	}
	if _, err := svc.Submit(ctx, schedcmd.KindCalibrationCancel, payload, "jacob"); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("cancel with nothing open: %v", err)
	}
	if _, err := svc.Submit(ctx, schedcmd.KindCalibrationRequest, json.RawMessage(`{"type":"flats","object":"M 31","night":"2026-10-09"}`), "jacob"); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("flats without target_id: %v", err)
	}
}
