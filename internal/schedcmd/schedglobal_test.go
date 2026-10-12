package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func marshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGlobalSchedulingCommand(t *testing.T) {
	t.Parallel()
	spec, err := schedcmd.Default().Get(schedcmd.KindSchedulingGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Destination() != schedcmd.DestinationApp {
		t.Fatal("saved in the app")
	}
	before := schedglobal.Defaults()
	after := before
	after.DitherEvery, after.EnableGrader = 3, false
	raw := marshal(t, schedcmd.GlobalSchedulingPayload{Before: nil, After: &after})
	if err := spec.Validate(raw); err != nil {
		t.Fatal(err)
	}
	d, err := spec.Describe(raw)
	if err != nil || len(d.Diffs) != 2 || d.Diffs[0].Field != "Dither every" || string(d.Diffs[0].Before) != "2" || string(d.Diffs[0].After) != "3" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []schedglobal.Settings{{DitherEvery: -1}, {FlatsHandling: 150}, {MaximumAltitude: 91}, {MaximumAltitude: 12}, {MeridianWindow: 721}} {
		b := marshal(t, schedcmd.GlobalSchedulingPayload{After: &bad})
		if err := spec.Validate(b); !errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	same := marshal(t, schedcmd.GlobalSchedulingPayload{Before: &before, After: &before})
	if spec.Validate(same) == nil {
		t.Error("no change")
	}
	kind, inv, err := spec.Inverse(raw)
	var back schedcmd.GlobalSchedulingPayload
	if err != nil || kind != schedcmd.KindSchedulingGlobal || json.Unmarshal(inv, &back) != nil || back.After != nil || back.Before == nil || *back.Before != after {
		t.Fatalf("inverse %s %s %v", kind, inv, err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.GlobalSchedulingSetting{}); err != nil {
		t.Fatal(err)
	}
	applier, ok := spec.(interface {
		ApplyApp(context.Context, *gorm.DB, json.RawMessage) error
	})
	if !ok {
		t.Fatal("app command")
	}
	ctx := context.Background()
	if err := applier.ApplyApp(ctx, db, raw); err != nil {
		t.Fatal(err)
	}
	if s, _ := schedglobal.Load(ctx, db); s.Settings == nil || *s.Settings != after {
		t.Fatalf("saved %+v", s)
	}
	if err := applier.ApplyApp(ctx, db, raw); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("stale save: %v", err)
	}
	if err := applier.ApplyApp(ctx, db, inv); err != nil {
		t.Fatal(err)
	}
	if s, _ := schedglobal.Load(ctx, db); s.Settings != nil {
		t.Errorf("undo removes the saved set: %+v", s)
	}
}
