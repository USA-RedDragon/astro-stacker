package binaries

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedsettle"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// nightStep is how often the dark time of a second eclipse's night is
// looked at.
const nightStep = 10 * time.Minute

// ErrVague is a second eclipse whose timing is too uncertain to fit a
// night.
var ErrVague = errors.New("not requested")

// nightDark is how long the site sees the star dark and high in the night
// of p: the dark span within half a day of its middle that overlaps the
// dip the most, the longest when none does.
func nightDark(site sky.Site, ra, dec float64, p Predicted) time.Duration {
	spans := darkSpansEvery(site, ra, dec, p.Mid.Add(-12*time.Hour), p.Mid.Add(12*time.Hour), nightStep)
	sp, ok := bestDark(spans, p.Mid.Add(-p.Duration/2), p.Mid.Add(p.Duration/2))
	if !ok {
		return 0
	}
	return sp.End.Sub(sp.Start)
}

// vagueWhy is why the second eclipse p is not asked for: windowSigma
// timing errors on each side and its length spread the span its dip may
// fall in past the night's dark time with the star up, though the dip
// alone fits in it, and those errors span more than the dip itself, so
// the timing, not the dip's length, is what overflows the night. It is
// empty for every other eclipse, and once the error shrinks.
func vagueWhy(site sky.Site, ra, dec float64, p Predicted) string {
	errSpan := time.Duration(2 * windowSigma * float64(p.Err))
	if p.Kind != KindSecondary || p.Err <= 0 || errSpan < p.Duration {
		return ""
	}
	spread := p.Duration + errSpan
	night := nightDark(site, ra, dec, p)
	if night <= p.Duration || spread <= night {
		return ""
	}
	return fmt.Sprintf("the second eclipse's timing is uncertain by ±%s, so its dip could fall anywhere in %s, more than the %s the star is up in the dark that night",
		spanText(p.Err), spanText(spread), spanText(night))
}

// heldNote is what the planner shows for an eclipse vagueWhy keeps from
// being asked for.
func heldNote(why string) string {
	if why == "" {
		return ""
	}
	return "Not requested: " + why + ". It comes back when its timing is measured better."
}

func spanText(d time.Duration) string {
	if d < 90*time.Minute {
		return fmt.Sprintf("%.0f min", d.Minutes())
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

// vagueOf is vagueWhy for st's eclipse p at the site, empty when the site
// is not known.
func vagueOf(site *sky.Site, st Star, p Predicted) string {
	if site == nil || st.Facts == nil || st.Facts.Gaia == nil {
		return ""
	}
	ra, dec := st.Facts.Gaia.Now(p.Mid)
	return vagueWhy(*site, ra, dec, p)
}

func (s *Service) siteOf(ctx context.Context) *sky.Site {
	if s.Site == nil {
		return nil
	}
	v, err := s.Site(ctx)
	if err != nil {
		return nil
	}
	return &v
}

// holdVague withdraws each open request whose window has not begun and
// whose second eclipse vagueWhy keeps from being asked for, through the
// scheduler's withdraw, and asks again for each of the user's held
// requests whose eclipse fits a night again and has not begun. The app's
// own come back through autoRequests.
func (s *Service) holdVague(ctx context.Context, stars []Star) error {
	site := s.siteOf(ctx)
	if s.Commands == nil || site == nil {
		return nil
	}
	byID := map[int]Star{}
	for _, st := range stars {
		byID[st.Source.ID] = st
	}
	var reqs []app.EclipseRequest
	now := s.now()
	if err := s.DB.WithContext(ctx).Where("kind = ? AND ((state IN ? AND start > ?) OR (state = ? AND auto = ? AND \"end\" > ?))", KindSecondary,
		[]string{app.EclipseWaiting, app.EclipseSent}, now, app.EclipseHeld, false, now).Find(&reqs).Error; err != nil {
		return err
	}
	var errs []error
	for _, r := range reqs {
		st, ok := byID[r.SourceID]
		if !ok {
			continue
		}
		p, ok := nearestPrediction(st, r)
		if !ok {
			continue
		}
		why := vagueOf(site, st, p)
		if r.State == app.EclipseHeld {
			if start, _ := p.Window(); why != "" || !start.After(now) {
				continue
			}
			_, err := s.requestImaging(ctx, r.SourceID, p.Kind, p.Mid, false)
			if err != nil && !errors.Is(err, ErrOver) {
				errs = append(errs, fmt.Errorf("source %d: %w", r.SourceID, err))
				continue
			}
			r.State, r.UpdatedAt = app.EclipseMoved, now
			r.Note = "Its timing is measured better now, so the eclipse was asked for again."
			if err := s.DB.WithContext(ctx).Save(&r).Error; err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if why == "" {
			continue
		}
		r.State, r.UpdatedAt = app.EclipseHeld, now
		if r.CreateCmd != "" {
			r.Settle = schedsettle.Withdraw
		}
		r.Note = "Withdrawn: " + why + ". It comes back when its timing is measured better."
		if err := s.DB.WithContext(ctx).Save(&r).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.settle(ctx, r.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
