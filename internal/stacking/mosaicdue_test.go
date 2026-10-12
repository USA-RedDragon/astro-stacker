package stacking

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestMosaicDueThrottlesInsteadOfWaitingForQuiet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	every := 30 * time.Minute
	built := app.Mosaic{Signature: "old", Panels: 4, UpdatedAt: now.Add(-10 * time.Minute)}
	if mosaicDue(built, "old", 4, every, now) {
		t.Fatal("unchanged mosaic rebuilt")
	}
	if mosaicDue(built, "new", 4, every, now) {
		t.Fatal("rebuilt 10 minutes after the last build")
	}
	if !mosaicDue(built, "new", 5, every, now) {
		t.Fatal("a new panel waited for the throttle")
	}
	built.UpdatedAt = now.Add(-31 * time.Minute)
	if !mosaicDue(built, "new", 4, every, now) {
		t.Fatal("changed panels never rebuilt while they kept updating")
	}
	if !mosaicDue(app.Mosaic{}, "new", 1, every, now) {
		t.Fatal("first mosaic not built")
	}
}
