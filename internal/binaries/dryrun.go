package binaries

import (
	"context"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
)

// DryStar is one confirmed star's facts and flags as a dry run prints
// them: everything each flag is based on.
type DryStar struct {
	SourceID   int                 `json:"source_id"`
	Name       string              `json:"name"`
	Field      string              `json:"field"`
	Error      string              `json:"error,omitempty"`
	Period     string              `json:"period"`
	Doubled    bool                `json:"doubled"`
	Type       string              `json:"type"`
	Deeper     int                 `json:"deeper"`
	SurveyFrom string              `json:"survey_from"`
	Coverage   []eclfacts.Coverage `json:"coverage"`
	Twin       *eclfacts.Twin      `json:"twin,omitempty"`
	Secondary  *eclfacts.Limit     `json:"secondary_limit,omitempty"`
	RUWE       float64             `json:"ruwe"`
	Max        KitField            `json:"max_mag"`
	Min        KitField            `json:"min_mag"`
	DoubleChk  string              `json:"double_check"`
	HalfChk    string              `json:"half_check"`
	Flags      []Flag              `json:"flags"`
	VSXNear    []Near              `json:"vsx_near,omitempty"`
	Surveys    map[string]string   `json:"surveys,omitempty"`
}

// DryRun works out the facts of the confirmed stars (ids, or all) and
// returns them with their flags, writing nothing.
func (s *Service) DryRun(ctx context.Context, ids []int) ([]DryStar, error) {
	stars, err := s.Binaries(ctx)
	if err != nil {
		return nil, err
	}
	var out []DryStar
	for _, st := range stars {
		if len(ids) > 0 && !slices.Contains(ids, st.Source.ID) {
			continue
		}
		d := DryStar{SourceID: st.Source.ID, Field: st.Source.Object}
		sd, err := s.Stars.Star(ctx, st.Source)
		if err != nil {
			d.Error = err.Error()
		}
		var near []starcat.VSXStar
		if s.VSX != nil && sd.Gaia != nil {
			near, _ = s.VSX.Cone(ctx, sd.Gaia.RA, sd.Gaia.Dec, vsxRadius/3600, nil)
		}
		f := Work(st.Report, sd, near, s.now())
		st.Facts = &f
		b := st.View(nil)
		d.Name, d.Period, d.Doubled, d.Type, d.Deeper, d.SurveyFrom = b.Name, b.PeriodText, f.Doubled, b.TypeWords, f.Deeper, f.SurveyFrom
		if f.Survey != nil {
			d.Coverage, d.Twin, d.Secondary = f.Survey.Coverage, f.Survey.Twin, f.Survey.Secondary
		}
		if f.Gaia != nil {
			d.RUWE = f.Gaia.RUWE
		}
		k := KitOf(st, "", nil, nil, nil)
		for _, kf := range k.Fields {
			switch kf.Key {
			case "max_mag":
				d.Max = kf
			case "min_mag":
				d.Min = kf
			}
		}
		if c, ok := checkOf(st.Report, "double"); ok {
			d.DoubleChk = string(c.Status) + ": " + c.Evidence
		}
		if c, ok := checkOf(st.Report, "half"); ok {
			d.HalfChk = string(c.Status) + ": " + c.Evidence
		}
		d.Flags, d.VSXNear, d.Surveys = b.Flags, f.VSXNear, f.Surveys
		out = append(out, d)
	}
	return out, nil
}
