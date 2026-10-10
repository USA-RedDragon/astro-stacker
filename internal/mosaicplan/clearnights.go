package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	minProjectNights = 5
	minHistoryNights = 5
)

type MonthNights struct {
	Month  int      `json:"month"`
	Name   string   `json:"name"`
	Nights *float64 `json:"nights"`
	Years  int      `json:"years"`
}

type nightHistory struct {
	hours map[time.Time]float64
	first time.Time
	last  time.Time
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Service) clearNights(ctx context.Context) (nightHistory, error) {
	h := nightHistory{hours: map[time.Time]float64{}}
	var rows []struct {
		Night     time.Time
		Effective float64
	}
	if err := s.App.WithContext(ctx).Table("stack_frames sf").
		Select("f.night AS night, COALESCE(SUM(sf.score*sf.exposure),0) AS effective").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND f.night IS NOT NULL", app.StackStatusAdded).
		Group("f.night").Scan(&rows).Error; err != nil {
		return h, fmt.Errorf("load clear nights: %w", err)
	}
	for _, r := range rows {
		d := dayOf(r.Night)
		h.hours[d] += r.Effective / 3600
		if h.first.IsZero() || d.Before(h.first) {
			h.first = d
		}
		if d.After(h.last) {
			h.last = d
		}
	}
	return h, nil
}

func (h nightHistory) perMonth() []MonthNights {
	var counts, years [12]int
	for d := range h.hours {
		counts[int(d.Month())-1]++
	}
	if !h.first.IsZero() {
		for m := time.Date(h.first.Year(), h.first.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(h.last); m = m.AddDate(0, 1, 0) {
			years[int(m.Month())-1]++
		}
	}
	out := make([]MonthNights, 12)
	for i := range 12 {
		out[i] = MonthNights{Month: i + 1, Name: time.Month(i + 1).String()[:3], Years: years[i]}
		if years[i] > 0 {
			v := math.Round(float64(counts[i])/float64(years[i])*10) / 10
			out[i].Nights = &v
		}
	}
	return out
}

func (h nightHistory) hoursPerClearNight() *float64 {
	if len(h.hours) < minHistoryNights {
		return nil
	}
	var sum float64
	for _, v := range h.hours {
		sum += v
	}
	v := sum / float64(len(h.hours))
	return &v
}

func (h nightHistory) span() (from, to *time.Time) {
	if h.first.IsZero() {
		return nil, nil
	}
	f, t := h.first, h.last
	return &f, &t
}

func clearNightsIn(months []MonthNights, usable []int) *float64 {
	if len(usable) == 0 {
		return nil
	}
	var sum float64
	for _, m := range usable {
		n := months[m-1].Nights
		if n == nil {
			return nil
		}
		sum += *n
	}
	return &sum
}

func monthsWithoutHistory(months []MonthNights, usable []int) []string {
	var out []string
	for _, m := range usable {
		if months[m-1].Nights == nil {
			out = append(out, months[m-1].Name)
		}
	}
	return out
}

func usableMonths(months [12]float64) []int {
	var out []int
	for i, h := range months {
		if h >= usableMonthly {
			out = append(out, i+1)
		}
	}
	return out
}

func (s *Service) medianTargetHours(ctx context.Context) (*float64, int, error) {
	var rows []struct {
		Object string
		Hours  float64
	}
	if err := s.App.WithContext(ctx).Model(&app.Stack{}).Select("object, SUM(effective_seconds)/3600.0 AS hours").
		Where("subs > 0").Group("object").Having("SUM(effective_seconds) >= ?", 3600).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}
	hs := make([]float64, len(rows))
	for i, r := range rows {
		hs[i] = r.Hours
	}
	slices.Sort(hs)
	v := hs[len(hs)/2]
	if len(hs)%2 == 0 {
		v = (hs[len(hs)/2-1] + hs[len(hs)/2]) / 2
	}
	v = math.Round(v*10) / 10
	return &v, len(rows), nil
}
