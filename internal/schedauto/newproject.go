package schedauto

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

// NewProject gives a project.create payload of a project on profileID the
// global settings and the automatic minimum time and minimum altitude, as
// the automatic run would, so a new project does not wait for the next
// run. With the automatic run off it changes nothing.
func (s *Service) NewProject(ctx context.Context, profileID string, p *schedcmd.ProjectCreatePayload) error {
	if p == nil || s.mode() == ModeOff {
		return nil
	}
	if s.Site == nil {
		return ErrNoSite
	}
	if _, err := s.Site(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	res, err := s.Current(ctx)
	if err != nil {
		return err
	}
	hz, _ := s.horizon(ctx)
	horizon, use := Inputs{Horizon: hz}.horizonFor(planning.Project{ProfileID: profileID})
	alt := MinAltitudeFor(horizon != nil)
	g := res.Global
	np := &p.Project
	np.DitherEvery, np.FilterSwitchFrequency, np.FlatsHandling = ptr(g.DitherEvery), ptr(g.FilterSwitchFrequency), ptr(g.FlatsHandling)
	np.SmartExposureOrder, np.MeridianWindow, np.EnableGrader = ptr(g.SmartExposureOrder), ptr(g.MeridianWindow), ptr(g.EnableGrader)
	np.HorizonOffset = ptr(0.0)
	if use != nil {
		np.UseCustomHorizon = use
	}
	np.MinimumAltitude = alt.Degrees
	np.MaximumAltitude = ptr(g.MaximumAltitude)
	if g.MaximumAltitude > 0 && g.MaximumAltitude <= alt.Degrees {
		np.MaximumAltitude = ptr(0.0)
	}
	np.MinimumTime = res.SwitchFloor
	return nil
}
