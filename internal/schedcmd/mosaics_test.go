package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const dolphin = "Dolphin Head"

func TestMosaicAdoptAppliesAndUndoes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newService(t)
	db := svc.AppDB
	if err := db.AutoMigrate(&app.MosaicAdoption{}, &app.MosaicPanel{}, &app.FrameTarget{}, &app.Frame{}); err != nil {
		t.Fatal(err)
	}
	panels, err := json.Marshal([]mosaics.AdoptedPanel{
		{TargetGUID: "t1", Target: "IC 4604 Panel 1", Panel: 1},
		{TargetGUID: "t2", Target: "IC 4604 Panel 2", Panel: 2, Neighbours: []string{"t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := json.Marshal(mosaicstore.FramesProposal{Object: dolphin, TargetGUID: "t9", ProjectGUID: "p9"})
	if err != nil {
		t.Fatal(err)
	}
	rows := []app.MosaicAdoption{
		{ID: 1, Subject: "project:p", ProjectGUID: "p", Project: "Rho", Kind: mosaics.KindMosaic, Proposal: string(panels), Status: app.AdoptionProposed},
		{ID: 2, Subject: "frames:" + dolphin, Project: dolphin, Kind: mosaicstore.KindFrames, Proposal: string(frames), Status: app.AdoptionProposed},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]app.Frame{
		{ID: 7, Key: "d1", ETag: "e", Type: "LIGHT", Object: dolphin},
		{ID: 8, Key: "d2", ETag: "e", Type: "LIGHT", Object: dolphin},
	}).Error; err != nil {
		t.Fatal(err)
	}
	payload := `{"decisions":[{"id":1,"subject":"project:p","title":"Rho","before":"proposed","after":"accepted"},` +
		`{"id":2,"subject":"frames:Dolphin Head","title":"Dolphin Head","before":"proposed","after":"accepted"}]}`
	r, err := svc.Submit(ctx, schedcmd.KindMosaicAdopt, json.RawMessage(payload), "web")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusSaved || r.Category != schedcmd.CategoryAdoption || len(r.Diffs) != 2 {
		t.Fatalf("record %+v", r)
	}
	count := func(m any, where string, args ...any) int64 {
		var n int64
		if err := db.Model(m).Where(where, args...).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(&app.MosaicPanel{}, "project_guid = ?", "p"); n != 2 {
		t.Errorf("%d panels after accepting", n)
	}
	if n := count(&app.FrameTarget{}, "target_guid = ? AND method = ?", "t9", mosaicstore.LinkReview); n != 2 {
		t.Errorf("%d frames linked", n)
	}
	if _, err := svc.Submit(ctx, schedcmd.KindMosaicAdopt, json.RawMessage(payload), "web"); !errors.Is(err, mosaicstore.ErrConflict) {
		t.Errorf("stale decision: %v", err)
	}
	if _, err := svc.Undo(ctx, r.ID, "web"); err != nil {
		t.Fatal(err)
	}
	if n := count(&app.MosaicPanel{}, "1 = 1"); n != 0 {
		t.Errorf("%d panels after undo", n)
	}
	if n := count(&app.FrameTarget{}, "1 = 1"); n != 0 {
		t.Errorf("%d frame links after undo", n)
	}
	if n := count(&app.MosaicAdoption{}, "status = ?", app.AdoptionProposed); n != 2 {
		t.Errorf("%d adoptions back to proposed", n)
	}
}

func TestMosaicAdoptRejectsBadPayloads(t *testing.T) {
	t.Parallel()
	spec := schedcmd.MosaicAdoptSpec()
	for _, p := range []string{
		`{}`,
		`{"decisions":[{"id":1,"before":"proposed","after":"proposed"}]}`,
		`{"decisions":[{"id":1,"before":"proposed","after":"maybe"}]}`,
		`{"decisions":[{"id":0,"before":"proposed","after":"accepted"}]}`,
		`{"decisions":[{"id":1,"before":"proposed","after":"accepted"},{"id":1,"before":"proposed","after":"rejected"}]}`,
	} {
		if err := spec.Validate(json.RawMessage(p)); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s: %v", p, err)
		}
	}
}
