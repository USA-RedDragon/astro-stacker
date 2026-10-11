package mosaicplan

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

func SiteFromLights(client *minio.Client, bucket string, db *gorm.DB) SiteSource {
	return CachedSite(func(ctx context.Context) (mosaics.Site, bool) {
		frames, err := newestLights(ctx, db)
		if err != nil {
			return mosaics.Site{}, false
		}
		for _, f := range frames {
			kw, err := indexer.ReadHeader(ctx, client, bucket, minio.ObjectInfo{Key: f.Key, Size: f.Size})
			if err != nil {
				continue
			}
			lat, lon := kw.Float("SITELAT"), kw.Float("SITELONG")
			if !math.IsNaN(lat) && !math.IsNaN(lon) {
				return mosaics.Site{Lat: lat, Lon: lon}, true
			}
		}
		return mosaics.Site{}, false
	})
}

func newestLights(ctx context.Context, db *gorm.DB) ([]app.Frame, error) {
	var frames []app.Frame
	err := db.WithContext(ctx).Select("key", "size").Where("type = ? AND index_error IS NULL", "LIGHT").
		Order("date_obs IS NULL, date_obs DESC").Limit(5).Find(&frames).Error
	return frames, err
}

func (s *Service) SiteFallback(src SiteSource) {
	if s.Site == nil {
		s.Site = src
	}
}

func (s *Service) RunAdoptionEvery(ctx context.Context, interval time.Duration) {
	for {
		rep, err := s.RunAdoption(ctx, false)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.Error("Mosaic adoption failed", "error", err)
		case err == nil && rep.New+rep.Changed+rep.Auto > 0:
			slog.Info("Mosaic adoption proposals", "new", rep.New, "changed", rep.Changed, "decided_automatically", rep.Auto, "to_review", rep.Review, "clean", rep.Clean)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
