package observatory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const TableCheckTTL = time.Minute

type APITransport struct {
	Client *Client
}

func NewAPITransport(c *Client) *APITransport { return &APITransport{Client: c} }

func (t *APITransport) Name() string { return "api" }

func (t *APITransport) Send(ctx context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	r, err := t.Client.Send(ctx, e)
	if err != nil {
		return r, err
	}
	if r.Status == "" {
		r.Status = schedcmd.StatusPending
	}
	return r, nil
}

func (t *APITransport) Cancel(ctx context.Context, id string) (schedcmd.Result, error) {
	return t.Client.Cancel(ctx, id)
}

type TableCheck struct {
	DB    *gorm.DB
	Table string
	TTL   time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	checked time.Time
	has     bool
}

func (c *TableCheck) Exists() bool {
	if c == nil || c.DB == nil {
		return false
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = TableCheckTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked.IsZero() && now.Sub(c.checked) < ttl {
		return c.has
	}
	c.has = c.DB.Migrator().HasTable(c.Table)
	c.checked = now
	return c.has
}

type QueueTransport struct {
	DB    *gorm.DB
	Table *TableCheck
	Now   func() time.Time
}

func NewQueueTransport(db *gorm.DB) *QueueTransport {
	return &QueueTransport{DB: db, Table: &TableCheck{DB: db, Table: schedcmd.QueuedCommand{}.TableName()}}
}

var errNotQueued = errors.New("command is not in ts_command")

func (t *QueueTransport) Name() string { return "queue" }

func (t *QueueTransport) now() time.Time {
	if t.Now != nil {
		return t.Now().UTC()
	}
	return time.Now().UTC()
}

func (t *QueueTransport) Send(ctx context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	if !t.Table.Exists() {
		return schedcmd.Result{}, unreachable(errors.New("ts_command does not exist"))
	}
	row := schedcmd.QueuedCommand{
		ID:        e.ID,
		Kind:      string(e.Kind),
		Payload:   string(e.Payload),
		Author:    e.Author,
		CreatedAt: e.CreatedAt.UTC(),
	}
	if row.Payload == "" {
		row.Payload = "{}"
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = t.now()
	}
	if e.UndoOf != "" {
		u := e.UndoOf
		row.UndoOf = &u
	}
	if err := t.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return schedcmd.Result{}, unreachable(fmt.Errorf("queue %s: %w", e.ID, err))
	}
	return schedcmd.Result{ID: e.ID, Status: schedcmd.StatusQueued, UpdatedAt: t.now()}, nil
}

func (t *QueueTransport) Cancel(ctx context.Context, id string) (schedcmd.Result, error) {
	if !t.Table.Exists() {
		return schedcmd.Result{}, unreachable(errors.New("ts_command does not exist"))
	}
	res := t.DB.WithContext(ctx).Model(&schedcmd.QueuedCommand{}).Where("id = ?", id).Update("cancelled", 1)
	if res.Error != nil {
		return schedcmd.Result{}, unreachable(res.Error)
	}
	if res.RowsAffected == 0 {
		return schedcmd.Result{}, errNotQueued
	}
	return schedcmd.Result{ID: id, Status: schedcmd.StatusCancelled, UpdatedAt: t.now()}, nil
}
