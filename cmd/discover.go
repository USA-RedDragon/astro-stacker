package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

var errNoSiteHeader = errors.New("the newest light has no SITELAT and SITELONG")

func discoverRig(cfg *config.Config) discover.Rig {
	d := cfg.Discover
	return discover.Rig{
		Filters:     discover.ParseFilterValues(d.Filters),
		MinAltitude: d.MinAltitude,
		SkyBright:   d.SkyBrightness,
	}
}

func siteFunc(cfg *config.Config, appStore store.Store) discover.SiteFunc {
	d := cfg.Discover
	if d.SiteLatitude != 0 || d.SiteLongitude != 0 {
		site := sky.Site{Latitude: d.SiteLatitude, Longitude: d.SiteLongitude, Elevation: d.SiteElevation}
		return func(context.Context) (sky.Site, error) { return site, nil }
	}
	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return func(context.Context) (sky.Site, error) { return sky.Site{}, discover.ErrNoSite }
	}
	return func(ctx context.Context) (sky.Site, error) {
		var f app.Frame
		err := appStore.DB().WithContext(ctx).Where("type = ? AND index_error IS NULL AND date_obs IS NOT NULL", "LIGHT").
			Order("date_obs DESC").Limit(1).Find(&f).Error
		if err != nil {
			return sky.Site{}, err
		}
		if f.ID == 0 {
			return sky.Site{}, discover.ErrNoSite
		}
		s3, err := newS3(cfg)
		if err != nil {
			return sky.Site{}, err
		}
		kw, err := indexer.ReadHeader(ctx, s3, cfg.S3.Bucket, minio.ObjectInfo{Key: f.Key, Size: f.Size})
		if err != nil {
			return sky.Site{}, fmt.Errorf("read the site from %s: %w", f.Key, err)
		}
		lat, lon, elev := kw.Float("SITELAT"), kw.Float("SITELONG"), kw.Float("SITEELEV")
		if math.IsNaN(lat) || math.IsNaN(lon) {
			return sky.Site{}, fmt.Errorf("%w: %s", errNoSiteHeader, f.Key)
		}
		site := sky.Site{Latitude: lat, Longitude: lon, Elevation: d.SiteElevation}
		if site.Elevation == nil && !math.IsNaN(elev) {
			site.Elevation = &elev
		}
		slog.Info("Observatory site read from a light's header", "key", f.Key)
		return site, nil
	}
}

func newDiscover(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, backfill func() goals.BackfillLive) (*discover.Service, *starfront.Poller) {
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		slog.Error("Catalogue store failed to load; the Discover pages are off", "error", err)
		return nil, nil
	}
	svc := &discover.Service{
		Catalog:  ix,
		AppDB:    appStore.DB(),
		SchedDB:  schedStore.DB(),
		Site:     siteFunc(cfg, appStore),
		Rig:      discoverRig(cfg),
		Measure:  (&rigsource.Source{App: appStore.DB(), Sched: schedStore.DB()}).Get,
		Backfill: backfill,
	}
	sky := &skybright.Source{DB: appStore.DB(), Override: cfg.Discover.SkyBrightness, Site: func(ctx context.Context) (float64, float64, bool) {
		site, err := svc.Site(ctx)
		return site.Latitude, site.Longitude, err == nil
	}}
	svc.Sky = sky.Get
	hmap := &halpha.Provider{DB: appStore.DB(), Endpoint: cfg.Discover.HiPS2FITSURL, Off: !cfg.Discover.HAlphaMap}
	go hmap.Run(ctx)
	svc.HAlpha = hmap.Get
	slog.Info("Catalogue store loaded", "objects", ix.Len())
	if !cfg.Discover.Starfront {
		return svc, nil
	}
	poller := &starfront.Poller{
		BaseURL:  cfg.Discover.StarfrontURL,
		Interval: time.Duration(cfg.Discover.StarfrontMinutes) * time.Minute,
		DB:       appStore.DB(),
	}
	go poller.Run(ctx)
	slog.Info("Reading Starfront collaborations", "url", cfg.Discover.StarfrontURL, "minutes", cfg.Discover.StarfrontMinutes)
	return svc, poller
}
