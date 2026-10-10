package goals

import (
	"context"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	BackfillOff     = "off"
	BackfillIdle    = "idle"
	BackfillRunning = "measuring"
	BackfillPaused  = "paused-for-stacking"
)

type BackfillLive struct {
	State       string     `json:"state"`
	Workers     int        `json:"workers"`
	Queued      int        `json:"queued"`
	QueuedNew   int        `json:"queuedNew"`
	DoneInPass  int        `json:"doneInPass"`
	PassStarted *time.Time `json:"passStarted,omitempty"`
	LastHour    int        `json:"lastHour"`
	PerHour     float64    `json:"perHour"`
	ETA         *time.Time `json:"eta,omitempty"`
}

type Backfill struct {
	Total    int `json:"total"`
	Measured int `json:"measured"`
	Current  int `json:"current"`
	Failed   int `json:"failed"`
	BackfillLive
}

func CountBackfill(ctx context.Context, db *gorm.DB, live BackfillLive) (Backfill, error) {
	out := Backfill{BackfillLive: live}
	if out.State == "" {
		out.State = BackfillOff
	}
	if db == nil {
		return out, nil
	}
	var stacks []app.Stack
	if err := db.WithContext(ctx).Select("object", "filter").Where("subs > 0").Find(&stacks).Error; err != nil {
		return out, err
	}
	out.Total = len(stacks)
	if !db.Migrator().HasTable(&app.GoalMeasurement{}) {
		return out, nil
	}
	var ms []app.GoalMeasurement
	if err := db.WithContext(ctx).Select("object", "filter", "method_revision", "error").Find(&ms).Error; err != nil {
		return out, err
	}
	byKey := make(map[Key]app.GoalMeasurement, len(ms))
	for _, m := range ms {
		byKey[Key{Object: m.Object, Filter: m.Filter}] = m
	}
	for _, s := range stacks {
		m, ok := byKey[Key{Object: s.Object, Filter: s.Filter}]
		if !ok {
			continue
		}
		out.Measured++
		if m.MethodRevision == MethodRevision {
			out.Current++
		}
		if m.Error != nil {
			out.Failed++
		}
	}
	if out.State == BackfillRunning && out.PerHour > 0 && out.Queued > out.DoneInPass {
		left := float64(out.Queued-out.DoneInPass) / out.PerHour
		eta := time.Now().UTC().Add(time.Duration(left * float64(time.Hour))).Truncate(time.Minute)
		out.ETA = &eta
	}
	out.PerHour = math.Round(out.PerHour*10) / 10
	return out, nil
}
