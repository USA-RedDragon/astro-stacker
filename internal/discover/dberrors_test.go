package discover_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"gorm.io/gorm"
)

const injected = "injected_failure"

func failQueries(t *testing.T, db *gorm.DB, match string) {
	t.Helper()
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == match || strings.Contains(tx.Statement.SQL.String(), match) {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM " + injected)
			tx.Statement.Vars = nil
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
}

func TestSubjectsFailWhenAnAppTableCheckFails(t *testing.T) {
	t.Parallel()
	s := newService(t)
	failQueries(t, s.AppDB, "sqlite_master")
	if subs, err := s.Subjects(context.Background()); err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("subjects built from a failed table check: %d subjects, %v", len(subs), err)
	}
}

func TestSubjectsFailWhenASchedulerColumnCheckFails(t *testing.T) {
	t.Parallel()
	s := newService(t)
	failQueries(t, s.SchedDB, "sqlite_master")
	if subs, err := s.Subjects(context.Background()); err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("subjects built from a failed column check: %d subjects, %v", len(subs), err)
	}
}

func TestSubjectsFailWhenJudgingFails(t *testing.T) {
	t.Parallel()
	s := newService(t)
	failQueries(t, s.SchedDB, "exposureplan")
	if _, err := s.Overview(context.Background()); err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("catalogues without Target Scheduler judgements: %v", err)
	}
}

func TestPickFailsWhenATableCheckFails(t *testing.T) {
	t.Parallel()
	s, db := pickFixture(t)
	failQueries(t, db, "sqlite_master")
	if _, err := s.Pick(context.Background(), "M51", pickPlans(), noGoal()); err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("pick built from a failed table check: %v", err)
	}
}

func TestCollabPracticeStatesATableCheckFailure(t *testing.T) {
	t.Parallel()
	s := newService(t)
	if _, err := s.Subjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	failQueries(t, s.AppDB, "sqlite_master")
	p := starfront.Project{ID: "d", Name: "visits", Status: "done", Created: float64(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).Unix()),
		Payload: starfront.Payload{Region: starfront.Region{RA: 83.8, Dec: -5.4, Width: 1.66, Height: 1.48},
			Requirements: starfront.Requirements{MinFramesPerVisit: ptr(10), RequireCalibrated: true}}}
	v, err := s.Collabs(context.Background(), fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{p}}})
	if err != nil || len(v.Closed) != 1 {
		t.Fatalf("%v %+v", err, v)
	}
	for _, c := range v.Closed[0].Criteria {
		if (c.Name == "Frames per visit" || c.Name == "Calibration") && !strings.Contains(c.You, injected) {
			t.Errorf("%s hides the database error: %+v", c.Name, c)
		}
	}
}
