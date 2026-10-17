package discoveries

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const frameLight = "LIGHT"

func progressDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Postgres(t, "progress", "progress")
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.DiscoveryRun{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedLights(t *testing.T, db *gorm.DB, object string, night time.Time, processed time.Time, status string) {
	t.Helper()
	stack := app.Stack{Object: object, Filter: broadbandClear, UpdatedAt: processed}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	start, exp := night.Add(4*time.Hour), 300.0
	fr := app.Frame{Key: object + night.Format(time.DateOnly), Type: frameLight, Object: object, Filter: broadbandClear, Exposure: &exp, DateObs: &start, Night: &night, LastModified: start}
	if err := db.Create(&fr).Error; err != nil {
		t.Fatal(err)
	}
	k := "registered/" + fr.Key
	if err := db.Create(&app.StackFrame{FrameID: fr.ID, StackID: &stack.ID, Status: status, Weight: 1, Exposure: exp, RegisteredKey: &k, ProcessedAt: processed}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestProgressCountsOnlyNightsARunnerTakes: a night still settling, an old
// run of a moving target and a night with no accepted subs are not due, as
// no pass takes them, and a run whose ZTF queries failed is due again once
// its wait is over if its night still has subs to compare.
func TestProgressCountsOnlyNightsARunnerTakes(t *testing.T) {
	t.Parallel()
	db := progressDB(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	since := now.AddDate(0, 0, -30)
	night := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	const settling, comet, recheck, waiting, gone, rejected, due = "Settling Field", "C/2025 R2 (SWAN)", "Recheck Field", "Waiting Field", "Gone Field", "Rejected Field", "Due Field"
	seedLights(t, db, settling, night(10, 10), now.Add(-5*time.Minute), app.StackStatusAdded)
	seedLights(t, db, comet, night(10, 11), now.Add(-48*time.Hour), app.StackStatusAdded)
	seedLights(t, db, rejected, night(10, 12), now.Add(-48*time.Hour), "rejected")
	seedSubs(t, db, recheck, broadbandClear, night(1, 5), night(1, 6), night(1, 7))
	seedSubs(t, db, due, broadbandClear, night(10, 1), night(10, 2), night(10, 3))
	past, later := now.Add(-time.Hour), now.Add(time.Hour)
	runs := []app.DiscoveryRun{
		{Object: settling, Night: night(10, 10), Group: GroupBroadband, Method: Method - 1, State: app.DiscoveryDone},
		{Object: comet, Night: night(10, 11), Group: GroupBroadband, Method: Method - 1, State: app.DiscoveryDone},
		{Object: recheck, Night: night(1, 5), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: catalogZTF, ZTFFailures: 1, NextTry: &past},
		{Object: waiting, Night: night(1, 6), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: catalogZTF, ZTFFailures: 1, NextTry: &later},
		{Object: gone, Night: night(1, 8), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: catalogZTF, ZTFFailures: 1, NextTry: &past},
		{Object: rejected, Night: night(10, 12), Group: GroupBroadband, Method: Method - 1, State: app.DiscoveryDone},
		{Object: due, Night: night(10, 1), Group: GroupBroadband, Method: Method - 1, State: app.DiscoveryDone},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	p, err := ReadProgress(context.Background(), db, now, since, map[string]bodies.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Nights != 7 || p.Settled != 3 || p.Old != 3 || p.Due != 2 || p.Settling != 1 || p.NoSubs != 1 {
		t.Fatalf("progress %+v, want 7 nights, 3 settled, 3 old, 2 due, 1 settling and 1 with no subs", p)
	}
}
