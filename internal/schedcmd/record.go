package schedcmd

import (
	"encoding/json"
	"time"
)

type Record struct {
	ID          string          `gorm:"primaryKey" json:"id"`
	Kind        Kind            `gorm:"index;not null" json:"kind"`
	Payload     json.RawMessage `gorm:"type:text;serializer:json" json:"payload"`
	Author      string          `gorm:"not null" json:"author"`
	UndoOf      string          `gorm:"index" json:"undo_of,omitempty"`
	UndoneBy    string          `json:"undone_by,omitempty"`
	Title       string          `gorm:"not null" json:"title"`
	Category    Category        `gorm:"index;not null" json:"category"`
	Destination Destination     `gorm:"not null" json:"destination"`
	Diffs       []Diff          `gorm:"type:text;serializer:json" json:"diffs"`
	Objects     []ObjectRef     `gorm:"type:text;serializer:json" json:"objects"`
	Search      string          `gorm:"type:text" json:"-"`
	Note        string          `json:"note,omitempty"`
	Status      Status          `gorm:"index;not null" json:"status"`
	Message     string          `json:"message,omitempty"`
	Detail      json.RawMessage `gorm:"type:text;serializer:json" json:"detail,omitempty"`
	Transport   string          `json:"transport,omitempty"`
	Attempts    int             `json:"attempts"`
	AppliesAt   *time.Time      `json:"applies_at,omitempty"`
	AppliedAt   *time.Time      `json:"applied_at,omitempty"`
	CreatedAt   time.Time       `gorm:"index" json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (Record) TableName() string { return "scheduler_commands" }

func (r Record) Envelope() Envelope {
	return Envelope{ID: r.ID, Kind: r.Kind, Payload: r.Payload, Author: r.Author, UndoOf: r.UndoOf, CreatedAt: r.CreatedAt}
}

func (r Record) Undoable() bool {
	if r.UndoneBy != "" {
		return false
	}
	switch r.Status {
	case StatusApplied, StatusSaved, StatusQueued, StatusPending:
		return true
	case StatusConflict, StatusRejected, StatusFailed, StatusCancelled:
		return false
	}
	return false
}
