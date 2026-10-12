package skyevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// AutoPlanEvery is how often the stacker looks for rare events to plan
	// between searches.
	AutoPlanEvery = 15 * time.Minute
	// AutoRetry is how long the stacker waits after a failed automatic plan
	// before trying that event again.
	AutoRetry = 6 * time.Hour
	// AutoAuthor is the author of the commands of automatic plans.
	AutoAuthor = "astro-stacker (rare sky event)"
	// autoInFlight is how long a claim without a command counts as a plan
	// in progress rather than a failed one.
	autoInFlight   = 10 * time.Minute
	releaseTimeout = 10 * time.Second
	colWindowStart = "window_start"
	colWindowEnd   = "window_end"
)

func ptr[T any](v T) *T { return &v }

// autoState says what the stacker did, or will do, about planning the event
// by itself.
func autoState(e Event, c *app.SkyEventChoice, pl Planning, now time.Time) *AutoPlan {
	if c != nil && c.AutoDeclinedAt != nil && (e.IsRare() || c.AutoPlannedAt != nil) {
		return &AutoPlan{State: AutoDeclined, At: ptr(*c.AutoDeclinedAt),
			Reason: ptr("You unplanned this event or set it to normal priority, so the stacker does not plan it by itself.")}
	}
	if c != nil && c.AutoPlannedAt != nil {
		at := *c.AutoPlannedAt
		switch {
		case c.AutoCommandID != nil:
			return &AutoPlan{State: AutoPlanned, At: &at}
		case now.Sub(at) < autoInFlight:
			return &AutoPlan{State: AutoWaiting, At: &at, Reason: ptr("Being planned now.")}
		}
		return &AutoPlan{State: AutoFailed, At: &at,
			Reason: ptr("The stacker started planning this event at " + at.UTC().Format("2006-01-02 15:04") + " UTC but recorded no command, so it will not try again. Plan it by hand if it is not in the scheduler.")}
	}
	if !e.IsRare() {
		return nil
	}
	if c != nil && c.PlannedAt != nil {
		return &AutoPlan{State: AutoSkipped, Reason: ptr("Planned by hand.")}
	}
	if !e.End.After(now) {
		return nil
	}
	switch {
	case !pl.Available && pl.Reason != nil:
		return &AutoPlan{State: AutoWaiting, Reason: ptr("Planned at high priority once the scheduler takes event windows. " + *pl.Reason)}
	case c != nil && c.AutoError != nil:
		next := ""
		if c.AutoFailedAt != nil {
			next = " It is tried again after " + c.AutoFailedAt.Add(AutoRetry).UTC().Format("2006-01-02 15:04") + " UTC."
		}
		return &AutoPlan{State: AutoWaiting, At: c.AutoFailedAt, Reason: ptr("The last automatic plan failed: " + *c.AutoError + "." + next)}
	}
	return &AutoPlan{State: AutoWaiting, Reason: ptr(fmt.Sprintf("Planned at high priority at the next check (every %d minutes).", int(AutoPlanEvery.Minutes())))}
}

// turnedDown reports whether any stored choice for the event rules out an
// automatic plan: planned before, claimed before or turned down.
func turnedDown(cs []app.SkyEventChoice, e Event) bool {
	for _, c := range cs {
		if matches(c, e) && (c.AutoPlannedAt != nil || c.AutoDeclinedAt != nil || c.PlannedAt != nil) {
			return true
		}
	}
	return false
}

// claim marks a rare event as being planned by the stacker. Only an event
// never planned, never turned down and never claimed can be claimed; the
// update is conditional, so two stackers cannot both claim it, and it
// stores the event window so later searches still find the claim.
func (s *Service) claim(ctx context.Context, e Event) (string, bool, error) {
	cs, err := s.choices(ctx)
	if err != nil {
		return "", false, err
	}
	if turnedDown(cs, e) {
		return "", false, nil
	}
	c, found := matchOwnChoice(cs, e)
	now := s.now().UTC()
	if found && c.AutoFailedAt != nil && now.Sub(*c.AutoFailedAt) < AutoRetry {
		return "", false, nil
	}
	db := s.App.WithContext(ctx)
	if !found {
		c = newChoice(e)
		c.UpdatedAt = now
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&c).Error; err != nil {
			return "", false, err
		}
	}
	r := db.Model(&app.SkyEventChoice{}).
		Where("event_id = ? AND auto_planned_at IS NULL AND auto_declined_at IS NULL AND planned_at IS NULL AND (auto_failed_at IS NULL OR auto_failed_at < ?)",
			c.EventID, now.Add(-AutoRetry)).
		Updates(map[string]any{"auto_planned_at": now, "auto_error": nil, colWindowStart: e.Start, colWindowEnd: e.End})
	if r.Error != nil {
		return "", false, r.Error
	}
	return c.EventID, r.RowsAffected == 1, nil
}

