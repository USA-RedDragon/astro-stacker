package claimswatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Record is a dated public record of the user's finds. Sources are the
// stars it covers.
type Record struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Sources   []int     `json:"-"`
}

// Records are the user's dated records: the Zenodo discovery record of
// the 31 stars confirmed by 2026-10-20, first published 2026-10-21 00:12:24
// UTC (version 1.0, record 23519982; concept DOI 10.5281/zenodo.23519981).
func Records() []Record {
	return []Record{{
		Name:      "Zenodo",
		URL:       "https://doi.org/10.5281/zenodo.23519981",
		Published: time.Date(2026, 10, 21, 0, 12, 24, 0, time.UTC),
		Sources: []int{167, 236, 571, 618, 1592, 1688, 2217, 2289, 2290, 2301, 3635, 3754, 4046, 6014, 15362, 15523, 16040,
			16658, 17511, 21142, 23081, 23991, 27822, 30141, 31560, 33136, 33629, 34867, 43141, 45029, 49572},
	}}
}

// RecordFor is the earliest record covering a star, or nil.
func RecordFor(recs []Record, id int) *Record {
	var best *Record
	for i := range recs {
		if slices.Contains(recs[i].Sources, id) && (best == nil || recs[i].Published.Before(best.Published)) {
			best = &recs[i]
		}
	}
	return best
}

// Service runs the weekly check and answers the pages.
type Service struct {
	DB      *gorm.DB
	Stars   func(ctx context.Context) ([]Star, error)
	Places  []Place
	Records []Record
	Task    *activity.Task
	Loc     *time.Location
	Now     func() time.Time
	// Weekday and Hour are when the weekly check runs, in Loc.
	Weekday time.Weekday
	Hour    int
	// FirstDelay is how long after start the very first check waits.
	FirstDelay time.Duration

	mu      sync.Mutex
	running bool
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

func (s *Service) records() []Record {
	if s.Records != nil {
		return s.Records
	}
	return Records()
}

// NextAfter is the first weekly slot after t.
func (s *Service) NextAfter(t time.Time) time.Time {
	l := t.In(s.loc())
	d := time.Date(l.Year(), l.Month(), l.Day(), s.Hour, 0, 0, 0, s.loc())
	for !d.After(t) || d.Weekday() != s.Weekday {
		d = time.Date(d.Year(), d.Month(), d.Day()+1, s.Hour, 0, 0, 0, s.loc())
	}
	return d.UTC()
}

// LastChecked is when the last check finished; false before the first.
func (s *Service) LastChecked(ctx context.Context) (time.Time, bool, error) {
	v, err := dbState{s.DB}.Get(ctx, stateLastRun)
	if err != nil || v == "" {
		return time.Time{}, false, err
	}
	t, perr := time.Parse(time.RFC3339, v)
	return t, perr == nil, nil
}

const stateLastRun = "run:last"

// Next is when the next check runs.
func (s *Service) Next(ctx context.Context) (time.Time, error) {
	last, ok, err := s.LastChecked(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return s.now().Add(s.FirstDelay), nil
	}
	return s.NextAfter(last), nil
}

// Run checks once a week until ctx ends; the first check runs FirstDelay
// after start.
func (s *Service) Run(ctx context.Context) {
	for {
		next, err := s.Next(ctx)
		if err != nil {
			slog.Error("Claims watch: could not work out the next check", "error", err)
			next = s.now().Add(time.Hour)
		}
		if s.Task != nil {
			s.Task.SetNext(next)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		if err := s.Check(ctx); err != nil {
			slog.Error("Claims watch check", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Hour):
			}
		}
	}
}

// Running tells whether a check is under way.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

type dbState struct{ db *gorm.DB }

func (d dbState) Get(ctx context.Context, key string) (string, error) {
	var v app.ClaimWatchState
	err := d.db.WithContext(ctx).Where("key = ?", key).Limit(1).Find(&v).Error
	return v.Value, err
}

func (d dbState) Set(ctx context.Context, key, value string) error {
	return d.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).
		Create(&app.ClaimWatchState{Key: key, Value: value, UpdatedAt: time.Now().UTC()}).Error
}

