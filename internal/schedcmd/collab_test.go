package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCollabSwitchSpec(t *testing.T) {
	t.Parallel()
	spec := schedcmd.CollabSwitchSpec()
	for _, bad := range []string{`{}`, `{"on":"yes"}`, `[]`, `7`} {
		if err := spec.Validate(json.RawMessage(bad)); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s validated: %v", bad, err)
		}
	}
	if spec.Destination() != schedcmd.DestinationApp || spec.Category() != schedcmd.CategoryControl {
		t.Error("destination or category")
	}
	if _, _, err := spec.Inverse(json.RawMessage(`{"on":true}`)); !errors.Is(err, schedcmd.ErrNoInverse) {
		t.Errorf("inverse %v", err)
	}
	for p, title := range map[string]string{`{"on":true}`: "Turn the collab on", `{"on":false}`: "Pause the collab", `{"on":true,"resend":"NINA restarted"}`: "Send the collab switch again: on"} {
		d, err := spec.Describe(json.RawMessage(p))
		if err != nil || d.Title != title || len(d.Diffs) != 1 {
			t.Errorf("%s: %+v %v", p, d, err)
		}
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "app.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.CollabSwitch{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, err := schedcmd.LoadCollabSwitch(ctx, db); ok || err != nil {
		t.Fatalf("empty table read as set: %v", err)
	}
	applier, ok := spec.(schedcmd.AppApplier)
	if !ok {
		t.Fatal("not an app applier")
	}
	if err := applier.ApplyApp(schedcmd.WithAuthor(ctx, "jacob"), db, json.RawMessage(`{"on":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := applier.ApplyApp(schedcmd.WithAuthor(ctx, "stacker"), db, json.RawMessage(`{"on":false,"resend":"x"}`)); err != nil {
		t.Fatal(err)
	}
	row, ok, err := schedcmd.LoadCollabSwitch(ctx, db)
	if err != nil || !ok || !row.Enabled || row.SetBy != "jacob" || time.Since(row.SetAt) > time.Minute {
		t.Errorf("stored %+v %v %v", row, ok, err)
	}
	if err := schedcmd.SaveCollabSwitch(ctx, db, false, "ann", time.Now()); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := schedcmd.LoadCollabSwitch(ctx, db); row.Enabled || row.SetBy != "ann" {
		t.Errorf("overwrite %+v", row)
	}
	var n int64
	db.Model(&app.CollabSwitch{}).Count(&n)
	if n != 1 {
		t.Errorf("%d rows", n)
	}
}