// release gives a claim back when no command was sent, keeping the reason
// and the time, so the event is tried again after AutoRetry.
func (s *Service) release(ctx context.Context, id string, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	return s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ? AND auto_command_id IS NULL", id).
		Updates(map[string]any{"auto_planned_at": nil, "auto_error": cause.Error(), "auto_failed_at": s.now().UTC()}).Error
}

// backfillDeclines treats choices saved before automatic planning existed,
// where the user left or set an unplanned event at normal priority, as
// turned down. Only those rows lack a window.
func (s *Service) backfillDeclines(ctx context.Context) error {
	return s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).
		Where("priority = ? AND planned_at IS NULL AND window_start IS NULL AND auto_declined_at IS NULL AND auto_planned_at IS NULL AND auto_error IS NULL", PriorityNormal).
		Update("auto_declined_at", gorm.Expr("updated_at")).Error
}

// autoCommandID is the project.create id for a claim, the same on every
// retry, so a retry after a lost answer finds the command it already sent.
func autoCommandID(claim string) string {
	return uuidFrom("sky-event-auto:" + claim)
}

// AutoPlanRare plans every rare event still to come at high priority, the
// way "Plan it" does, once the scheduler takes event windows. An event is
// planned this way at most once, and never after the user planned it,
// unplanned it or set it to normal priority.
func (s *Service) AutoPlanRare(ctx context.Context) error {
	if s.App == nil || s.Snapshot == nil || s.Commands == nil {
		return nil
	}
	if has, err := tables.Has(ctx, s.App, &app.SkyEventChoice{}); err != nil || !has {
		return err
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if !s.backfilled {
		if err := s.backfillDeclines(ctx); err != nil {
			return fmt.Errorf("mark earlier normal-priority choices as turned down: %w", err)
		}
		s.backfilled = true
	}
	res := s.Latest()
	if res == nil || res.Status != StatusReady {
		return nil
	}
	now := s.now()
	var rare []Event
	for _, e := range res.Events {
		if e.IsRare() && e.End.After(now) {
			rare = append(rare, e)
		}
	}
	if len(rare) == 0 || !s.Planning(ctx).Available {
		return nil
	}
	var snap *planning.Snapshot
	var errs []error
	for _, e := range rare {
		id, ok, err := s.claim(ctx, e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.ID, err))
			continue
		}
		if !ok {
			continue
		}
		if snap == nil {
			if snap, err = s.Snapshot(ctx); err != nil {
				return errors.Join(append(errs, err, s.release(ctx, id, err))...)
			}
		}
		if err := s.autoPlan(ctx, snap, e, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) autoPlan(ctx context.Context, snap *planning.Snapshot, e Event, id string) error {
	var c app.SkyEventChoice
	if err := s.App.WithContext(ctx).Limit(1).Find(&c, "event_id = ?", id).Error; err != nil {
		return errors.Join(err, s.release(ctx, id, err))
	}
	_, rec, err := s.plan(ctx, snap, e, c, PriorityHigh, AutoAuthor, autoCommandID(id))
	if rec.ID == "" {
		if err == nil {
			err = errors.New("no command was recorded")
		}
		return errors.Join(err, s.release(ctx, id, err))
	}
	fields := map[string]any{"auto_command_id": rec.ID}
	if err != nil {
		fields["auto_error"] = err.Error()
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if uerr := s.App.WithContext(uctx).Model(&app.SkyEventChoice{}).
		Where("event_id IN ? AND auto_planned_at IS NOT NULL AND auto_command_id IS NULL", []string{id, e.ID}).Updates(fields).Error; uerr != nil {
		err = errors.Join(err, uerr)
	}
	if err != nil {
		slog.Warn("Sent the automatic plan of a rare sky event, but recording it failed", "event", e.ID, "command", rec.ID, "error", err)
		return err
	}
	var interval any
	if e.Rarity != nil && e.Rarity.IntervalYears != nil {
		interval = *e.Rarity.IntervalYears
	}
	slog.Info("Planned a rare sky event automatically", "event", e.ID, "title", e.Title, "first_night", e.FirstNight, "interval_years", interval,
		"priority", PriorityHigh, "command", rec.ID, "status", rec.Status)
	return nil
}
