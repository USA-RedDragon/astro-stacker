package cmd

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

func newMosaicPlans(cfg *config.Config, appStore, schedStore store.Store) *mosaicplan.Service {
	svc := mosaicplan.New(appStore.DB(), schedStore.DB())
	if d := cfg.Discover; d.SiteLatitude != 0 || d.SiteLongitude != 0 {
		site := mosaics.Site{Lat: d.SiteLatitude, Lon: d.SiteLongitude}
		svc.Site = func(context.Context) (mosaics.Site, bool) { return site, true }
	}
	return svc
}
