package discoveries

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// TestARunWhoseZTFQueriesFailedIsRedone is the 45 method 15 runs of
// October 2026 whose ZTF light curve queries failed: 24 held fadings in 18
// of them could have been promoted had ZTF answered.
func TestARunWhoseZTFQueriesFailedIsRedone(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	r := New(db, nil, &memStore{m: map[string][]byte{}}, &Catalogs{}, Options{BackfillDays: 1, Threads: 1})
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	const object = "Recheck Field"
	seedSubs(t, db, object, broadbandClear, day(10), day(11), day(12), day(13))
	rows, err := r.subRows(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	nights := byGroup(rows)[object][GroupBroadband]
	later := now.Add(time.Hour)
	runs := []app.DiscoveryRun{
		{Object: object, Night: day(10), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: catalogZTF, SubsHash: subsHash(nights[day(10)])},
		{Object: object, Night: day(11), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: sourceFink + "," + catalogZTF, SubsHash: subsHash(nights[day(11)]), ZTFFailures: maxZTFRuns},
		{Object: object, Night: day(12), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: catalogZTF, SubsHash: subsHash(nights[day(12)]), ZTFFailures: 1, NextTry: &later},
		{Object: object, Night: day(13), Group: GroupBroadband, Method: Method, State: app.DiscoveryDone, Failed: sourceFink, SubsHash: subsHash(nights[day(13)])},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	jobs, err := r.due(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !jobs[0].Night.Equal(day(10)) {
		t.Fatalf("due %+v, want only the night whose ZTF queries failed", jobs)
	}
	now = later.Add(time.Minute)
	if jobs, err = r.due(context.Background()); err != nil || len(jobs) != 2 {
		t.Fatalf("after the wait: %+v, %v", jobs, err)
	}
}

func TestNoteZTFCountsTheRunsInARowItFailed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	run := app.DiscoveryRun{ZTFFailures: 1}
	noteZTF(&run, []string{sourceFink, catalogZTF}, now)
	if run.ZTFFailures != 2 || run.NextTry == nil || !run.NextTry.Equal(now.Add(ztfRecheckAfter)) {
		t.Fatalf("after a failure %+v", run)
	}
	noteZTF(&run, []string{sourceFink}, now)
	if run.ZTFFailures != 0 || run.NextTry != nil {
		t.Fatalf("after ZTF answered %+v", run)
	}
	before := app.DiscoveryRun{Failed: catalogZTF}
	noteZTF(&before, []string{catalogZTF}, now)
	if before.ZTFFailures != 2 {
		t.Fatalf("a run that failed before the count was kept %+v", before)
	}
}
