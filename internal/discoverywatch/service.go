package discoverywatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"gorm.io/gorm"
)

// Commands sends scheduler commands once each, reads their outcome and
// takes them back.
type Commands interface {
	SubmitOnce(ctx context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error)
	Status(ctx context.Context, id string) (schedcmd.Record, error)
	Undo(ctx context.Context, id, author string) (schedcmd.Record, error)
}

// Service keeps the watches and their imaging requests in step with the
// vet, in the background.
type Service struct {
	DB       *gorm.DB
	Commands Commands
	Snapshot func(ctx context.Context) (*planning.Snapshot, error)
	// Loc is the observatory's time zone, which says what tonight is.
	Loc *time.Location
	Now func() time.Time
	// Task shows the background work in the processing indicator.
	Task     *activity.Task
	Interval time.Duration

	kick     *trigger.Debouncer
	kickOnce sync.Once
	mu       sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

// Kick asks for a look soon.
func (s *Service) Kick() { s.debouncer().Kick() }

func (s *Service) debouncer() *trigger.Debouncer {
	s.kickOnce.Do(func() { s.kick = &trigger.Debouncer{Quiet: 30 * time.Second, MaxWait: 5 * time.Minute} })
	return s.kick
}

// Run looks at the watches at start, soon after each Kick and every
// Interval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	every := s.Interval
	if every <= 0 {
		every = 30 * time.Minute
	}
	s.debouncer().Run(ctx, every, func(ctx context.Context, _ []string) {
		if err := s.Tick(ctx); err != nil {
			slog.Error("Discovery watch", "error", err)
		}
		s.Task.SetNext(s.now().Add(every))
	})
}

// candidates is every source that is or may become watched: unvetted
// changes the vet calls real at low confidence, and those with a watch
// under way.
func (s *Service) candidates(ctx context.Context) ([]discoverysource.Summary, error) {
	db := s.DB.WithContext(ctx)
	var ids []int
	if err := db.Model(&app.DiscoveryVet{}).Where("suggested = ? AND confidence = ?", app.VetReal, discoverysource.ConfidenceLow).
		Pluck("source_id", &ids).Error; err != nil {
		return nil, err
	}
	var open []int
	if err := db.Model(&app.DiscoveryWatch{}).Where("ended_at IS NULL").Pluck("source_id", &open).Error; err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var all []int
	for _, id := range append(ids, open...) {
		if !seen[id] {
			seen[id] = true
			all = append(all, id)
		}
	}
	return discoverysource.Load(ctx, db, all)
}

