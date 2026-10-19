package binaries

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedsettle"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// minMoveSlack is the least a request's window may miss the eclipse's new
// window by, at either end, before the request is drafted again.
const minMoveSlack = 10 * time.Minute

// movedOut says the eclipse's new window reaches past the requested window
// by more than the slack at either end: the larger of minMoveSlack and a
// tenth of the new window. A window that still holds the whole new window,
// or misses only its outer tenth, still images the dip and its sides, so
// small changes of the ephemeris leave the request alone.
func movedOut(r app.EclipseRequest, p Predicted) bool {
	start, end := p.Window()
	slack := max(minMoveSlack, end.Sub(start)/10)
	return r.Start.Sub(start) > slack || end.Sub(r.End) > slack
}

// nearestPrediction is the predicted eclipse of r's kind nearest r's
// middle, within half a period of it, or one of the other kind within
// relabelMatch of it: when the vet takes one dip per cycle as two equal
// eclipses at twice the period, or two equal eclipses swap which is
// deeper, the eclipse times stay and only the kinds change, which must not
// draft a request again.
func nearestPrediction(st Star, r app.EclipseRequest) (Predicted, bool) {
	e, _ := st.ephemeris()
	if e == nil || e.Period <= 0 || st.Facts == nil || st.Facts.Gaia == nil {
		return Predicted{}, false
	}
	half := time.Duration(e.Period / 2 * 24 * float64(time.Hour))
	ra, dec := st.Facts.Gaia.Now(r.Mid)
	var best *Predicted
	for _, p := range Predict(e, st.deeper(), ra, dec, r.Mid.Add(-half), r.Mid.Add(half)) {
		if p.Kind != r.Kind && absDur(p.Mid.Sub(r.Mid)) > relabelMatch {
			continue
		}
		if best == nil || absDur(p.Mid.Sub(r.Mid)) < absDur(best.Mid.Sub(r.Mid)) {
			p = st.withPhaseError(p, e)
			best = &p
		}
	}
	if best == nil {
		return Predicted{}, false
	}
	return *best, true
}

// redraftMoved withdraws each open request whose window has not begun and
// whose eclipse the current ephemeris moves out of that window, and asks
// for the eclipse again at its new time: the user's own requests here, the
// app's own through autoRequests, which picks the season's next eclipse.
func (s *Service) redraftMoved(ctx context.Context, stars []Star) error {
	if s.Commands == nil {
		return nil
	}
	byID := map[int]Star{}
	for _, st := range stars {
		byID[st.Source.ID] = st
	}
	var reqs []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("state IN ?", []string{app.EclipseWaiting, app.EclipseSent}).Find(&reqs).Error; err != nil {
		return err
	}
	now := s.now()
	var errs []error
	for _, r := range reqs {
		st, ok := byID[r.SourceID]
		if !ok || !r.Start.After(now) {
			continue
		}
		p, ok := nearestPrediction(st, r)
		if !ok || !movedOut(r, p) {
			continue
		}
		r.State, r.UpdatedAt = app.EclipseMoved, now
		if r.CreateCmd != "" {
			r.Settle = schedsettle.Withdraw
		}
		r.Note = fmt.Sprintf("Newer survey data moved this eclipse to %s, so this request was withdrawn and the eclipse asked for again.",
			p.Mid.In(s.loc()).Format("Jan 2 3:04 PM MST"))
		if err := s.DB.WithContext(ctx).Save(&r).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.settle(ctx, r.ID); err != nil {
			errs = append(errs, err)
		}
		if r.Auto {
			continue
		}
		if _, err := s.requestImaging(ctx, r.SourceID, p.Kind, p.Mid, false); err != nil && !errors.Is(err, ErrOver) {
			errs = append(errs, fmt.Errorf("source %d: %w", r.SourceID, err))
		}
	}
	return errors.Join(errs...)
}
