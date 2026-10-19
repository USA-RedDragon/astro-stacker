package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	objM13   = "M 13"
	objAbell = "Abell 85"
	groupHa  = "H-a"
)

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func TestCalibrationItem(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	view := func(c *observatory.Calibration) observatory.View {
		return observatory.View{Reachable: observatory.Online, Status: &observatory.Status{Calibration: c}}
	}
	_, err := calibrationItem(view(nil), now)
	if !errors.Is(err, errNoCalibration) {
		t.Fatalf("no calibration block: %v", err)
	}
	_, err = calibrationItem(view(&observatory.Calibration{Kind: "done", UpdatedAt: &now}), now)
	if !errors.Is(err, errNoCalibration) {
		t.Fatalf("a finished run is not busy: %v", err)
	}
	old := now.Add(-time.Hour)
	if _, err := calibrationItem(view(&observatory.Calibration{Kind: "darks", UpdatedAt: &old}), now); !errors.Is(err, errNoCalibration) {
		t.Fatalf("an hour-old report is not busy: %v", err)
	}
	it, err := calibrationItem(view(&observatory.Calibration{Kind: "cooling", UpdatedAt: &now,
		Camera: &observatory.CalibrationCamera{Temperature: fp(-6)}, Cooling: &observatory.CalibrationCool{From: fp(10), Target: fp(-10)}}), now)
	if err != nil || it.State != activity.StateRunning || it.Done != 160 || it.Total != 200 || it.Detail != "now −6.0 °C, target −10.0 °C" {
		t.Fatalf("cooling %+v %v", it, err)
	}
	it, err = calibrationItem(view(&observatory.Calibration{Kind: "darks", UpdatedAt: &now,
		Set:   &observatory.CalibrationSet{Exposure: fp(300), FramesTotal: ip(30), FramesDone: ip(12)},
		Queue: []observatory.CalibrationQueue{{Remaining: ip(5)}, {Remaining: ip(0)}, {}, {Status: "skipped", Remaining: ip(3)}}}), now)
	if err != nil || it.Done != 12 || it.Total != 30 || it.Unit != "frames" || it.Current != "Darks, 300 s" || it.Queued != 2 {
		t.Fatalf("darks %+v %v", it, err)
	}
}

func TestLiveGroups(t *testing.T) {
	t.Parallel()
	at := func(m int) time.Time { return time.Date(2026, 10, 16, 5, m, 0, 0, time.UTC) }
	groups := []vetGroup{
		{Object: objAbell, Started: at(0), Updated: at(37)},
		{Object: "Barnard's E", Started: at(10), Updated: at(10)},
		{Object: "Caldwell 14", Started: at(23), Updated: at(23)},
	}
	got := liveGroups(groups, 2)
	if len(got) != 2 || got[0].Object != objAbell || got[1].Object != "Caldwell 14" {
		t.Fatalf("live groups %+v", got)
	}
	if liveGroups(groups, 0) != nil {
		t.Fatal("no workers, no groups")
	}
}

func TestGoalsItem(t *testing.T) {
	t.Parallel()
	it := goalsItem(goals.BackfillLive{State: goals.BackfillRunning, Workers: 3, Queued: 40, DoneInPass: 10})
	if it.State != activity.StateRunning || it.Done != 10 || it.Total != 50 || it.Current != "3 workers measuring" {
		t.Fatalf("running %+v", it)
	}
	it = goalsItem(goals.BackfillLive{State: goals.BackfillPaused, Queued: 4})
	if it.State != activity.StateQueued || it.Total != 4 {
		t.Fatalf("paused %+v", it)
	}
	if it := goalsItem(goals.BackfillLive{State: goals.BackfillIdle}); it.State != activity.StateIdle {
		t.Fatalf("idle %+v", it)
	}
}