// report is a source's latest vet report.
func (s *Service) report(ctx context.Context, id int) (*vetting.Report, error) {
	st, err := vetting.Load(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	return st.Report, nil
}

// Tick applies the rules to every watched or watchable find, then asks for,
// follows and closes the watched nights' imaging.
func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := discoverysource.Sync(ctx, s.DB); err != nil {
		return err
	}
	list, err := s.candidates(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	s.Task.Start("Watching finds for more fades", int64(len(list)), "finds")
	var errs []error
	if err := s.endOrphans(ctx, list, now); err != nil {
		errs = append(errs, err)
	}
	for _, sum := range list {
		s.Task.SetCurrent(fmt.Sprintf("Find #%d in %s", sum.Source.ID, sum.Source.Object))
		if err := s.advance(ctx, sum, now); err != nil {
			errs = append(errs, fmt.Errorf("source %d: %w", sum.Source.ID, err))
		}
		s.Task.Step(1)
	}
	if err := s.follow(ctx, now); err != nil {
		errs = append(errs, err)
	}
	err = errors.Join(errs...)
	s.Task.Finish(err)
	return err
}

// endOrphans ends the open watches of sources gone from the list, as when
// a rerun left a source with no candidates.
func (s *Service) endOrphans(ctx context.Context, list []discoverysource.Summary, now time.Time) error {
	have := map[int]bool{}
	for _, sum := range list {
		have[sum.Source.ID] = true
	}
	var open []app.DiscoveryWatch
	if err := s.DB.WithContext(ctx).Where("ended_at IS NULL").Find(&open).Error; err != nil {
		return err
	}
	var errs []error
	for i := range open {
		w := &open[i]
		if have[w.SourceID] {
			continue
		}
		w.EndedAt, w.EndReason, w.UpdatedAt = &now, app.WatchEndOther, now
		if err := s.DB.WithContext(ctx).Save(w).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.cancelOpen(ctx, w.SourceID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) advance(ctx context.Context, sum discoverysource.Summary, now time.Time) error {
	rep, err := s.report(ctx, sum.Source.ID)
	if err != nil {
		return err
	}
	fades, known := FadeNights(sum, rep)
	out, ok := Advance(sum, sum.Watch, fades, known, now)
	if !ok {
		return nil
	}
	if out.Changed {
		if err := s.DB.WithContext(ctx).Save(&out.Watch).Error; err != nil {
			return err
		}
	}
	if out.Started && !known {
		if _, err := vetting.Request(ctx, s.DB, sum.Source.ID, now); err != nil {
			return err
		}
	}
	if out.Cancel {
		if err := s.cancelOpen(ctx, sum.Source.ID); err != nil {
			return err
		}
	}
	if Asking(&out.Watch) {
		return s.ask(ctx, sum, out.Watch, now)
	}
	return nil
}

// ask plans the watch's next set of nights once every night of the last
// is over, and sends those still waiting.
func (s *Service) ask(ctx context.Context, sum discoverysource.Summary, w app.DiscoveryWatch, now time.Time) error {
	db := s.DB.WithContext(ctx)
	var nights []app.DiscoveryWatchNight
	if err := db.Where("source_id = ? AND batch = ?", w.SourceID, w.Batch).Order("night").Find(&nights).Error; err != nil {
		return err
	}
	if len(nights) > 0 && allOver(nights, now) {
		w.Batch++
		w.UpdatedAt = now
		if err := db.Save(&w).Error; err != nil {
			return err
		}
		nights = nil
	}
	if len(nights) == 0 {
		for _, d := range PlanNights(now, s.loc()) {
			start, end := NightWindow(d, s.loc())
			nights = append(nights, app.DiscoveryWatchNight{SourceID: w.SourceID, Batch: w.Batch, Night: d, Start: start, End: end,
				Minutes: MinutesPerNight, State: app.EclipseWaiting, CreatedAt: now, UpdatedAt: now})
		}
		if err := db.Create(&nights).Error; err != nil {
			return err
		}
	}
	var waiting []*app.DiscoveryWatchNight
	for i := range nights {
		if nights[i].State == app.EclipseWaiting {
			waiting = append(waiting, &nights[i])
		}
	}
	if len(waiting) == 0 || s.Commands == nil || s.Snapshot == nil {
		return nil
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("planning snapshot: %w", err)
	}
	orig, err := discoveryfollowup.OriginalTarget(ctx, s.DB, &app.DiscoveryFollowup{Object: sum.Source.Object, Night: sum.Source.Night})
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range waiting {
		if err := s.send(ctx, snap, orig, sum.Source.Object, w.Group, n, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// allOver says every night of a set has ended and none waits on the
// scheduler.
func allOver(nights []app.DiscoveryWatchNight, now time.Time) bool {
	for _, n := range nights {
		if n.End.After(now) || n.State == app.EclipseSent {
			return false
		}
	}
	return true
}

func (s *Service) send(ctx context.Context, snap *planning.Snapshot, orig, object, group string, n *app.DiscoveryWatchNight, now time.Time) error {
	n.UpdatedAt = now
	if !n.End.After(now) {
		n.State = app.EclipseFailed
		if n.Note == "" {
			n.Note = "the night passed before it could be sent"
		}
		return s.DB.WithContext(ctx).Save(n).Error
	}
	payload, why := Draft(snap, orig, object, n, group)
	if payload == nil {
		n.Note = why
		return s.DB.WithContext(ctx).Save(n).Error
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rec, err := s.Commands.SubmitOnce(ctx, "discovery-watch-"+n.ProjectGUID, schedcmd.KindProjectCreate, raw, Author)
	if err != nil {
		n.Note = "the scheduler did not take it: " + err.Error()
	} else {
		n.CreateCmd, n.State = rec.ID, app.EclipseSent
		n.Note = fmt.Sprintf("Sent to the scheduler: %d × %gs %s in gaps on the night of %s.", n.Subs, n.Exposure, n.Filter, n.Night.Format("Jan 2"))
	}
	return s.DB.WithContext(ctx).Save(n).Error
}

// cancelOpen withdraws a source's waiting and sent nights still to come.
func (s *Service) cancelOpen(ctx context.Context, sourceID int) error {
	var open []app.DiscoveryWatchNight
	if err := s.DB.WithContext(ctx).Where("source_id = ? AND state IN ?", sourceID, []string{app.EclipseWaiting, app.EclipseSent}).
		Find(&open).Error; err != nil {
		return err
	}
	var errs []error
	for i := range open {
		n := &open[i]
		if n.State == app.EclipseSent && !n.End.After(s.now()) {
			continue
		}
		underWay := n.State == app.EclipseSent && !n.Start.After(s.now())
		if underWay {
			if err := s.closeProject(ctx, n); err != nil {
				errs = append(errs, err)
				continue
			}
		} else if n.CreateCmd != "" && s.Commands != nil {
			if _, err := s.Commands.Undo(ctx, n.CreateCmd, Author); err != nil {
				if !errors.Is(err, schedcmd.ErrNotUndoable) {
					errs = append(errs, err)
					continue
				}
				if err := s.closeProject(ctx, n); err != nil {
					errs = append(errs, err)
					continue
				}
			}
		}
		n.State, n.Note, n.UpdatedAt = app.EclipseCancel, "Withdrawn: the watch no longer asks for imaging.", s.now()
		if err := s.DB.WithContext(ctx).Save(n).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// follow marks nights the scheduler refused as failed, and closes the
// project of each night that has passed.
func (s *Service) follow(ctx context.Context, now time.Time) error {
	if s.Commands == nil {
		return nil
	}
	var open []app.DiscoveryWatchNight
	if err := s.DB.WithContext(ctx).Where("state = ?", app.EclipseSent).Find(&open).Error; err != nil {
		return err
	}
	var errs []error
	for i := range open {
		n := &open[i]
		rec, err := s.Commands.Status(ctx, n.CreateCmd)
		switch {
		case err == nil && refused(rec.Status):
			n.State, n.Note = app.EclipseFailed, "The scheduler did not take it: "+cmpOr(rec.Message, string(rec.Status))
		case now.After(n.End.Add(time.Hour)):
			if err := s.closeProject(ctx, n); err != nil {
				errs = append(errs, err)
				continue
			}
			n.State, n.Note = app.EclipseDone, "The night has passed."
		default:
			continue
		}
		n.UpdatedAt = now
		if err := s.DB.WithContext(ctx).Save(n).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func refused(st schedcmd.Status) bool {
	return st == schedcmd.StatusRejected || st == schedcmd.StatusFailed || st == schedcmd.StatusConflict || st == schedcmd.StatusCancelled
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// closeProject closes a night's project in Target Scheduler.
func (s *Service) closeProject(ctx context.Context, n *app.DiscoveryWatchNight) error {
	if s.Snapshot == nil || s.Commands == nil {
		return nil
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, p := range snap.Projects {
		if p.GUID != n.ProjectGUID || p.State == "Closed" {
			continue
		}
		raw, err := json.Marshal(schedcmd.EditPayload{Entity: "project", ID: int64(p.ID), GUID: p.GUID, Name: p.Name,
			Changes: []schedcmd.FieldChange{{Field: "state", Before: json.RawMessage(stateNumber(p.State)), After: json.RawMessage("3")}}})
		if err != nil {
			return err
		}
		rec, err := s.Commands.SubmitOnce(ctx, "discovery-watch-close-"+n.ProjectGUID, schedcmd.KindProjectEdit, raw, Author)
		if err != nil {
			return err
		}
		n.CloseCmd = rec.ID
	}
	return nil
}

func stateNumber(name string) string {
	for k, v := range schedcmd.ProjectStates() {
		if v == name {
			return fmt.Sprint(k)
		}
	}
	return "1"
}

// ErrNotWatched says the source has no watch under way.
var ErrNotWatched = errors.New("the find is not being watched")

// Stop stops a watch asking for imaging, by the user, and withdraws its
// nights still to come. The find stays in Watching.
func (s *Service) Stop(ctx context.Context, sourceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var w app.DiscoveryWatch
	err := s.DB.WithContext(ctx).Where("source_id = ? AND ended_at IS NULL", sourceID).First(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotWatched
	}
	if err != nil {
		return err
	}
	if w.ImagingStoppedAt == nil {
		now := s.now()
		w.ImagingStoppedAt, w.StopReason, w.UpdatedAt = &now, app.WatchStopUser, now
		if err := s.DB.WithContext(ctx).Save(&w).Error; err != nil {
			return err
		}
	}
	return s.cancelOpen(ctx, sourceID)
}
