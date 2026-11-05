package morningreport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sender delivers a report.
type Sender interface {
	Message(ctx context.Context, html string) error
	Photos(ctx context.Context, photos []Photo) error
}

// Service sends each night's report once, at the schedule's time, or late
// the same day when the stacker was down at that time.
type Service struct {
	DB        *gorm.DB
	Collector Collector
	Formatter Formatter
	Sender    Sender
	Schedule  Schedule
	Now       func() time.Time
	// RetryAfter is the wait before a failed send is tried again, up to
	// MaxAttempts sends the same day.
	RetryAfter  time.Duration
	MaxAttempts int
	// First is the first night sent; earlier nights never are. Zero sends
	// every night.
	First time.Time
	// Task, if set, hears each send's steps and the next run.
	Task *activity.Task
}

// Render collects a night's report and renders the message and photos
// without sending anything.
func (s *Service) Render(ctx context.Context, night time.Time) (string, []Photo, Report, error) {
	r, err := s.Collector.Collect(ctx, night)
	if err != nil {
		return "", nil, r, err
	}
	photos := s.Formatter.dustPhotos(r)
	for _, rc := range r.Runs {
		if len(rc.JPEG) > 0 {
			photos = append(photos, Photo{Caption: s.Formatter.RunCaption(rc.Night), JPEG: rc.JPEG})
		}
	}
	for _, im := range r.Images {
		if len(im.JPEG) == 0 {
			continue
		}
		photos = append(photos, Photo{Caption: s.Formatter.Caption(im), JPEG: im.JPEG})
	}
	return s.Formatter.Text(r), photos, r, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run sends reports until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		wait := s.Tick(ctx)
		s.Task.SetNext(s.now().Add(wait))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// Tick sends the report that is due, if it hasn't gone out, and returns how
// long to wait before the next tick.
func (s *Service) Tick(ctx context.Context) time.Duration {
	now := s.now()
	night, due := s.Schedule.Due(now)
	if !s.Schedule.SameDay(due, now) || night.Before(s.First) {
		return s.Schedule.Next(now).Sub(now)
	}
	retry, err := s.send(ctx, night, now)
	if err != nil {
		slog.Error("Morning report not sent", "night", NightKey(night), "error", err)
	}
	now = s.now()
	wait := s.Schedule.Next(now).Sub(now)
	if retry > 0 && retry < wait {
		return retry
	}
	return wait
}

// ClaimLease is how long a claim with no outcome blocks another send; a
// send that stopped while rendering is tried again after it.
const ClaimLease = 10 * time.Minute

const (
	colError          = "error"
	colPhotosFailedAt = "photos_failed_at"
)

// PhotoRetryAfter is the wait before photos that failed, or that Telegram
// may have taken without answering, are sent once more.
const PhotoRetryAfter = 5 * time.Minute

// MaxPhotoAttempts is the most sends of a night's photos. A second send can
// double them when Telegram took the first without answering; that beats
// a report with no pictures.
const MaxPhotoAttempts = 2

type claimKind int

const (
	claimNone claimKind = iota
	claimReport
	claimPhotos
)

var errClaimed = errors.New("another send claimed this night")

// claim records that the night's report is being sent, or says why not and
// when to look again.
func (s *Service) claim(ctx context.Context, key string, now time.Time) (claimKind, time.Duration, error) {
	db := s.DB.WithContext(ctx)
	var recs []app.MorningReport
	if err := db.Where("night = ?", key).Limit(1).Find(&recs).Error; err != nil {
		return claimNone, s.RetryAfter, err
	}
	if len(recs) == 0 {
		res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&app.MorningReport{Night: key, ClaimedAt: now, Attempts: 1})
		if res.Error != nil {
			return claimNone, s.RetryAfter, res.Error
		}
		if res.RowsAffected == 0 {
			return claimNone, 0, errClaimed
		}
		return claimReport, 0, nil
	}
	rec := recs[0]
	wait := s.RetryAfter
	switch {
	case rec.SentAt != nil:
		return s.claimPhotos(ctx, rec, now)
	case rec.SendingAt != nil:
		slog.Warn("Morning report may have gone out; not sending it again", "night", key, "sending_at", rec.SendingAt.Format(time.RFC3339))
		return claimNone, 0, nil
	case rec.Attempts >= s.MaxAttempts:
		return claimNone, 0, nil
	case rec.Error == nil:
		wait = ClaimLease
	}
	if at := rec.ClaimedAt.Add(wait); now.Before(at) {
		return claimNone, at.Sub(now), nil
	}
	res := db.Model(&app.MorningReport{}).Where("night = ? AND attempts = ? AND sent_at IS NULL AND sending_at IS NULL", key, rec.Attempts).
		Updates(map[string]any{"claimed_at": now, "attempts": rec.Attempts + 1, colError: nil})
	if res.Error != nil {
		return claimNone, s.RetryAfter, res.Error
	}
	if res.RowsAffected == 0 {
		return claimNone, 0, errClaimed
	}
	return claimReport, 0, nil
}

