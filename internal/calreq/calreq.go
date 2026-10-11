// Package calreq keeps the web app's requests for flats, darks and bias
// frames for one object's lights of one night, and marks them done once the
// frames are indexed.
package calreq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

// RefusalError is a request or cancel the stacker turns down, with a plain
// message for the user.
type RefusalError struct{ Msg string }

func (r RefusalError) Error() string { return r.Msg }

func refuse(format string, args ...any) error { return RefusalError{Msg: fmt.Sprintf(format, args...)} }

type Payload struct {
	Type     string `json:"type"`
	Object   string `json:"object"`
	TargetID *int   `json:"target_id,omitempty"`
	Night    string `json:"night"`
}

// Check validates a payload; a request for flats needs the Target
// Scheduler target id.
func (p Payload) Check(request bool) error {
	switch p.Type {
	case app.CalibrationFlats, app.CalibrationDarks, app.CalibrationBias:
	default:
		return refuse("type must be flats, darks or bias")
	}
	if strings.TrimSpace(p.Object) == "" {
		return refuse("object is required")
	}
	if _, err := time.Parse(time.DateOnly, p.Night); err != nil {
		return refuse("night must be a date, YYYY-MM-DD")
	}
	if p.TargetID != nil && *p.TargetID <= 0 {
		return refuse("target_id must be a Target Scheduler target id")
	}
	if request && p.Type == app.CalibrationFlats && p.TargetID == nil {
		return refuse("a flats request needs the Target Scheduler target_id")
	}
	return nil
}

func (p Payload) night() time.Time {
	t, _ := time.Parse(time.DateOnly, p.Night)
	return t
}

func (p Payload) what() string {
	return fmt.Sprintf("%s request for %s on the night of %s", p.Type, p.Object, p.Night)
}

func findOpen(ctx context.Context, db *gorm.DB, p Payload) (app.CalibrationRequest, bool, error) {
	var open []app.CalibrationRequest
	if err := db.WithContext(ctx).Where("type = ? AND object = ? AND status = ?", p.Type, p.Object, app.CalibrationOpen).
		Find(&open).Error; err != nil {
		return app.CalibrationRequest{}, false, fmt.Errorf("load open calibration requests: %w", err)
	}
	for _, r := range open {
		if r.Night.Format(time.DateOnly) == p.Night {
			return r, true, nil
		}
	}
	return app.CalibrationRequest{}, false, nil
}

// Open saves a new open request, refusing one with no lights to calibrate
// or one that duplicates an open request.
func Open(ctx context.Context, db *gorm.DB, p Payload, id, author string, now time.Time) (app.CalibrationRequest, error) {
	if err := p.Check(true); err != nil {
		return app.CalibrationRequest{}, err
	}
	prog, err := coverage.RequestLights(ctx, db, p.Type, p.Object, p.night())
	if err != nil {
		return app.CalibrationRequest{}, err
	}
	if prog.Lights == 0 {
		return app.CalibrationRequest{}, refuse("no lights of %s on the night of %s are indexed", p.Object, p.Night)
	}
	if len(prog.Items) == 0 {
		return app.CalibrationRequest{}, refuse("%s cannot be matched to the lights of %s on the night of %s: %s", p.Type, p.Object, p.Night, prog.Unschedulable)
	}
	r := app.CalibrationRequest{ID: id, Type: p.Type, Object: p.Object, TargetID: p.TargetID, Night: p.night(),
		Status: app.CalibrationOpen, CreatedAt: now.UTC(), CreatedBy: author}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, dup, err := findOpen(ctx, tx, p)
		if err != nil {
			return err
		}
		if dup {
			return refuse("an open %s already exists", p.what())
		}
		return tx.Create(&r).Error
	})
	return r, err
}

// Cancel cancels the open request with the payload's type, object and
// night.
func Cancel(ctx context.Context, db *gorm.DB, p Payload, author string, now time.Time) (app.CalibrationRequest, error) {
	if err := p.Check(false); err != nil {
		return app.CalibrationRequest{}, err
	}
	var out app.CalibrationRequest
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, ok, err := findOpen(ctx, tx, p)
		if err != nil {
			return err
		}
		if !ok {
			return refuse("there is no open %s", p.what())
		}
		at := now.UTC()
		r.Status, r.CancelledAt, r.CancelledBy = app.CalibrationCancelled, &at, &author
		out = r
		return tx.Save(&r).Error
	})
	return out, err
}

// OpenRequests lists the open requests, oldest first; none when the table is
// missing.
func OpenRequests(ctx context.Context, db *gorm.DB) ([]app.CalibrationRequest, error) {
	has, err := tables.Has(ctx, db, &app.CalibrationRequest{})
	if err != nil || !has {
		return nil, err
	}
	var out []app.CalibrationRequest
	if err := db.WithContext(ctx).Where("status = ?", app.CalibrationOpen).Order("created_at, id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("load open calibration requests: %w", err)
	}
	return out, nil
}

const (
	StatusAll  = "all"
	ListLimit  = 200
	errNoTable = "no calibration_requests table"
)

var ErrStatus = errors.New("status must be open, done, cancelled or all")

// List returns requests newest first, at most ListLimit.
func List(ctx context.Context, db *gorm.DB, object, status string) ([]app.CalibrationRequest, error) {
	switch status {
	case "", StatusAll, app.CalibrationOpen, app.CalibrationDone, app.CalibrationCancelled:
	default:
		return nil, ErrStatus
	}
	has, err := tables.Has(ctx, db, &app.CalibrationRequest{})
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New(errNoTable)
	}
	q := db.WithContext(ctx).Model(&app.CalibrationRequest{})
	if object != "" {
		q = q.Where("object = ?", object)
	}
	if status != "" && status != StatusAll {
		q = q.Where("status = ?", status)
	}
	var out []app.CalibrationRequest
	if err := q.Order("created_at DESC, id DESC").Limit(ListLimit).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list calibration requests: %w", err)
	}
	return out, nil
}

// EventType is the event stream type of a request that changed.
const EventType = "calibration"

// Event is what listeners are told when a request changes.
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Object string `json:"object"`
	Night  string `json:"night"`
	Status string `json:"status"`
}

func EventOf(r app.CalibrationRequest) Event {
	return Event{ID: r.ID, Type: r.Type, Object: r.Object, Night: r.Night.Format(time.DateOnly), Status: r.Status}
}

// Refresh marks every open request whose frames are all indexed done, and
// returns those.
func Refresh(ctx context.Context, db *gorm.DB, now time.Time) ([]app.CalibrationRequest, error) {
	open, err := OpenRequests(ctx, db)
	if err != nil || len(open) == 0 {
		return nil, err
	}
	prog, err := coverage.MeasureRequests(ctx, db, open)
	if err != nil {
		return nil, err
	}
	var done []app.CalibrationRequest
	for i, r := range open {
		if !prog[i].Done() {
			continue
		}
		at := now.UTC()
		res := db.WithContext(ctx).Model(&app.CalibrationRequest{}).Where("id = ? AND status = ?", r.ID, app.CalibrationOpen).
			Updates(map[string]any{"status": app.CalibrationDone, "done_at": at})
		if res.Error != nil {
			return done, fmt.Errorf("mark calibration request %s done: %w", r.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			continue
		}
		r.Status, r.DoneAt = app.CalibrationDone, &at
		done = append(done, r)
	}
	return done, nil
}
