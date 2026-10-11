package darkneed_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPlannedLengthsJoinTheBacklog(t *testing.T) {
	t.Parallel()
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db, sched := open(), open()
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationMaster{}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Exec(darkneed.Schema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clean := "clean"
	for i := range 25 {
		at := now.Add(-time.Duration(i) * time.Hour)
		if err := db.Create(&app.Frame{Key: fmt.Sprintf("DARK/d%d.xisf", i), Type: "DARK", Exposure: fp(600), Gain: fp(0), Offset: fp(50), SetTemp: fp(-10),
			BinX: fp(1), CalCheck: &clean, DateObs: &at, ETag: "e", Size: 1, LastModified: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &darkneed.Publisher{App: db, Sched: sched, Mode: darkneed.PublishOn, Now: func() time.Time { return now },
		Planned: func(context.Context) []darkneed.Planned {
			return []darkneed.Planned{
				{Exposure: 300, Gain: 100, Offset: 50, SetTemp: -10, Basis: "proposed by the sub length model"},
				{Exposure: 600, Gain: 0, Offset: 50, SetTemp: -10, Basis: "already has a full set"},
				{Exposure: 180, Gain: 0, Offset: 50, SetTemp: math.NaN()},
			}
		}}
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []darkneed.Row
	if err := sched.Table(darkneed.Table).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows %+v, want only the 300 s gain 100 set", rows)
	}
	r := rows[0]
	if r.ComboKey != "300|100|50|1|-10" || r.FramesNeeded != 25 || r.FramesHave != 0 || r.LightsBlocked != 0 || r.Basis != "proposed by the sub length model" {
		t.Errorf("row %+v", r)
	}
}
