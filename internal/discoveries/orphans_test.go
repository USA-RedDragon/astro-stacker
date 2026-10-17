package discoveries

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

func seedSubs(t *testing.T, db *gorm.DB, object, filter string, nights ...time.Time) {
	t.Helper()
	stack := app.Stack{Object: object, Filter: filter, UpdatedAt: time.Now()}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	processed := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for n, night := range nights {
		start := night.Add(4 * time.Hour)
		exp := 300.0
		k := fmt.Sprintf("registered/%s/%s/%d.xisf", object, filter, n)
		fr := app.Frame{Key: fmt.Sprintf("%s/LIGHT/%s_%d.xisf", object, filter, n), Type: "LIGHT", Object: object, Filter: filter, Exposure: &exp, DateObs: &start, Night: &night, LastModified: start}
		if err := db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: fr.ID, StackID: &stack.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: exp, RegisteredKey: &k, ProcessedAt: processed}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func day(d int) time.Time { return time.Date(2025, 2, d, 0, 0, 0, 0, time.UTC) }

// TestAnOldRunNoPassTakesIsRetired is IC 3393 Panel 1's H-a run of
// 2025-02-28: a request for every group queued H-a on a night with no H-a
// subs, method 3 skipped it, and the re-check then waited on it forever.
func TestAnOldRunNoPassTakesIsRetired(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	r := New(db, nil, &memStore{m: map[string][]byte{}}, &Catalogs{}, Options{BackfillDays: 1, Threads: 1})
	r.now = func() time.Time { return time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC) }
	const object = "Orphan Field"
	seedSubs(t, db, object, testNarrowband, day(19), day(23), day(24))
	seedSubs(t, db, object, "Luminance", day(23), day(24), day(28))
	seedSubs(t, db, object, "SII", day(24))
	next := r.now().Add(-time.Hour)
	orphan := app.DiscoveryRun{Object: object, Night: day(28), Group: testNarrowband, Method: 3, State: app.DiscoverySkipped, Error: new("too few subs to compare"), Attempts: 1, NextTry: &next, Requested: true}
	few := app.DiscoveryRun{Object: object, Night: day(24), Group: "SII", Method: 3, State: app.DiscoverySkipped, SubsHash: "h:n1"}
	kept := app.DiscoveryRun{Object: object, Night: day(19), Group: testNarrowband, Method: 3, State: app.DiscoveryDone}
	done := app.DiscoveryRun{Object: object, Night: day(20), Group: testNarrowband, Method: 3, State: app.DiscoveryDone}
	stale := app.DiscoveryRun{Object: object, Night: day(21), Group: testNarrowband, Method: 3, State: app.DiscoveryRunning}
	asked := app.DiscoveryRun{Object: object, Night: day(22), Group: testNarrowband, Method: 3, State: app.DiscoveryQueued, Requested: true}
	for _, run := range []*app.DiscoveryRun{&orphan, &few, &kept, &done, &stale, &asked} {
		if err := db.Create(run).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	db.Model(&app.DiscoveryRun{}).Where("id = ?", stale.ID).UpdateColumn("updated_at", r.now().Add(-time.Hour))
	if _, err := r.due(ctx); err != nil {
		t.Fatal(err)
	}
	var got []app.DiscoveryRun
	db.Order("id").Find(&got)
	if got[0].Method != Method || got[0].State != app.DiscoverySkipped || got[0].Requested || *got[0].Error != noSubsReason {
		t.Errorf("orphan run not retired: %+v", got[0])
	}
	if got[1].Method != Method || *got[1].Error != errNotEligible.Error() || got[1].SubsHash != "" {
		t.Errorf("run of a group with too few nights not retired: %+v", got[1])
	}
	if got[2].Method != 3 {
		t.Errorf("a run a pass takes again was retired: %+v", got[2])
	}
	if got[3].Method != 3 || got[3].State != app.DiscoveryDone {
		t.Errorf("a done run whose subs are away was retired, hiding its finds: %+v", got[3])
	}
	if got[4].Method != Method || got[4].State != app.DiscoverySkipped {
		t.Errorf("a run left running by a dead runner was not retired: %+v", got[4])
	}
	if got[5].Method != 3 || got[5].State != app.DiscoveryQueued || !got[5].Requested {
		t.Errorf("a queued request was retired before the requests took it: %+v", got[5])
	}
	r.Request(object, day(28), "")
	var runs []app.DiscoveryRun
	db.Where("night = ? AND state = ?", day(28), app.DiscoveryQueued).Find(&runs)
	if len(runs) != 1 || runs[0].Group != GroupBroadband {
		t.Errorf("a request for every group queued groups with no subs that night: %+v", runs)
	}
}
