// Package autofocus reads the autofocus runs that observatory-scheduler
// records in ts_autofocus_run and SymmetricDS replicates to schedulerdb.
package autofocus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	Table        = "ts_autofocus_run"
	DefaultLimit = 200
	MaxLimit     = 1000
)

// SchemaPostgres is the schedulerdb table SymmetricDS loads the observatory's
// rows into. The flux repo's SymmetricDS config carries the same statement.
const SchemaPostgres = `create table if not exists ts_autofocus_run (
    id text NOT NULL,
    profile_id text,
    started_at timestamp,
    finished_at timestamp NOT NULL,
    duration_seconds double precision,
    success integer NOT NULL,
    error text,
    filter text,
    temperature double precision,
    autofocuser text,
    method text,
    fitting text,
    initial_position double precision,
    previous_position double precision,
    previous_value double precision,
    chosen_position double precision,
    chosen_value double precision,
    points text NOT NULL,
    fit text,
    target_name text,
    target_guid text,
    source text NOT NULL,
    recorded_at timestamp NOT NULL,
    PRIMARY KEY (id)
);
create index if not exists ts_autofocus_run_finished_idx on ts_autofocus_run (finished_at);
`

type Point struct {
	Position float64  `json:"position"`
	Value    float64  `json:"value"`
	Error    *float64 `json:"error,omitempty"`
}

type Run struct {
	ID               string          `json:"id"`
	ProfileID        string          `json:"profile_id,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       time.Time       `json:"finished_at"`
	DurationSeconds  *float64        `json:"duration_seconds,omitempty"`
	Success          bool            `json:"success"`
	Error            string          `json:"error,omitempty"`
	Filter           string          `json:"filter,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	Autofocuser      string          `json:"autofocuser,omitempty"`
	Method           string          `json:"method,omitempty"`
	Fitting          string          `json:"fitting,omitempty"`
	InitialPosition  *float64        `json:"initial_position,omitempty"`
	PreviousPosition *float64        `json:"previous_position,omitempty"`
	PreviousValue    *float64        `json:"previous_value,omitempty"`
	ChosenPosition   *float64        `json:"chosen_position,omitempty"`
	ChosenValue      *float64        `json:"chosen_value,omitempty"`
	Points           []Point         `json:"points"`
	Fit              json.RawMessage `json:"fit,omitempty"`
	TargetName       string          `json:"target_name,omitempty"`
	TargetGUID       string          `json:"target_guid,omitempty"`
	Source           string          `json:"source"`
	PointsError      string          `json:"points_error,omitempty"`
}

type Runs struct {
	Since     time.Time `json:"since"`
	Available bool      `json:"available"`
	Runs      []Run     `json:"runs"`
}

type row struct {
	ID               string     `gorm:"column:id"`
	ProfileID        *string    `gorm:"column:profile_id"`
	StartedAt        *time.Time `gorm:"column:started_at"`
	FinishedAt       time.Time  `gorm:"column:finished_at"`
	DurationSeconds  *float64   `gorm:"column:duration_seconds"`
	Success          int        `gorm:"column:success"`
	Error            *string    `gorm:"column:error"`
	Filter           *string    `gorm:"column:filter"`
	Temperature      *float64   `gorm:"column:temperature"`
	Autofocuser      *string    `gorm:"column:autofocuser"`
	Method           *string    `gorm:"column:method"`
	Fitting          *string    `gorm:"column:fitting"`
	InitialPosition  *float64   `gorm:"column:initial_position"`
	PreviousPosition *float64   `gorm:"column:previous_position"`
	PreviousValue    *float64   `gorm:"column:previous_value"`
	ChosenPosition   *float64   `gorm:"column:chosen_position"`
	ChosenValue      *float64   `gorm:"column:chosen_value"`
	Points           string     `gorm:"column:points"`
	Fit              *string    `gorm:"column:fit"`
	TargetName       *string    `gorm:"column:target_name"`
	TargetGUID       *string    `gorm:"column:target_guid"`
	Source           string     `gorm:"column:source"`
}

const columns = "id, profile_id, started_at, finished_at, duration_seconds, success, error, filter, temperature, autofocuser, method, fitting, " +
	"initial_position, previous_position, previous_value, chosen_position, chosen_value, points, fit, target_name, target_guid, source"

// Load returns the runs that finished at or after since, newest first. A
// database without the table yet answers with no runs and Available false.
func Load(ctx context.Context, db *gorm.DB, since time.Time, limit int) (Runs, error) {
	return LoadBetween(ctx, db, since, time.Time{}, limit)
}

// LoadBetween is Load of the runs that finished before until; a zero until
// has no end.
func LoadBetween(ctx context.Context, db *gorm.DB, since, until time.Time, limit int) (Runs, error) {
	out := Runs{Since: since.UTC(), Runs: []Run{}}
	if db == nil {
		return out, nil
	}
	has, err := tables.Has(ctx, db, Table)
	if err != nil || !has {
		return out, err
	}
	out.Available = true
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	var rows []row
	q := db.WithContext(ctx).Table(Table).Select(columns).Where("finished_at >= ?", since.UTC())
	if !until.IsZero() {
		q = q.Where("finished_at < ?", until.UTC())
	}
	if err := q.
		Order("finished_at DESC, id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("load autofocus runs: %w", err)
	}
	for _, r := range rows {
		out.Runs = append(out.Runs, r.run())
	}
	return out, nil
}

func (r row) run() Run {
	run := Run{
		ID:               r.ID,
		ProfileID:        str(r.ProfileID),
		StartedAt:        utcPtr(r.StartedAt),
		FinishedAt:       utc(r.FinishedAt),
		DurationSeconds:  finite(r.DurationSeconds),
		Success:          r.Success != 0,
		Error:            str(r.Error),
		Filter:           str(r.Filter),
		Temperature:      finite(r.Temperature),
		Autofocuser:      str(r.Autofocuser),
		Method:           str(r.Method),
		Fitting:          str(r.Fitting),
		InitialPosition:  finite(r.InitialPosition),
		PreviousPosition: finite(r.PreviousPosition),
		PreviousValue:    finite(r.PreviousValue),
		ChosenPosition:   finite(r.ChosenPosition),
		ChosenValue:      finite(r.ChosenValue),
		TargetName:       str(r.TargetName),
		TargetGUID:       str(r.TargetGUID),
		Source:           r.Source,
	}
	points, err := ParsePoints(r.Points)
	if err != nil {
		run.PointsError = err.Error()
	}
	run.Points = points
	if f := strings.TrimSpace(str(r.Fit)); f != "" && json.Valid([]byte(f)) {
		run.Fit = json.RawMessage(f)
	}
	return run
}

// ParsePoints decodes the points column, dropping points with a position or
// value that is not a finite number. It always returns a non-nil slice.
func ParsePoints(s string) ([]Point, error) {
	out := []Point{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	var raw []struct {
		Position *float64 `json:"position"`
		Value    *float64 `json:"value"`
		Error    *float64 `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return out, fmt.Errorf("points are not a JSON list: %w", err)
	}
	for _, p := range raw {
		pos, val := finite(p.Position), finite(p.Value)
		if pos == nil || val == nil {
			continue
		}
		out = append(out, Point{Position: *pos, Value: *val, Error: finite(p.Error)})
	}
	return out, nil
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := utc(*t)
	return &u
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func finite(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return nil
	}
	v := *p
	return &v
}
