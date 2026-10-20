package binaries

import (
	"context"
	"errors"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrBadDipWatch says a dip watch's link is not usable.
var ErrBadDipWatch = errors.New("bad dip watch")

// SaveDipWatch links a scheduler project to the dipping star it watches,
// replacing any earlier link of the same project.
func (s *Service) SaveDipWatch(ctx context.Context, w app.DipWatch) (app.DipWatch, error) {
	switch {
	case w.ProjectGUID == "":
		return w, fmt.Errorf("%w: no project", ErrBadDipWatch)
	case !w.End.After(w.Start):
		return w, fmt.Errorf("%w: the window ends before it starts", ErrBadDipWatch)
	case w.DepthLow < 0 || w.DepthHigh < w.DepthLow:
		return w, fmt.Errorf("%w: the past depths must run from a low to a high of 0 or more", ErrBadDipWatch)
	case w.ActiveShare < 0 || w.ActiveShare > 1:
		return w, fmt.Errorf("%w: the share of windows with a dip must be from 0 to 1", ErrBadDipWatch)
	}
	db := s.DB.WithContext(ctx)
	var src app.DiscoverySource
	if err := db.First(&src, w.SourceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return w, fmt.Errorf("%w: no source %d", ErrBadDipWatch, w.SourceID)
	} else if err != nil {
		return w, err
	}
	w.ID = 0
	err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_guid"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_id", "start", "end", "depth_low", "depth_high", "active_share", "updated_at"})}).Create(&w).Error
	if err != nil {
		return w, err
	}
	s.liveMu.Lock()
	delete(s.liveMiss, w.ProjectGUID)
	s.liveMu.Unlock()
	s.liveDebouncer().Kick("dip-watch")
	return w, db.Where("project_guid = ?", w.ProjectGUID).First(&w).Error
}

// DipWatches are the linked dip watch projects, newest window first.
func (s *Service) DipWatches(ctx context.Context) ([]app.DipWatch, error) {
	out := []app.DipWatch{}
	return out, s.DB.WithContext(ctx).Order("start DESC").Find(&out).Error
}
