package stacking

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// bodyPositions places moving-body targets, whose stored position is not
// where the telescope pointed, for the moon floor.
type bodyPositions struct {
	sched *gorm.DB
	guids map[string]string
	mu    sync.Mutex
	eph   map[string]ephemeris
}

func newBodyPositions(ctx context.Context, sched *gorm.DB) *bodyPositions {
	b := &bodyPositions{sched: sched, guids: map[string]string{}, eph: map[string]ephemeris{}}
	if sched == nil {
		return b
	}
	targets, err := bodies.TargetsByName(ctx, sched)
	if err != nil {
		slog.Warn("Could not read the moving-body targets for the moon floor", "error", err)
		return b
	}
	for name, t := range targets {
		b.guids[name] = t.GUID
	}
	return b
}

func (b *bodyPositions) moving(object string) bool {
	return b != nil && b.guids[object] != ""
}

// at is where the light pointed: the header's mount position, else the
// body's ts_body_position track at mid.
func (b *bodyPositions) at(ctx context.Context, f app.Frame, mid time.Time) ([2]float64, bool) {
	if f.MountRA != nil && f.MountDec != nil {
		return [2]float64{*f.MountRA, *f.MountDec}, true
	}
	eph, ok := b.track(ctx, b.guids[f.Object])
	if !ok {
		return [2]float64{}, false
	}
	ra, dec, ok := eph.at(mid)
	return [2]float64{ra, dec}, ok
}

func (b *bodyPositions) track(ctx context.Context, guid string) (ephemeris, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.eph[guid]; ok {
		return e, len(e.times) > 1
	}
	var rows []struct {
		At  time.Time `gorm:"column:at"`
		RA  float64   `gorm:"column:ra"`
		Dec float64   `gorm:"column:dec"`
	}
	var e ephemeris
	if err := b.sched.WithContext(ctx).Table(bodies.PositionTable).Select("at, ra, dec").
		Where("target_guid = ?", guid).Order("at").Scan(&rows).Error; err != nil {
		slog.Warn("Could not read a moving body's positions for the moon floor", "target", guid, "error", err)
		b.eph[guid] = e
		return e, false
	}
	for _, r := range rows {
		e.times, e.ra, e.dec = append(e.times, r.At), append(e.ra, r.RA), append(e.dec, r.Dec)
	}
	b.eph[guid] = e
	return e, len(e.times) > 1
}
