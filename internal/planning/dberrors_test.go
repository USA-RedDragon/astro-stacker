package planning

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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

func TestLoadFailsWhenASchedulerReadFails(t *testing.T) {
	t.Parallel()
	for _, match := range []string{"sqlite_master", "ts_target_season", "acquiredimage", "profilepreference"} {
		t.Run(match, func(t *testing.T) {
			t.Parallel()
			sched, appDB := testDBs(t)
			if err := sched.Exec(`create table profilepreference ("Id" integer primary key, "profileId" text, exposurethrottle real, "delayGrading" real)`).Error; err != nil {
				t.Fatal(err)
			}
			failQueries(t, sched, match)
			if s, err := Load(context.Background(), sched, appDB, Inputs{}); err == nil || !strings.Contains(err.Error(), injected) {
				p, _ := s.Project(5)
				t.Fatalf("loaded with %s failing: %v, season %+v, last sub %v, completion %+v", match, err, p.Season, p.LastSub, p.Completion)
			}
		})
	}
}

func TestLoadFailsWhenAnAppTableCheckFails(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	failQueries(t, appDB, "sqlite_master")
	if _, err := Load(context.Background(), sched, appDB, Inputs{}); err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("loaded with the app table check failing: %v", err)
	}
}

func TestProfilePreferencesBeforeDelayGrading(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	if err := sched.Exec(`create table profilepreference ("Id" integer primary key, "profileId" text, exposurethrottle real)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := sched.Exec(`insert into profilepreference values (1, 'p', 150)`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Project(5)
	if c := p.Completion; c.Source != "profile" || c.ExposureThrottle != 150 || c.DelayGrading != pluginDelayGrading {
		t.Fatalf("completion %+v", c)
	}
}

func TestAppInputsFailWhenObjectLinksFail(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	if err := appDB.AutoMigrate(&app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	if err := appDB.Create(&app.FrameTarget{FrameID: 1, Object: "Garlic", TargetGUID: "g12", Method: app.LinkHeader}).Error; err != nil {
		t.Fatal(err)
	}
	seen := 0
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table != "frame_targets" {
			return
		}
		if seen++; seen == 1 {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM " + injected)
			tx.Statement.Vars = nil
		}
	}
	if err := appDB.Callback().Query().Before("gorm:query").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if err := appDB.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	in, err := AppInputs(context.Background(), appDB, sched)
	if err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("inputs without object links: %v, links %+v", err, in.Links)
	}
}