func TestActivityFromTheDatabase(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "activity", "activity")
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}, &app.JobProgress{},
		&app.Frame{}, &app.Stack{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	night := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	stack := app.Stack{Object: objM13, Filter: "Red"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	for _, d := range []int{1, 2, 5} {
		obs, exp, key := night(d).Add(4*time.Hour), 300.0, fmt.Sprintf("registered/m13-%d", d)
		light := app.Frame{Key: key, Type: "LIGHT", Object: objM13, Filter: stack.Filter, Night: new(night(d)), DateObs: &obs, Exposure: &exp, LastModified: obs}
		if err := db.Create(&light).Error; err != nil {
			t.Fatal(err)
		}
		sf := app.StackFrame{FrameID: light.ID, StackID: &stack.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: exp, RegisteredKey: &key, ProcessedAt: obs}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
	runs := []app.DiscoveryRun{
		{Object: objM13, Night: night(1), Group: discoveries.GroupBroadband, Method: discoveries.Method, State: app.DiscoveryDone},
		{Object: objM13, Night: night(1), Group: groupHa, Method: discoveries.Method, State: app.DiscoverySkipped},
		{Object: objM13, Night: night(2), Group: discoveries.GroupBroadband, Method: discoveries.Method - 1, State: app.DiscoveryDone},
		{Object: objM13, Night: night(3), Group: discoveries.GroupBroadband, Method: discoveries.Method, State: app.DiscoveryRunning, Stage: "measured sub 6 of 57"},
		{Object: objM13, Night: night(4), Group: discoveries.GroupBroadband, Method: discoveries.Method, State: app.DiscoveryFailed, Attempts: 3},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	src := &activitySources{DB: db, BackfillDays: 3650}
	items, err := src.discoveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if it := items[0]; it.Queued != 1 || it.Warning != "1 night failed every attempt" {
		t.Fatalf("within the window %+v", it)
	}
	src.BackfillDays = 1
	items, err = src.discoveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	it := items[0]
	if it.Label != "Discoveries re-check" || it.Done != 2 || it.Total != 4 || it.Unit != "nights" || it.State != activity.StateRunning ||
		it.Queued != 0 || it.Current != "M 13, night of 3 Sep 2026: measured sub 6 of 57" ||
		it.Warning != "1 night failed every attempt; 1 older night waits for an archive re-check" {
		t.Fatalf("discoveries %+v", it)
	}
	vetChecks(t, db, now)
}

func vetChecks(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	night := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	run := app.DiscoveryRun{Object: "Vet run", Night: night(9), Group: discoveries.GroupBroadband}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	src := &activitySources{DB: db}

	sources := []app.DiscoverySource{
		{Object: objAbell, Kind: app.DiscoveryKindChange, Night: night(1)},
		{Object: objAbell, Kind: app.DiscoveryKindChange, Night: night(1)},
		{Object: "Andromeda", Kind: app.DiscoveryKindChange, Night: night(1)},
	}
	if err := db.Create(&sources).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		c := app.DiscoveryCandidate{RunID: run.ID, Object: s.Object, Night: night(1), Kind: app.DiscoveryKindChange,
			Class: discoveries.ClassNewVariable, SourceID: &s.ID}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	src.Now = func() time.Time { return now }
	items, err := src.vetting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != activity.StateQueued || items[0].Queued != 2 || items[0].Current != "Abell 85, Andromeda" {
		t.Fatalf("queued vetting %+v", items)
	}

	rep := activity.Report{State: activity.ReportRunning, Current: "Abell 85: reading our own subs", Done: 40, Total: 120, Unit: "subs",
		Queued: 1, QUnit: "targets", Started: now, Updated: now}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.JobProgress{Kind: activity.KindVetting, Worker: "pod-a", Report: string(b), UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	src.pickAt = time.Time{}
	items, err = src.vetting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "vetting:pod-a" || items[0].State != activity.StateRunning || items[0].Done != 40 || items[0].Total != 120 ||
		items[0].Queued != 1 {
		t.Fatalf("reported vetting %+v", items)
	}

	r := dbReporter(db, "test-kind")
	r.Start(ctx)
	r.Set("step", 3, 9, "things")
	r.Finish(ctx, nil)
	var row app.JobProgress
	if err := db.Where("kind = ?", "test-kind").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	got, err := activity.ParseReport([]byte(row.Report))
	if err != nil || got.State != activity.ReportDone || got.Done != 3 || got.Total != 9 {
		t.Fatalf("db reporter wrote %+v %v", got, err)
	}
}