// Check searches every place for every confirmed star once.
func (s *Service) Check(ctx context.Context) (err error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("a check is already running")
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	if s.Task != nil {
		s.Task.Start("Looking for your stars in other catalogs and papers", int64(len(s.Places)), "places")
		defer func() { s.Task.Finish(err) }()
	}
	stars, err := s.Stars(ctx)
	if err != nil {
		return fmt.Errorf("could not read the confirmed stars: %w", err)
	}
	if len(stars) == 0 {
		return nil
	}
	st := dbState{s.DB}
	var failed []string
	for _, p := range s.Places {
		if s.Task != nil {
			s.Task.SetCurrent(Label(p.Where()))
		}
		at := s.now()
		var out Outcome
		var perr error
		off := p.Off()
		if off == "" {
			out, perr = p.Check(ctx, stars, st)
			if perr != nil {
				failed = append(failed, Label(p.Where()))
				slog.Warn("Claims watch: a place could not be checked", "where", p.Where(), "error", perr)
			}
		}
		if err := s.save(ctx, p.Where(), stars, out, off, perr, at); err != nil {
			return err
		}
		if s.Task != nil {
			s.Task.Step(1)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := st.Set(ctx, stateLastRun, s.now().Format(time.RFC3339)); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not check %v; the next weekly check tries again", failed)
	}
	return nil
}

func (s *Service) save(ctx context.Context, where string, stars []Star, out Outcome, off string, perr error, at time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, star := range stars {
			var prev app.ClaimCheck
			if err := tx.Where("source_id = ? AND place = ?", star.SourceID, where).Limit(1).Find(&prev).Error; err != nil {
				return err
			}
			row := app.ClaimCheck{SourceID: star.SourceID, Where: where, CheckedAt: at, FirstAt: prev.FirstAt}
			switch {
			case off != "":
				row.State, row.Note = app.ClaimNotChecked, off
			case perr != nil:
				row.State, row.Note = app.ClaimFailed, perr.Error()
				if err := s.merge(tx, star, where, out.Found[star.SourceID], prev.FirstAt == nil, at); err != nil {
					return err
				}
			case out.Failed[star.SourceID] != "":
				row.State, row.Note = app.ClaimFailed, out.Failed[star.SourceID]
			default:
				row.State = app.ClaimChecked
				baseline := prev.FirstAt == nil
				if baseline {
					row.FirstAt = &at
				}
				if err := s.merge(tx, star, where, out.Found[star.SourceID], baseline, at); err != nil {
					return err
				}
			}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) merge(tx *gorm.DB, star Star, where string, found []Mention, baseline bool, at time.Time) error {
	for _, m := range found {
		var row app.ClaimMention
		err := tx.Where("source_id = ? AND place = ? AND key = ?", star.SourceID, where, m.Key).Limit(1).Find(&row).Error
		switch {
		case err == nil && row.ID == 0:
			row = app.ClaimMention{SourceID: star.SourceID, Where: where, Key: m.Key, StarName: star.Name, Title: m.Title, Detail: m.Detail,
				URL: m.URL, Dated: m.Dated, FirstSeen: at, LastSeen: at, Baseline: baseline, Own: m.Own}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if err := tx.Model(&row).Updates(map[string]any{"title": m.Title, "detail": m.Detail, "url": m.URL, "dated": m.Dated,
				"last_seen": at, "own": m.Own, "star_name": star.Name}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrBadMark is a mark other than seen, citing or none.
var ErrBadMark = errors.New(`mark must be "seen", "citing" or ""`)

// Mark sets the user's mark on a mention.
func (s *Service) Mark(ctx context.Context, id int, mark string) (MentionView, error) {
	if mark != "" && mark != app.ClaimMarkSeen && mark != app.ClaimMarkCiting {
		return MentionView{}, ErrBadMark
	}
	var row app.ClaimMention
	if err := s.DB.WithContext(ctx).Take(&row, id).Error; err != nil {
		return MentionView{}, err
	}
	var at *time.Time
	if mark != "" {
		t := s.now()
		at = &t
	}
	if err := s.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"mark": mark, "marked_at": at}).Error; err != nil {
		return MentionView{}, err
	}
	row.Mark, row.MarkedAt = mark, at
	return s.mentionView(row), nil
}
