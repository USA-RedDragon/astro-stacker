package observatory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const (
	RedeliverInterval   = 15 * time.Second
	QueueResultInterval = 10 * time.Second
)

func every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

type Redeliverer struct {
	Service *schedcmd.Service
	Client  *Client
}

func (r *Redeliverer) Run(ctx context.Context, d time.Duration) {
	every(ctx, d, func(ctx context.Context) { r.Once(ctx) })
}

func (r *Redeliverer) Once(ctx context.Context) {
	r.Service.Redeliver(ctx)
	r.Reconcile(ctx)
}

func (r *Redeliverer) Reconcile(ctx context.Context) {
	if !r.Client.Configured() {
		return
	}
	waiting, err := r.Service.Log.Waiting(ctx)
	if err != nil {
		return
	}
	for _, rec := range waiting {
		if rec.Status != schedcmd.StatusPending || rec.Destination != schedcmd.DestinationObservatory {
			continue
		}
		res, err := r.Client.Command(ctx, rec.ID)
		if errors.Is(err, schedcmd.ErrUnreachable) {
			return
		}
		if err != nil {
			continue
		}
		res.ID = rec.ID
		if _, err := r.Service.ApplyResult(ctx, res); err != nil {
			slog.Warn("Recording a scheduler result failed", "id", rec.ID, "error", err)
		}
	}
}

type QueueResults struct {
	DB      *gorm.DB
	Service *schedcmd.Service
	Table   *TableCheck
}

func NewQueueResults(db *gorm.DB, svc *schedcmd.Service) *QueueResults {
	return &QueueResults{DB: db, Service: svc, Table: &TableCheck{DB: db, Table: schedcmd.QueuedResult{}.TableName()}}
}

func (q *QueueResults) Run(ctx context.Context, d time.Duration) {
	every(ctx, d, func(ctx context.Context) {
		if _, err := q.Once(ctx); err != nil {
			slog.Warn("Reading queued command results failed", "error", err)
		}
	})
}

func (q *QueueResults) Once(ctx context.Context) (int, error) {
	if !q.Table.Exists() {
		return 0, nil
	}
	waiting, err := q.Service.Log.Waiting(ctx)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(waiting))
	for _, r := range waiting {
		if r.Destination == schedcmd.DestinationObservatory {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var rows []schedcmd.QueuedResult
	if err := q.DB.WithContext(ctx).Where("command_id IN ?", ids).Find(&rows).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		res := schedcmd.Result{ID: row.CommandID, Status: schedcmd.Status(row.Status), UpdatedAt: row.UpdatedAt}
		if row.Message != nil {
			res.Message = *row.Message
		}
		if row.Detail != nil && *row.Detail != "" {
			if json.Valid([]byte(*row.Detail)) {
				res.Detail = json.RawMessage(*row.Detail)
			} else if b, err := json.Marshal(*row.Detail); err == nil {
				res.Detail = b
			}
		}
		if _, err := q.Service.ApplyResult(ctx, res); err != nil {
			slog.Warn("Recording a queued command result failed", "id", row.CommandID, "error", err)
			continue
		}
		n++
	}
	return n, nil
}
