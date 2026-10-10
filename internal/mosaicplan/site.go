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
		var frames []app.Frame
		if err := db.WithContext(ctx).Select("key", "size").Where("type = ? AND index_error IS NULL", "LIGHT").
			Order("date_obs DESC").Limit(5).Find(&frames).Error; err != nil {
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

func (s *Service) RunAdoptionEvery(ctx context.Context, interval time.Duration) {
	for {
		rep, err := s.RunAdoption(ctx, false)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.Error("Mosaic adoption failed", "error", err)
		case err == nil && (rep.Auto > 0 || rep.Changed > 0):
			slog.Info("Mosaic adoption", "adopted", rep.Auto, "to_review", rep.Review, "changed", rep.Changed)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
