package mosaicplan

import (
	"context"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

type History struct {
	Nights []string       `json:"nights"`
	Panels []PanelHistory `json:"panels"`
}

type PanelHistory struct {
	Number int       `json:"number"`
	Hours  []float64 `json:"hours"`
}

func (s *Service) History(ctx context.Context, key string) (History, error) {
	d, err := s.Detail(ctx, key)
	if err != nil {
		return History{}, err
	}
	out := History{Nights: []string{}, Panels: []PanelHistory{}}
	panelOf := map[string]int{}
	var objects []string
	for i, p := range d.Panels {
		for _, o := range p.Objects {
			if _, ok := panelOf[o]; !ok {
				panelOf[o] = i
				objects = append(objects, o)
			}
		}
		out.Panels = append(out.Panels, PanelHistory{Number: p.Number})
	}
	if len(objects) == 0 {
		return out, nil
	}
	var rows []struct {
		Night     time.Time
		Object    string
		Effective float64
	}
	if err := s.App.WithContext(ctx).Table("stack_frames sf").
		Select("f.night AS night, f.object AS object, COALESCE(SUM(sf.score*sf.exposure),0) AS effective").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND f.object IN ? AND f.night IS NOT NULL", app.StackStatusAdded, objects).
		Group("f.night, f.object").Order("f.night").Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("load nights: %w", err)
	}
	index := map[string]int{}
	for _, r := range rows {
		n := r.Night.UTC().Format(time.DateOnly)
		if _, ok := index[n]; !ok {
			index[n] = len(out.Nights)
			out.Nights = append(out.Nights, n)
		}
	}
	for i := range out.Panels {
		out.Panels[i].Hours = make([]float64, len(out.Nights))
	}
	for _, r := range rows {
		out.Panels[panelOf[r.Object]].Hours[index[r.Night.UTC().Format(time.DateOnly)]] += r.Effective / 3600
	}
	for i := range out.Panels {
		h := out.Panels[i].Hours
		for k := 1; k < len(h); k++ {
			h[k] += h[k-1]
		}
	}
	return out, nil
}
