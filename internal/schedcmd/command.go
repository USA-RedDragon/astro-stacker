package schedcmd

import (
	"encoding/json"
	"errors"
	"time"
)

type Kind string

type Status string

const (
	StatusSaved     Status = "saved"
	StatusQueued    Status = "queued"
	StatusPending   Status = "pending"
	StatusApplied   Status = "applied"
	StatusConflict  Status = "conflict"
	StatusRejected  Status = "rejected"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Final() bool {
	switch s {
	case StatusSaved, StatusApplied, StatusConflict, StatusRejected, StatusFailed, StatusCancelled:
		return true
	case StatusQueued, StatusPending:
		return false
	}
	return false
}

func (s Status) Waiting() bool {
	return s == StatusQueued || s == StatusPending
}

type Destination string

const (
	DestinationObservatory Destination = "observatory"
	DestinationApp         Destination = "app"
)

type Category string

const (
	CategoryControl   Category = "control"
	CategoryPriority  Category = "priority"
	CategoryGoals     Category = "goals"
	CategoryRules     Category = "rules"
	CategoryPlans     Category = "plans"
	CategoryTemplates Category = "templates"
	CategoryCreated   Category = "created"
	CategoryMatching  Category = "matching"
	CategoryAdoption  Category = "adoption"
)

type Envelope struct {
	ID        string          `json:"id"`
	Kind      Kind            `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Author    string          `json:"author"`
	UndoOf    string          `json:"undo_of,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Result struct {
	ID        string          `json:"id"`
	Status    Status          `json:"status"`
	Message   string          `json:"message,omitempty"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	AppliesAt *time.Time      `json:"applies_at,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type ObjectRef struct {
	Entity string `json:"entity"`
	ID     int64  `json:"id,omitempty"`
	GUID   string `json:"guid,omitempty"`
	Name   string `json:"name"`
}

type Diff struct {
	Object ObjectRef       `json:"object"`
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type Description struct {
	Title   string      `json:"title"`
	Diffs   []Diff      `json:"diffs"`
	Objects []ObjectRef `json:"objects"`
	Note    string      `json:"note,omitempty"`
}

var (
	ErrNoInverse   = errors.New("this command cannot be undone")
	ErrUnknownKind = errors.New("unknown command kind")
	ErrInvalid     = errors.New("invalid command payload")
)
