package cmd

import (
	"context"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryreport"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

// newReports drafts the report pages of finds; the forced photometry on
// earlier nights runs on the night comparisons' runner when it is on.
func newReports(ctx context.Context, cfg *config.Config, appStore store.Store, nights discoveryHandle) *discoveryreport.Service {
	d := cfg.Discoveries
	svc := &discoveryreport.Service{DB: appStore.DB(), Cutouts: newCutoutStore(cfg), Instrument: discoveryreport.InstrumentFromFrames(appStore.DB()),
		TNS:      tnsReports(d),
		Settings: discoveryreport.Settings{Reporter: d.TNSReporter, TNSGroupID: d.TNSGroupID, TNSInstrumentID: d.TNSInstrumentID, MPCCode: d.MPCCode, TNSSite: tnsSite(d)}}
	if nights.tns != nil {
		svc.TNSIndex = nights.tns.Index
	}
	if m, ok := nights.source.(discoveryreport.SpotMeasurer); ok {
		svc.Measurer = m
	}
	cat := tnsCatalogs(d)
	if cat.TNSEnabled() {
		svc.Search = func(ctx context.Context, ra, dec, radius float64) ([]string, error) {
			as, err := cat.TNS(ctx, ra, dec, radius)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(as))
			for _, a := range as {
				out = append(out, a.ID)
			}
			return out, nil
		}
	}
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	svc.Resume(rctx)
	return svc
}
