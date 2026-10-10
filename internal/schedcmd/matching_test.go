package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestCatalogMatchConfirmAndUndo(t *testing.T) {
	t.Parallel()
	svc := newService(t)
	if err := svc.AppDB.AutoMigrate(&app.ObjectXref{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := json.RawMessage(`{"subject":"project:Garlic Nebula","subject_name":"Garlic Nebula","object_id":"G116.9+00.2","object_name":"CTB 1","before":"","after":"confirmed","confidence":0.92}`)
	r, err := svc.Submit(ctx, schedcmd.KindCatalogMatch, payload, author)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != schedcmd.StatusSaved || r.Category != schedcmd.CategoryMatching || r.Title != "Name match · Garlic Nebula" {
		t.Fatalf("record %+v", r)
	}
	var rows []app.ObjectXref
	svc.AppDB.Find(&rows)
	if len(rows) != 1 || rows[0].Decision != app.XrefConfirmed || rows[0].Confidence != 0.92 {
		t.Fatalf("rows after confirm %+v", rows)
	}
	again := json.RawMessage(`{"subject":"project:Garlic Nebula","object_id":"G116.9+00.2","before":"confirmed","after":"rejected"}`)
	if _, err := svc.Submit(ctx, schedcmd.KindCatalogMatch, again, author); err != nil {
		t.Fatal(err)
	}
	svc.AppDB.Find(&rows)
	if len(rows) != 1 || rows[0].Decision != app.XrefRejected {
		t.Fatalf("rows after reject %+v", rows)
	}
	if _, err := svc.Undo(ctx, r.ID, author); !errors.Is(err, schedcmd.ErrMatchChanged) {
		t.Fatalf("undoing a superseded confirm: %v", err)
	}
	var log []schedcmd.Record
	svc.AppDB.Order("created_at").Find(&log)
	if _, err := svc.Undo(ctx, log[len(log)-1].ID, author); err != nil {
		t.Fatal(err)
	}
	u, err := svc.Undo(ctx, r.ID, author)
	if err != nil {
		t.Fatal(err)
	}
	if u.UndoOf != r.ID {
		t.Errorf("undo %+v", u)
	}
	rows = nil
	svc.AppDB.Find(&rows)
	if len(rows) != 0 {
		t.Fatalf("rows after undo %+v", rows)
	}
}

func TestCatalogMatchValidate(t *testing.T) {
	t.Parallel()
	spec := schedcmd.MatchSpec{}
	for _, bad := range []string{
		`{"subject":"","object_id":"M31","after":"confirmed"}`,
		`{"subject":"a","object_id":"M31","before":"confirmed","after":"confirmed"}`,
		`{"subject":"a","object_id":"M31","after":"maybe"}`,
		`not json`,
	} {
		if err := spec.Validate(json.RawMessage(bad)); err == nil {
			t.Errorf("%s validated", bad)
		}
	}
}
