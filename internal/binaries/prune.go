package binaries

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// prune takes away what was worked out for stars that are no longer
// eclipsing binaries: their facts and rendered files, their eclipse
// timings, and their open imaging requests, which are cancelled and owe
// the scheduler a withdraw. binaries are the current eclipsing binaries.
func (s *Service) prune(ctx context.Context, binaries []Star) error {
	keep := make([]int, 0, len(binaries))
	for _, st := range binaries {
		keep = append(keep, st.Source.ID)
	}
	notKept := func(q *gorm.DB) *gorm.DB {
		if len(keep) == 0 {
			return q.Where("source_id IS NOT NULL")
		}
		return q.Where("source_id NOT IN ?", keep)
	}
	db := s.DB.WithContext(ctx)
	var facts []app.BinaryFacts
	if err := notKept(db.Select("source_id")).Find(&facts).Error; err != nil {
		return err
	}
	var errs []error
	for _, f := range facts {
		if s.Objects != nil {
			if err := s.Objects.RemovePrefix(ctx, fmt.Sprintf("binaries/%d/", f.SourceID)); err != nil {
				errs = append(errs, fmt.Errorf("files of source %d: %w", f.SourceID, err))
				continue
			}
		}
		if err := db.Where("source_id = ?", f.SourceID).Delete(&app.BinaryFacts{}).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		slog.Info("Took away the binary facts of a star that is not an eclipsing binary", "source", f.SourceID)
	}
	if err := notKept(db).Delete(&app.EclipseTiming{}).Error; err != nil {
		errs = append(errs, err)
	}
	var open []app.EclipseRequest
	if err := notKept(db.Where("state IN ? AND kind <> ?", []string{app.EclipseWaiting, app.EclipseSent}, KindDip)).Find(&open).Error; err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, r := range open {
		if _, err := s.CancelRequest(ctx, r.ID); err != nil {
			errs = append(errs, fmt.Errorf("request %d: %w", r.ID, err))
			continue
		}
		slog.Info("Cancelled an eclipse request of a star that is not an eclipsing binary", "source", r.SourceID, "request", r.ID)
	}
	return errors.Join(errs...)
}
