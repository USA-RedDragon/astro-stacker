package gorm

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	garlicLive   = "06096b69-bfa4-5778-b270-63487470cdbc"
	garlicStale  = "be58011c-80ce-582b-be8d-c7c7a117a4d7"
	andromedaOld = "3f99e20a-9543-5cc5-86bf-feb13ab186be"
	andromedaNow = "6cff9784-ec06-5f06-8c14-fe5d715bb50e"
	night1021    = "2026-10-21"
	secondary    = "secondary"
)

type legacyRunNight struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	ProjectGUID string `gorm:"not null;uniqueIndex:idx_run_night"`
	Night       string `gorm:"not null;uniqueIndex:idx_run_night;index"`
	Kind        string `gorm:"not null;default:''"`
	SourceID    int    `gorm:"not null;index"`
	First, Last time.Time
	Subs        int
	Measured    int
	JudgedAt    time.Time
}

func (legacyRunNight) TableName() string { return "run_nights" }

func TestMergeRunNightsOfReplacedProjects(t *testing.T) {
	t.Parallel()
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		checkMergeRunNights(t, testdb.SQLite(t, "merge"))
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		checkMergeRunNights(t, testdb.Postgres(t, "merge", "merge"))
	})
}

func checkMergeRunNights(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&legacyRunNight{}, &app.EclipseRequest{}); err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 22, h, m, 0, 0, time.UTC) }
	for _, r := range []app.EclipseRequest{
		{ID: 2, SourceID: 27822, Kind: secondary, ProjectGUID: garlicStale, State: app.EclipseCancel},
		{ID: 10, SourceID: 236, Kind: secondary, ProjectGUID: andromedaOld, State: app.EclipseCancel},
		{ID: 60, SourceID: 27822, Kind: secondary, ProjectGUID: garlicLive, State: app.EclipseSent},
		{ID: 61, SourceID: 236, Kind: secondary, ProjectGUID: andromedaNow, State: app.EclipseSent},
		{ID: 87, SourceID: 15523, Kind: "dip", ProjectGUID: "8a8fc07c-0e9b-5138-8b26-a8d6e48b64a5", State: app.EclipseDone},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []legacyRunNight{
		{ID: 1, ProjectGUID: "8a8fc07c-0e9b-5138-8b26-a8d6e48b64a5", Night: "2026-10-20", Kind: app.RunNightDip, SourceID: 15523,
			First: time.Date(2026, 10, 21, 2, 10, 0, 0, time.UTC), Last: time.Date(2026, 10, 21, 7, 19, 0, 0, time.UTC), Subs: 93, Measured: 93, JudgedAt: at(18, 18)},
		{ID: 2, ProjectGUID: garlicLive, Night: night1021, Kind: app.RunNightEclipse, SourceID: 27822,
			First: at(1, 34), Last: at(8, 18), Subs: 169, Measured: 169, JudgedAt: at(9, 22)},
		{ID: 3, ProjectGUID: garlicStale, Night: night1021, Kind: app.RunNightEclipse, SourceID: 27822,
			First: at(1, 34), Last: at(8, 18), Subs: 169, Measured: 169, JudgedAt: at(9, 21)},
		{ID: 5, ProjectGUID: andromedaOld, Night: night1021, Kind: app.RunNightEclipse, SourceID: 236,
			First: at(8, 21), Last: at(11, 37), Subs: 87, Measured: 87, JudgedAt: at(15, 36)},
		{ID: 6, ProjectGUID: andromedaNow, Night: night1021, Kind: app.RunNightEclipse, SourceID: 236,
			First: at(8, 21), Last: at(11, 37), Subs: 87, Measured: 87, JudgedAt: at(15, 35)},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := mergeRunNights(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.RunNight{}); err != nil {
		t.Fatal(err)
	}
	var ids []int
	if err := db.Model(&app.RunNight{}).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 6 {
		t.Fatalf("rows %v, want 1, 2 and 6: one per run, under the live requests", ids)
	}
	m := db.Migrator()
	if m.HasIndex(&app.RunNight{}, "idx_run_night") || !m.HasIndex(&app.RunNight{}, "idx_run_night_run") {
		t.Error("the old project and night index stays or the run key's index is missing")
	}
	dup := app.RunNight{ID: 100, ProjectGUID: garlicStale, Night: night1021, Kind: app.RunNightEclipse, SourceID: 27822,
		First: at(1, 34), Last: at(8, 18), Subs: 169}
	if err := db.Create(&dup).Error; err == nil {
		t.Error("the run key lets a second row of the same run in")
	}
	if err := mergeRunNights(db); err != nil {
		t.Errorf("a second start: %v", err)
	}
}
