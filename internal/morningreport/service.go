package morningreport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

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
}

// Render collects a night's report and renders the message and photos
// without sending anything.
func (s *Service) Render(ctx context.Context, night time.Time) (string, []Photo, Report, error) {
	r, err := s.Collector.Collect(ctx, night)
	if err != nil {
		return "", nil, r, err
	}
	photos := s.Formatter.dustPhotos(r)
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

const colError = "error"

var errClaimed = errors.New("another send claimed this night")

// claim records that the night's report is being sent, or says why not and
// when to look again.
func (s *Service) claim(ctx context.Context, key string, now time.Time) (bool, time.Duration, error) {
	db := s.DB.WithContext(ctx)
	var recs []app.MorningReport
	if err := db.Where("night = ?", key).Limit(1).Find(&recs).Error; err != nil {
		return false, s.RetryAfter, err
	}
	if len(recs) == 0 {
		res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&app.MorningReport{Night: key, ClaimedAt: now, Attempts: 1})
		if res.Error != nil {
			return false, s.RetryAfter, res.Error
		}
		if res.RowsAffected == 0 {
			return false, 0, errClaimed
		}
		return true, 0, nil
	}
	rec := recs[0]
	wait := s.RetryAfter
	switch {
	case rec.SentAt != nil:
		return false, 0, nil
	case rec.SendingAt != nil:
		slog.Warn("Morning report may have gone out; not sending it again", "night", key, "sending_at", rec.SendingAt.Format(time.RFC3339))
		return false, 0, nil
	case rec.Attempts >= s.MaxAttempts:
		return false, 0, nil
	case rec.Error == nil:
		wait = ClaimLease
	}
	if at := rec.ClaimedAt.Add(wait); now.Before(at) {
		return false, at.Sub(now), nil
	}
	res := db.Model(&app.MorningReport{}).Where("night = ? AND attempts = ? AND sent_at IS NULL AND sending_at IS NULL", key, rec.Attempts).
		Updates(map[string]any{"claimed_at": now, "attempts": rec.Attempts + 1, colError: nil})
	if res.Error != nil {
		return false, s.RetryAfter, res.Error
	}
	if res.RowsAffected == 0 {
		return false, 0, errClaimed
	}
	return true, 0, nil
}

func (s *Service) send(ctx context.Context, night, now time.Time) (time.Duration, error) {
	key := NightKey(night)
	ok, retry, err := s.claim(ctx, key, now)
	if err != nil || !ok {
		if errors.Is(err, errClaimed) {
			return 0, nil
		}
		return retry, err
	}
	record := func(fields map[string]any) {
		if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.MorningReport{}).Where("night = ?", key).Updates(fields).Error; err != nil {
			slog.Error("Recording the morning report's state failed", "night", key, "error", err)
		}
	}
	text, photos, _, err := s.Render(ctx, night)
	if err != nil {
		record(map[string]any{colError: err.Error()})
		return s.RetryAfter, err
	}
	if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.MorningReport{}).Where("night = ?", key).
		Update("sending_at", s.now()).Error; err != nil {
		return ClaimLease, fmt.Errorf("record the send before sending: %w", err)
	}
	if err := s.Sender.Message(ctx, text); err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			record(map[string]any{colError: err.Error()})
			return 0, err
		}
		record(map[string]any{colError: err.Error(), "sending_at": nil})
		return s.RetryAfter, err
	}
	record(map[string]any{"sent_at": s.now(), "text": text})
	slog.Info("Morning report sent", "night", key, "chars", utf8.RuneCountInString(text), "photos", len(photos))
	if len(photos) == 0 {
		return 0, nil
	}
	if err := s.Sender.Photos(ctx, photos); err != nil {
		record(map[string]any{colError: "photos: " + err.Error()})
		return 0, err
	}
	return 0, nil
}
