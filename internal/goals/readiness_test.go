package goals

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

func TestReady(t *testing.T) {
	t.Parallel()
	insufficient := ErrInsufficientData.Error()
	noDepth := "no depth: x"
	cases := []struct {
		name   string
		m      *app.GoalMeasurement
		p      *Progress
		subs   int
		state  string
		reason string
		open   bool
	}{
		{"no master", nil, nil, 0, StateCollecting, ReasonNoMaster, true},
		{"stacked, not measured", nil, nil, 3, StateCollecting, ReasonNotMeasured, true},
		{"too few subs", &app.GoalMeasurement{Error: &insufficient}, nil, MinSubs - 1, StateCollecting, insufficient, true},
		{"enough subs, waiting for the measurement", &app.GoalMeasurement{Error: &insufficient}, nil, MinSubs, StateCollecting, insufficient, false},
		{"measured, not done", &app.GoalMeasurement{}, &Progress{Progress: 0.4}, 40, StateMeasured, "", true},
		{"measured, done", &app.GoalMeasurement{}, &Progress{Progress: 0.4, Done: true}, 40, StateMeasured, "", false},
		{"no depth, below the bound", &app.GoalMeasurement{}, &Progress{Unmeasured: noDepth}, UnmeasurableSubLimit - 1, StateUnmeasurable, noDepth, true},
		{"no depth, at the bound", &app.GoalMeasurement{}, &Progress{Unmeasured: noDepth}, UnmeasurableSubLimit, StateUnmeasurable, noDepth, false},
		{"no depth but plateau stop", &app.GoalMeasurement{}, &Progress{Unmeasured: noDepth, Done: true}, 9, StateMeasured, "", false},
	}
	for _, c := range cases {
		r := Ready(c.m, c.p, c.subs)
		if r.State != c.state || r.Reason != c.reason || r.Open != c.open || r.StackSubs != c.subs || r.MinSubs != MinSubs || r.SubLimit != UnmeasurableSubLimit {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	if MinSubs != 8 || UnmeasurableSubLimit <= MinSubs {
		t.Fatalf("min %d limit %d", MinSubs, UnmeasurableSubLimit)
	}
}

func addStack(t *testing.T, db *gorm.DB, object, filter string, good, bad int) {
	t.Helper()
	s := app.Stack{Object: object, Filter: filter, Width: 100, Height: 100, Subs: good}
	if err := db.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	key := "reg"
	for i := range good + bad {
		sf := app.StackFrame{FrameID: s.ID*1000 + i, StackID: &s.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: 300, RegisteredKey: &key}
		if i >= good {
			sf.Status = app.StackStatusLowScore
		}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func readinessDBs(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	sched := schedDB(t, true)
	for _, c := range []string{"state TEXT", "reason TEXT", "stack_subs INTEGER", "min_subs INTEGER", "sub_limit INTEGER"} {
		if err := sched.Exec("ALTER TABLE ts_goal_progress ADD COLUMN " + c).Error; err != nil {
			t.Fatal(err)
		}
	}
	db := appDB(t)
	if err := db.AutoMigrate(&app.Stack{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	return sched, db
}

type readinessOut struct {
	TargetGUID string
	Filter     string
	Kind       int
	GoalValue  *float64
	Progress   *float64
	Done       int
	State      *string
	Reason     *string
	StackSubs  *int
	MinSubs    *int
	SubLimit   *int
	MeasuredAt *time.Time
}

func TestMeasurableSubs(t *testing.T) {
	t.Parallel()
	_, db := readinessDBs(t)
	addStack(t, db, objGarlic, filterHa, 5, 3)
	subs, err := MeasurableSubs(context.Background(), db)
	if err != nil || len(subs) != 1 || subs[Key{Object: objGarlic, Filter: filterHa}] != 5 {
		t.Fatalf("%v %v", subs, err)
	}
	empty, err := MeasurableSubs(context.Background(), appDB(t))
	if err != nil || len(empty) != 0 {
		t.Fatalf("%v %v", empty, err)
	}
}

func TestPublishReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched, db := readinessDBs(t)
	if !HasReadinessColumns(sched) || HasReadinessColumns(schedDB(t, true)) {
		t.Fatal("readiness columns")
	}
	if err := sched.Exec(`INSERT INTO ts_goal VALUES ('g-m31', 'L', 1, NULL, 26, 0, NULL, '2026-10-01 00:00:00')`).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	insufficient := ErrInsufficientData.Error()
	ms := []app.GoalMeasurement{
		{Object: objGarlic, Filter: filterHa, Subs: 6, Error: &insufficient, MeasuredAt: at},
		{Object: objM31, Filter: "L", Subs: 40, SNR: 12, EffectiveHours: 3, GainPerHourPct: 9, MeasuredAt: at},
	}
	if err := db.Create(&ms).Error; err != nil {
		t.Fatal(err)
	}
	addStack(t, db, objGarlic, filterHa, 7, 2)
	addStack(t, db, objM31, "L", 40, 0)

	p := &Publisher{App: db, Sched: sched, Mode: PublishOn}
	if sum, err := p.Publish(ctx); err != nil || sum.Rows != 3 || sum.Changed != 3 {
		t.Fatalf("publish %+v %v", sum, err)
	}
	checkReadinessRows(t, readRows(t, sched), at)
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 0 {
		t.Fatalf("unchanged publish %+v %v", sum, err)
	}
	ref := "reg"
	sid := 1
	if err := db.Create(&app.StackFrame{FrameID: 99999, StackID: &sid, Status: app.StackStatusAdded, Weight: 1, Exposure: 300, RegisteredKey: &ref}).Error; err != nil {
		t.Fatal(err)
	}
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 1 {
		t.Fatalf("one more sub %+v %v", sum, err)
	}
	if ha := readRows(t, sched)[guidGar+"/"+tsHa]; *ha.StackSubs != MinSubs {
		t.Errorf("H-a after one more sub %+v", ha)
	}
}

func readRows(t *testing.T, sched *gorm.DB) map[string]readinessOut {
	t.Helper()
	var rows []readinessOut
	if err := sched.Table(ProgressTable).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]readinessOut{}
	for _, r := range rows {
		out[r.TargetGUID+"/"+r.Filter] = r
	}
	return out
}

func checkReadinessRows(t *testing.T, byKey map[string]readinessOut, at time.Time) {
	t.Helper()
	insufficient := ErrInsufficientData.Error()
	ha := byKey[guidGar+"/"+tsHa]
	if ha.State == nil || *ha.State != StateCollecting || ha.Reason == nil || *ha.Reason != insufficient || *ha.StackSubs != 7 ||
		*ha.MinSubs != MinSubs || ha.Done != 0 || ha.GoalValue == nil || *ha.GoalValue != 15 || ha.MeasuredAt == nil || !ha.MeasuredAt.Equal(at) {
		t.Errorf("H-a %+v", ha)
	}
	o3 := byKey[guidGar+"/OIII"]
	if o3.State == nil || *o3.State != StateCollecting || *o3.Reason != ReasonNoMaster || *o3.StackSubs != 0 || o3.Kind != kindDepth || *o3.GoalValue != 25.9 || o3.MeasuredAt != nil {
		t.Errorf("O-III %+v", o3)
	}
	l := byKey["g-m31/L"]
	if l.State == nil || *l.State != StateUnmeasurable || *l.Reason != "no depth: the master has no plate solution to calibrate against" ||
		*l.StackSubs != 40 || *l.SubLimit != UnmeasurableSubLimit || l.Done != 0 {
		t.Errorf("L %+v", l)
	}
	if _, ok := byKey["g-gone/Ha"]; ok {
		t.Error("a goal for a target Target Scheduler does not have was published")
	}
}
