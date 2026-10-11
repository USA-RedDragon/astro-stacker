package mosaicplan

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

const (
	HorizonMinimumAltitude = "minimumAltitude"
	HorizonCustom          = "custom"
)

type HorizonSource func(ctx context.Context) (mosaics.HorizonReport, bool)

// HorizonBasis says which altitude limit the dark hours were worked out
// against. Kind is custom when the project asks for the custom horizon, unless
// the profile has none and Target Scheduler falls back to the minimum
// altitude. Known is false when Target Scheduler would use a limit the stacker
// does not have; the hours then use the flat minimum altitude and Reason says
// so, or are not worked out at all when the minimum altitude is unknown.
type HorizonBasis struct {
	Kind            string   `json:"kind"`
	MinimumAltitude *float64 `json:"minimumAltitude"`
	Offset          float64  `json:"offset"`
	Known           bool     `json:"known"`
	Reason          *string  `json:"reason"`
}

// HorizonSettings are a Target Scheduler project's altitude settings.
// MinAltitude is nil when the project row has no minimum altitude.
type HorizonSettings struct {
	ProfileID   string
	MinAltitude *float64
	UseCustom   bool
	Offset      float64
}

const (
	reasonNoMinAltitude = "Target Scheduler has no minimum altitude for this project (the database value is empty), and it cannot load a project without one, so dark hours are not worked out"
	reasonNoReport      = "the observatory scheduler has not reported its custom horizon yet (plugin older than the horizon endpoint, or the PC has not answered), so these hours use the flat minimum altitude and are not horizon-aware"
	reasonNoProfile     = "the project asks for the custom horizon, but the NINA profile has none, so Target Scheduler uses the flat minimum altitude, and so do these hours"
)

// LimitFor mirrors Target Scheduler's PlanningProject.DetermineHorizon. ok is
// false when no limit can be worked out because the minimum altitude is
// unknown.
func LimitFor(hs HorizonSettings, report *mosaics.HorizonReport) (limit mosaics.Limit, basis HorizonBasis, ok bool) {
	basis = HorizonBasis{Kind: HorizonMinimumAltitude, Offset: hs.Offset, Known: true}
	if hs.UseCustom {
		basis.Kind = HorizonCustom
	}
	if hs.MinAltitude == nil {
		basis.Known = false
		basis.Reason = reason(reasonNoMinAltitude)
		return mosaics.Limit{}, basis, false
	}
	minAlt := *hs.MinAltitude
	basis.MinimumAltitude = &minAlt
	flat := mosaics.Flat(minAlt)
	if !hs.UseCustom {
		return flat, basis, true
	}
	switch {
	case report == nil:
		basis.Known = false
		basis.Reason = reason(reasonNoReport)
		return flat, basis, true
	case hs.ProfileID != "" && report.ProfileID != "" && report.ProfileID != hs.ProfileID:
		basis.Known = false
		basis.Reason = reason(fmt.Sprintf("the observatory scheduler reported the custom horizon of NINA profile %s, not this project's profile %s, so these hours use the flat minimum altitude and are not horizon-aware", report.ProfileID, hs.ProfileID))
		return flat, basis, true
	case !report.Custom || report.Horizon == nil:
		basis.Kind = HorizonMinimumAltitude
		basis.Reason = reason(reasonNoProfile)
		return flat, basis, true
	}
	return mosaics.Custom(report.Horizon, hs.Offset, minAlt), basis, true
}

func (s *Service) horizonReport(ctx context.Context) *mosaics.HorizonReport {
	if s.Horizon == nil {
		return nil
	}
	r, ok := s.Horizon(ctx)
	if !ok {
		return nil
	}
	return &r
}

func (s *Service) limitFor(ctx context.Context, hs HorizonSettings) (mosaics.Limit, HorizonBasis, bool) {
	return LimitFor(hs, s.horizonReport(ctx))
}
