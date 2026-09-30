package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBackoff(t *testing.T) {
	t.Parallel()
	for attempts, want := range map[int]time.Duration{0: time.Hour, 1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour,
		5: 16 * time.Hour, 6: 24 * time.Hour, 20: 24 * time.Hour} {
		if got := backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestPlanVerdictsNew(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	img := func(id, status int, reason, target, file string) tsImage {
		return tsImage{ID: id, GUID: "g", PlanID: 7, Status: status, Reason: reason, Target: target, File: file, Grader: true}
	}
	images := []tsImage{
		img(1, quality.GradingAccepted, "", "M81", "a.xisf"),
		img(2, quality.GradingAccepted, "", "M81", "b.xisf"),
		img(3, quality.GradingPending, "", "M81", "c.xisf"),
		img(4, quality.GradingRejected, "HFR", "M81", "d.xisf"),
		img(5, quality.GradingAccepted, "", "M81", "e.xisf"),
		img(6, quality.GradingAccepted, "", "M81", "dup.xisf"),
		img(7, quality.GradingAccepted, "", "M81", "dup.xisf"),
		img(8, quality.GradingAccepted, "", "Other", "f.xisf"),
		{ID: 9, Status: quality.GradingAccepted, Target: "M81", File: "g.xisf"}, // project without grading
		img(10, quality.GradingAccepted, "", "M81", "h.xisf"),
	}
	frames := []verdictFrame{
		{FrameID: 101, Key: "M81/LIGHT/a.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore},
		{FrameID: 102, Key: "M81/LIGHT/b.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusMoon},
		{FrameID: 103, Key: "M81/LIGHT/c.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore},   // Pending in TS
		{FrameID: 104, Key: "M81/LIGHT/d.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore},   // TS rejected it
		{FrameID: 105, Key: "M81/LIGHT/e.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusAdded},      // stacked
		{FrameID: 106, Key: "M81/LIGHT/dup.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore}, // two images
		{FrameID: 107, Key: "NGC/LIGHT/f.xisf", Object: "NGC", Filter: "Red", Status: app.StackStatusLowScore},   // other target's file
		{FrameID: 108, Key: "M81/LIGHT/g.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore},
		{FrameID: 109, Key: "M81/LIGHT/h.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore},
	}
	plan := planVerdicts(frames, images, map[int]app.TSVerdict{}, now, 2)
	if len(plan.Sends) != 2 {
		t.Fatalf("sends = %+v, want 2", plan.Sends)
	}
	if s := plan.Sends[0]; s.Image.ID != 1 || s.Verdict != app.TSVerdictReject || s.Reason != "stacker: sky" || s.Kind != "new" {
		t.Errorf("first send = %+v", s)
	}
	if s := plan.Sends[1]; s.Image.ID != 2 || s.Reason != "stacker: moon" {
		t.Errorf("second send = %+v", s)
	}
	if len(plan.Records) != 2 || plan.Records[0].State != app.TSVerdictSent || plan.Records[0].Attempts != 1 ||
		plan.Records[0].ExposurePlanID != 7 || !plan.Records[0].NextAttemptAt.Equal(now.Add(time.Hour)) {
		t.Errorf("records = %+v", plan.Records)
	}
	want := map[string]int{"ambiguous": 1, "no_image": 1, "no_grader": 1, "over_max": 1}
	for k, n := range want {
		if plan.Skipped[k] != n {
			t.Errorf("skipped[%s] = %d, want %d (%v)", k, plan.Skipped[k], n, plan.Skipped)
		}
	}
}

func TestPlanVerdictsLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	im := tsImage{ID: 1, GUID: "g", PlanID: 7, Status: quality.GradingAccepted, Target: "M81", File: "a.xisf", Grader: true}
	low := verdictFrame{FrameID: 101, Key: "M81/LIGHT/a.xisf", Object: "M81", Filter: "Red", Status: app.StackStatusLowScore}
	added := low
	added.Status = app.StackStatusAdded
	step := func(f verdictFrame, im tsImage, known map[int]app.TSVerdict, at time.Time) (verdictPlan, map[int]app.TSVerdict) {
		plan := planVerdicts([]verdictFrame{f}, []tsImage{im}, known, at, 10)
		next := map[int]app.TSVerdict{}
		for k, v := range known {
			next[k] = v
		}
		for _, r := range plan.Records {
			next[r.AcquiredImageID] = r
		}
		return plan, next
	}

	plan, known := step(low, im, map[int]app.TSVerdict{}, now)
	if len(plan.Sends) != 1 {
		t.Fatalf("new: %+v", plan)
	}
	// TS was grading the plan: not applied. Nothing before the backoff ...
	plan, known = step(low, im, known, now.Add(30*time.Minute))
	if len(plan.Sends) != 0 {
		t.Fatalf("retry before backoff: %+v", plan.Sends)
	}
	// ... then again, with a longer wait after.
	plan, known = step(low, im, known, now.Add(time.Hour))
	if len(plan.Sends) != 1 || plan.Sends[0].Kind != "retry" || known[1].Attempts != 2 ||
		!known[1].NextAttemptAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("retry: %+v, record %+v", plan.Sends, known[1])
	}
	// Applied: TS shows the stacker's reject.
	im.Status, im.Reason = quality.GradingRejected, "stacker: sky"
	at := now.Add(2 * time.Hour)
	plan, known = step(low, im, known, at)
	if len(plan.Sends) != 0 || known[1].State != app.TSVerdictApplied || !known[1].AppliedAt.Equal(at) || known[1].NextAttemptAt != nil {
		t.Fatalf("applied: %+v, record %+v", plan.Sends, known[1])
	}
	// Nothing more while it stays so.
	if plan, _ := step(low, im, known, now.Add(48*time.Hour)); len(plan.Sends) != 0 || len(plan.Records) != 0 {
		t.Fatalf("steady: %+v", plan)
	}
	// The stacker stacks it after all: undo.
	plan, known = step(added, im, known, now.Add(50*time.Hour))
	if len(plan.Sends) != 1 || plan.Sends[0].Kind != "undo" || plan.Sends[0].Verdict != app.TSVerdictAccept ||
		known[1].State != app.TSVerdictSent || known[1].Verdict != app.TSVerdictAccept {
		t.Fatalf("undo: %+v, record %+v", plan.Sends, known[1])
	}
	im.Status, im.Reason = quality.GradingAccepted, ""
	plan, known = step(added, im, known, now.Add(51*time.Hour))
	if len(plan.Sends) != 0 || known[1].State != app.TSVerdictApplied {
		t.Fatalf("undo applied: %+v, record %+v", plan.Sends, known[1])
	}
	// And out again (moon rule changed): reject again.
	moon := low
	moon.Status = app.StackStatusMoon
	plan, known = step(moon, im, known, now.Add(60*time.Hour))
	if len(plan.Sends) != 1 || plan.Sends[0].Kind != "redo" || plan.Sends[0].Reason != "stacker: moon" {
		t.Fatalf("redo: %+v", plan.Sends)
	}
	im.Status, im.Reason = quality.GradingRejected, "stacker: moon"
	_, known = step(moon, im, known, now.Add(61*time.Hour))
	// Someone accepts it again in TS: final.
	im.Status, im.Reason = quality.GradingAccepted, ""
	_, known = step(moon, im, known, now.Add(70*time.Hour))
	if known[1].State != app.TSVerdictOverridden {
		t.Fatalf("override: %+v", known[1])
	}
	for _, f := range []verdictFrame{moon, low, added} {
		if plan, _ := step(f, im, known, now.Add(100*time.Hour)); len(plan.Sends) != 0 || len(plan.Records) != 0 {
			t.Fatalf("overridden but %+v", plan)
		}
	}
}

func TestPlanVerdictsMoot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	im := tsImage{ID: 1, GUID: "g", PlanID: 7, Status: quality.GradingRejected, Reason: "HFR", Target: "M81", File: "a.xisf", Grader: true}
	f := verdictFrame{FrameID: 101, Key: "M81/LIGHT/a.xisf", Object: "M81", Status: app.StackStatusLowScore}
	next := now
	known := map[int]app.TSVerdict{1: {AcquiredImageID: 1, Verdict: app.TSVerdictReject, State: app.TSVerdictSent, Attempts: 1, NextAttemptAt: &next}}
	plan := planVerdicts([]verdictFrame{f}, []tsImage{im}, known, now, 10)
	if len(plan.Sends) != 0 || len(plan.Records) != 1 || plan.Records[0].State != app.TSVerdictMoot {
		t.Fatalf("TS reject: %+v", plan)
	}
	// Never undone either: TS rejected it, not the stacker.
	f.Status = app.StackStatusAdded
	plan = planVerdicts([]verdictFrame{f}, []tsImage{im}, map[int]app.TSVerdict{1: plan.Records[0]}, now, 10)
	if len(plan.Sends) != 0 {
		t.Fatalf("undo of a TS reject: %+v", plan.Sends)
	}
}

// schedTables creates the scheduler database tables the verdicts touch, as
// SQLite (Target Scheduler's own engine).
func schedTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, sql := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, enablegrader INTEGER)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, projectid INTEGER)`,
		`CREATE TABLE acquiredimage ("Id" INTEGER PRIMARY KEY, "projectId" INTEGER, "targetId" INTEGER, acquireddate INTEGER,
			filtername TEXT, "gradingStatus" INTEGER, metadata TEXT, rejectreason TEXT, "profileId" TEXT, "exposureId" INTEGER, guid TEXT)`,
		`CREATE TABLE stacker_verdict (acquiredimage_id INTEGER PRIMARY KEY, guid TEXT NOT NULL, exposureplan_id INTEGER NOT NULL,
			verdict INTEGER NOT NULL, reason TEXT NOT NULL, attempt INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMP, updated_at TIMESTAMP)`,
		`CREATE TABLE stacker_reconcile (exposureplan_id INTEGER PRIMARY KEY, attempt INTEGER NOT NULL DEFAULT 0, updated_at TIMESTAMP)`,
		`INSERT INTO project VALUES (1, 'P', 1)`,
		`INSERT INTO target VALUES (87, 'Bode''s Galaxy', 1)`,
		`INSERT INTO acquiredimage VALUES
			(10, 1, 87, 0, 'Red', 1, '{"FileName":"A:\\NINA\\Bode''s Galaxy\\LIGHT\\r1.xisf"}', '', 'p', 577, 'g10'),
			(11, 1, 87, 0, 'Red', 1, '{"FileName":"A:\\NINA\\Bode''s Galaxy\\LIGHT\\r2.xisf"}', '', 'p', 577, 'g11'),
			(12, 1, 87, 0, 'Red', 1, '{"FileName":"A:\\NINA\\Bode''s Galaxy\\LIGHT\\r3.xisf"}', '', 'p', 577, 'g12'),
			(13, 1, 87, 0, 'Red', 1, 'not json', '', 'p', 577, 'g13')`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

func TestSendVerdicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.TSVerdict{}); err != nil {
		t.Fatal(err)
	}
	sched, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schedTables(t, sched)
	night := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	for i, s := range []string{app.StackStatusLowScore, app.StackStatusMoon, app.StackStatusAdded} {
		obs := night.Add(time.Duration(i) * time.Hour)
		f := app.Frame{ID: i + 1, Key: "Bode's Galaxy/LIGHT/r" + string(rune('1'+i)) + ".xisf", ETag: "e", Type: "LIGHT",
			Object: "Bode's Galaxy", Filter: "Red", DateObs: &obs, LastModified: obs}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: s}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db, sched: sched, drain: make(chan struct{})}
	count := func(q string) int {
		var n int
		if err := sched.Raw(q).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Off does nothing; a dry run plans but writes nothing.
	if plan, err := p.SendVerdicts(ctx, VerdictOptions{Mode: "off"}); err != nil || len(plan.Sends) != 0 {
		t.Fatalf("off: %+v %v", plan, err)
	}
	plan, err := p.SendVerdicts(ctx, VerdictOptions{Mode: verdictModeDryRun})
	if err != nil || len(plan.Sends) != 2 {
		t.Fatalf("dry run: %+v %v", plan, err)
	}
	var records int64
	db.Model(&app.TSVerdict{}).Count(&records)
	if n := count("SELECT count(*) FROM stacker_verdict"); n != 0 || records != 0 {
		t.Fatalf("dry run wrote %d verdicts, %d records", n, records)
	}

	// Limited to later subs: nothing.
	if plan, err := p.SendVerdicts(ctx, VerdictOptions{Mode: verdictModeOn, Since: night.Add(24 * time.Hour)}); err != nil || len(plan.Sends) != 0 {
		t.Fatalf("since: %+v %v", plan, err)
	}
	if plan, err := p.SendVerdicts(ctx, VerdictOptions{Mode: verdictModeOn, Targets: []string{"M 31"}}); err != nil || len(plan.Sends) != 0 {
		t.Fatalf("targets: %+v %v", plan, err)
	}

	opts := VerdictOptions{Mode: verdictModeOn, Targets: []string{"Bode's Galaxy"}, Since: night}
	if _, err := p.SendVerdicts(ctx, opts); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		AcquiredimageID int
		Guid            string
		ExposureplanID  int
		Verdict         int
		Reason          string
		Attempt         int
	}
	sched.Raw("SELECT acquiredimage_id, guid, exposureplan_id, verdict, reason, attempt FROM stacker_verdict ORDER BY 1").Scan(&rows)
	if len(rows) != 2 || rows[0].AcquiredimageID != 10 || rows[0].Guid != "g10" || rows[0].ExposureplanID != 577 ||
		rows[0].Verdict != 2 || rows[0].Reason != "stacker: sky" || rows[1].AcquiredimageID != 11 || rows[1].Reason != "stacker: moon" {
		t.Fatalf("stacker_verdict = %+v", rows)
	}
	// Again at once: nothing new, nothing sent again before the backoff.
	if plan, err := p.SendVerdicts(ctx, opts); err != nil || len(plan.Sends) != 0 {
		t.Fatalf("second sweep: %+v %v", plan, err)
	}
	// An hour on, still not applied: sent again, attempt + 1.
	db.Model(&app.TSVerdict{}).Where("1 = 1").Update("next_attempt_at", time.Now().Add(-time.Minute))
	if plan, err := p.SendVerdicts(ctx, opts); err != nil || len(plan.Sends) != 2 || plan.Sends[0].Kind != "retry" {
		t.Fatalf("retry sweep: %+v %v", plan, err)
	}
	if n := count("SELECT sum(attempt) FROM stacker_verdict"); n != 2 {
		t.Fatalf("attempts after retry = %d, want 2", n)
	}

	// The observatory applied one (as SymmetricDS brings back): recorded,
	// and its plan gets a reconcile request, once a day.
	sched.Exec(`UPDATE acquiredimage SET "gradingStatus" = 2, rejectreason = 'stacker: sky' WHERE "Id" = 10`)
	if _, err := p.SendVerdicts(ctx, opts); err != nil {
		t.Fatal(err)
	}
	var v app.TSVerdict
	db.First(&v, 10)
	if v.State != app.TSVerdictApplied || v.AppliedAt == nil {
		t.Fatalf("record after apply = %+v", v)
	}
	if n := count("SELECT count(*) FROM stacker_reconcile WHERE exposureplan_id = 577 AND attempt = 0"); n != 1 {
		t.Fatalf("reconcile requests = %d", n)
	}
	if _, err := p.SendVerdicts(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT attempt FROM stacker_reconcile WHERE exposureplan_id = 577"); n != 0 {
		t.Fatalf("reconcile asked again within a day: attempt %d", n)
	}

	// The stacker stacks sub 1 after all: undone.
	db.Model(&app.StackFrame{}).Where("frame_id = 1").Update("status", app.StackStatusAdded)
	plan, err = p.SendVerdicts(ctx, opts)
	if err != nil || len(plan.Sends) != 1 || plan.Sends[0].Kind != "undo" {
		t.Fatalf("undo sweep: %+v %v", plan, err)
	}
	if n := count("SELECT verdict FROM stacker_verdict WHERE acquiredimage_id = 10"); n != 1 {
		t.Fatalf("verdict after undo = %d, want 1", n)
	}
}
