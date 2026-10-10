package goals

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	ColumnNightQuality   = "night_quality"
	ColumnNightQualityAt = "night_quality_at"
	ColumnSeasonBoost    = "season_boost"
	NightQualityWindow   = 3 * time.Hour
	nightQualityMinSubs  = 3
	nightQualityStep     = 0.05
	seasonBoostStep      = 0.01
)

type SeasonBoostSource func(ctx context.Context, now time.Time) (map[string]float64, error)

type Signals struct {
	NightQuality   *float64
	NightQualityAt *time.Time
	Boosts         map[string]float64
}

func HasSignalColumns(sched *gorm.DB) bool {
	m := sched.Migrator()
	return m.HasColumn(ProgressTable, ColumnNightQuality) && m.HasColumn(ProgressTable, ColumnNightQualityAt) && m.HasColumn(ProgressTable, ColumnSeasonBoost)
}

func quantize(v, step float64) float64 {
	return math.Round(v/step) * step
}

func NightQuality(ctx context.Context, appDB *gorm.DB, now time.Time) (*float64, *time.Time, error) {
	var rows []scoredSub
	if err := appDB.WithContext(ctx).Table("stack_frames sf").
		Select("sf.score AS score, f.date_obs AS date_obs").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("f.type = ? AND f.date_obs IS NOT NULL AND f.date_obs > ? AND f.date_obs <= ?", "LIGHT", now.Add(-NightQualityWindow), now).
		Where("sf.status IN ?", []string{app.StackStatusAdded, app.StackStatusLowScore}).
		Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("load tonight's sub scores: %w", err)
	}
	q, at := medianQuality(rows)
	return q, at, nil
}

type scoredSub struct {
	Score   float64
	DateObs time.Time
}

func medianQuality(subs []scoredSub) (*float64, *time.Time) {
	scores := make([]float64, 0, len(subs))
	var latest time.Time
	for _, s := range subs {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) {
			continue
		}
		scores = append(scores, math.Max(0, math.Min(1, s.Score)))
		if s.DateObs.After(latest) {
			latest = s.DateObs
		}
	}
	if len(scores) < nightQualityMinSubs {
		return nil, nil
	}
	sort.Float64s(scores)
	mid := len(scores) / 2
	median := scores[mid]
	if len(scores)%2 == 0 {
		median = (scores[mid-1] + scores[mid]) / 2
	}
	q := quantize(median, nightQualityStep)
	at := latest.UTC().Truncate(time.Minute)
	return &q, &at
}

func (p *Publisher) signals(ctx context.Context) (Signals, error) {
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now()
	}
	var s Signals
	q, at, err := NightQuality(ctx, p.App, now)
	if err != nil {
		return s, err
	}
	s.NightQuality, s.NightQualityAt = q, at
	if p.SeasonBoosts != nil {
		boosts, err := p.SeasonBoosts(ctx, now)
		if err != nil {
			return s, fmt.Errorf("season boosts: %w", err)
		}
		s.Boosts = make(map[string]float64, len(boosts))
		for guid, b := range boosts {
			if math.IsNaN(b) || math.IsInf(b, 0) {
				continue
			}
			s.Boosts[guid] = quantize(math.Max(0, math.Min(1, b)), seasonBoostStep)
		}
	}
	return s, nil
}

func (s Signals) apply(r *progressRow) {
	r.NightQuality, r.NightQualityAt = s.NightQuality, s.NightQualityAt
	r.SeasonBoost = nil
	if b, ok := s.Boosts[r.TargetGUID]; ok {
		r.SeasonBoost = &b
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
