package vetting

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

// lineRefute is how far, in its errors, a filter must fall under the
// light of every colour it would show for the light to be line emission
// alone. Each filter's zero point comes from the same Gaia stars, so a
// source with a star's colours has the same magnitude in every filter:
// 41445 in the Rosette Nebula, H-a light of G-scale magnitude 17.35 on
// 2025-02-28, would have shown at 0.0055 in the night's S-II frames, at
// signal to noise 10, where they measured -0.0003 ± 0.00055.
const lineRefute = 3.0

// lineLight is a narrowband change's light against the same night's
// other filters: only says no filter deep enough to show light of every
// colour at seenSNR showed it, and the check says so.
type lineLight struct {
	check check.Check
	only  bool
	night ownimage.FluxNight
}

// emissionCheck weighs a change found in one narrowband filter against the
// same night's other filters.
func emissionCheck(fc *ownimage.FluxCurve, groups []string) lineLight {
	for _, g := range groups {
		if g == discoveries.GroupBroadband {
			return lineLight{check: check.Skipped(check.TransLine, "Not needed: it was found in broadband frames.")}
		}
	}
	if fc == nil {
		return lineLight{check: check.Skipped(check.TransLine, "Not measured yet. The spot's own light could not be measured.")}
	}
	p, ok := peakOf(fc, func(n ownimage.FluxNight) bool {
		return n.Filter != ownimage.AllFilters && discoveries.GroupOf(n.Filter) != discoveries.GroupBroadband && n.Mag > 0
	})
	if !ok || p.SNR() < faintSNR {
		return lineLight{check: check.Skipped(check.TransLine, "Not measured: no flagged night shows clear light in a narrowband filter.")}
	}
	var deep, showed, refuted []string
	for _, n := range fc.Nights {
		zp, ok := fc.Zero[n.Filter]
		if n.Night != p.Night || n.Filter == p.Filter || n.Filter == ownimage.AllFilters || n.Thin || !ok || !(n.Err > 0) {
			continue
		}
		want := math.Pow(10, -0.4*(p.Mag-zp))
		if want/n.Err < seenSNR {
			continue
		}
		at := fmt.Sprintf("%s (%.4f ± %.4f where it would give %.4f, signal to noise %.0f)", n.Filter, n.Flux, n.Err, want, want/n.Err)
		deep = append(deep, at)
		switch {
		case n.SNR() >= faintSNR:
			showed = append(showed, at)
		case n.Flux < want-lineRefute*n.Err && n.SNR() < noLightSNR:
			refuted = append(refuted, at)
		}
	}
	if len(deep) == 0 {
		return lineLight{check: check.Skipped(check.TransLine, fmt.Sprintf("Not judged: no other filter on %s was deep enough to show light of every colour as bright as its %s light, magnitude %.1f.", p.Night, p.Filter, p.Mag))}
	}
	if len(showed) > 0 {
		return lineLight{check: check.New(check.TransLine, check.Pass, fmt.Sprintf("Its light shows in the night's other filters too: %s.", joinWords(showed)))}
	}
	if len(refuted) == len(deep) {
		return lineLight{only: true, night: p, check: check.New(check.TransLine, check.Fail, fmt.Sprintf(
			"Line emission only: on %s the light was in %s alone. Light of every colour as bright as its %s light, magnitude %.1f, would show in the night's other frames, but they show nothing at the spot: %s. Broadband surveys such as ATLAS and ZTF cannot confirm light like this.",
			p.Night, p.Filter, p.Filter, p.Mag, joinWords(refuted)))}
	}
	return lineLight{check: check.Skipped(check.TransLine, fmt.Sprintf("Not clear: the night's other filters neither show it nor rule it out: %s.", joinWords(deep)))}
}

// fieldShare is how bright, as a share of a change's signal to noise,
// other changes in the same frames must be to count against it: light in
// one narrowband filter alone stands out only when no other spot of the
// field without a star changed that night by fieldShare of it or more.
// In the H-a frames of 41445's 2025-02-28, found at 9.3, three other such
// spots changed by 7.6 to 9.0, and the audit found it at about the
// largest noise peak a field that size gives.
const fieldShare = 0.8

// fieldRivals are the other changes of the same run, filter group and
// sign, with no Gaia star and in half or more of their subs, at fieldShare
// of the candidate's signal to noise or more.
type fieldRivals struct {
	asked bool
	err   error
	snr   float64
	group string
	night string
	n     int
	best  float64
}

// lineRivals counts the field's rivals of the source's strongest candidate
// in the line-only night's filter.
func (s *Service) lineRivals(ctx context.Context, members []app.DiscoveryCandidate, l lineLight) fieldRivals {
	var own *app.DiscoveryCandidate
	for i, m := range members {
		if m.Group != l.night.Filter || m.Night.UTC().Format("2006-01-02") != l.night.Night || m.Sign <= 0 {
			continue
		}
		if own == nil || m.SNR > own.SNR {
			own = &members[i]
		}
	}
	if own == nil || s.App == nil {
		return fieldRivals{}
	}
	r := fieldRivals{asked: true, snr: own.SNR, group: own.Group, night: l.night.Night}
	var rows []app.DiscoveryCandidate
	r.err = s.App.WithContext(ctx).Select("id", "snr").
		Where("run_id = ? AND filter_group = ? AND kind = ? AND sign > 0 AND gaia_g IS NULL AND id <> ? AND snr >= ? AND present_in * 2 >= subs_tested",
			own.RunID, own.Group, app.DiscoveryKindChange, own.ID, fieldShare*own.SNR).
		Where("source_id IS NULL OR source_id <> ?", own.SourceID).
		Find(&rows).Error
	for _, c := range rows {
		r.n++
		r.best = math.Max(r.best, c.SNR)
	}
	return r
}

// fieldCheck says whether line-only light stands out from the field's
// other changes of the same night and filter.
func fieldCheck(r fieldRivals) check.Check {
	switch {
	case !r.asked:
		return check.Skipped(check.TransField, "Not measured: the detection's other changes of that night could not be read.")
	case r.err != nil:
		return check.Skipped(check.TransField, sentence("Not measured: "+strings.TrimSpace(r.err.Error())))
	case r.n > 0:
		return check.New(check.TransField, check.Fail, fmt.Sprintf("Found at signal to noise %.1f, but %d other spots of the same %s frames of %s with no star changed by %.1f or more (up to %.1f), so it is no brighter than the field's noise peaks.",
			r.snr, r.n, r.group, r.night, fieldShare*r.snr, r.best))
	}
	return check.New(check.TransField, check.Pass, fmt.Sprintf("Found at signal to noise %.1f; no other spot of the same %s frames of %s with no star changed by %.1f or more.", r.snr, r.group, r.night, fieldShare*r.snr))
}

// lineSkipped is the survey checks of light that is line emission only.
func lineSkipped(id string) check.Check {
	return check.Skipped(id, "Not used: the light is line emission only, which a broadband survey does not show as new light.")
}