// claimPhotos claims another send of a sent report's photos when the last
// one failed and sends remain.
func (s *Service) claimPhotos(ctx context.Context, rec app.MorningReport, now time.Time) (claimKind, time.Duration, error) {
	if rec.PhotosSentAt != nil || rec.PhotosFailedAt == nil || rec.PhotoAttempts >= MaxPhotoAttempts {
		return claimNone, 0, nil
	}
	if at := rec.PhotosFailedAt.Add(PhotoRetryAfter); now.Before(at) {
		return claimNone, at.Sub(now), nil
	}
	res := s.DB.WithContext(ctx).Model(&app.MorningReport{}).
		Where("night = ? AND photo_attempts = ? AND photos_sent_at IS NULL AND photos_failed_at IS NOT NULL", rec.Night, rec.PhotoAttempts).
		Updates(map[string]any{"photo_attempts": rec.PhotoAttempts + 1, colPhotosFailedAt: nil})
	if res.Error != nil {
		return claimNone, s.RetryAfter, res.Error
	}
	if res.RowsAffected == 0 {
		return claimNone, 0, errClaimed
	}
	return claimPhotos, 0, nil
}

func (s *Service) send(ctx context.Context, night, now time.Time) (retry time.Duration, err error) {
	key := NightKey(night)
	kind, retry, err := s.claim(ctx, key, now)
	if err != nil || kind == claimNone {
		if errors.Is(err, errClaimed) {
			return 0, nil
		}
		return retry, err
	}
	s.Task.Start("collecting the night of "+key, 3, "steps")
	defer func() { s.Task.Finish(err) }()
	record := func(fields map[string]any) {
		if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.MorningReport{}).Where("night = ?", key).Updates(fields).Error; err != nil {
			slog.Error("Recording the morning report's state failed", "night", key, "error", err)
		}
	}
	text, photos, _, err := s.Render(ctx, night)
	if kind == claimPhotos {
		if err != nil {
			record(map[string]any{colError: "photos: " + err.Error(), colPhotosFailedAt: s.now()})
			return PhotoRetryAfter, err
		}
		return s.sendPhotos(ctx, key, photos, record)
	}
	if err != nil {
		record(map[string]any{colError: err.Error()})
		return s.RetryAfter, err
	}
	if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.MorningReport{}).Where("night = ?", key).
		Update("sending_at", s.now()).Error; err != nil {
		return ClaimLease, fmt.Errorf("record the send before sending: %w", err)
	}
	s.Task.SetProgress(1, 3)
	s.Task.SetCurrent("sending the report")
	if err := s.Sender.Message(ctx, text); err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			record(map[string]any{colError: err.Error()})
			return 0, err
		}
		record(map[string]any{colError: err.Error(), "sending_at": nil})
		return s.RetryAfter, err
	}
	record(map[string]any{"sent_at": s.now(), "text": text, "photo_attempts": 1})
	slog.Info("Morning report sent", "night", key, "chars", utf8.RuneCountInString(text), "photos", len(photos))
	return s.sendPhotos(ctx, key, photos, record)
}

// sendPhotos sends the report's photos and records the outcome; a failed
// send is tried again after PhotoRetryAfter while sends remain.
func (s *Service) sendPhotos(ctx context.Context, key string, photos []Photo, record func(map[string]any)) (time.Duration, error) {
	if len(photos) == 0 {
		return 0, nil
	}
	s.Task.SetProgress(2, 3)
	s.Task.SetCurrent("sending the photos")
	if err := s.Sender.Photos(ctx, photos); err != nil {
		record(map[string]any{colError: "photos: " + err.Error(), colPhotosFailedAt: s.now()})
		return PhotoRetryAfter, err
	}
	record(map[string]any{"photos_sent_at": s.now(), colError: nil})
	slog.Info("Morning report photos sent", "night", key, "photos", len(photos))
	return 0, nil
}
