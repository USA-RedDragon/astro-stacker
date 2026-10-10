package observatory

import (
	"encoding/json"
	"time"
)

type Score struct {
	Rule   string  `json:"rule"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
}

type Skip struct {
	Scope     string     `json:"scope"`
	TargetID  int64      `json:"target_id,omitempty"`
	ProjectID int64      `json:"project_id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
}

type Pause struct {
	Mount    string     `json:"mount,omitempty"`
	ResumeAt *time.Time `json:"resume_at,omitempty"`
}

type Target struct {
	ProjectID      int64      `json:"project_id"`
	ProjectName    string     `json:"project_name"`
	ProjectGUID    string     `json:"project_guid,omitempty"`
	TargetID       int64      `json:"target_id"`
	TargetName     string     `json:"target_name"`
	TargetGUID     string     `json:"target_guid,omitempty"`
	Priority       *int       `json:"priority,omitempty"`
	IsMosaic       bool       `json:"is_mosaic"`
	RAHours        *float64   `json:"ra_hours,omitempty"`
	DecDegrees     *float64   `json:"dec_degrees,omitempty"`
	Rotation       *float64   `json:"rotation,omitempty"`
	PickedAt       *time.Time `json:"picked_at,omitempty"`
	MinimumTimeEnd *time.Time `json:"minimum_time_end,omitempty"`
	HardStop       *time.Time `json:"hard_stop,omitempty"`
}

type Exposure struct {
	PlanID    int64      `json:"plan_id,omitempty"`
	Filter    string     `json:"filter"`
	Seconds   float64    `json:"seconds"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	Number    int        `json:"number,omitempty"`
}

type Wait struct {
	Until      *time.Time `json:"until,omitempty"`
	TargetName string     `json:"target_name,omitempty"`
}

type PendingCommand struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
}

type Status struct {
	Version        string           `json:"version,omitempty"`
	Time           *time.Time       `json:"time,omitempty"`
	WebEditing     *bool            `json:"web_editing,omitempty"`
	API            *bool            `json:"api,omitempty"`
	State          string           `json:"state,omitempty"`
	Paused         bool             `json:"paused"`
	PauseRequested bool             `json:"pause_requested"`
	Pause          *Pause           `json:"pause,omitempty"`
	Target         *Target          `json:"target,omitempty"`
	Exposure       *Exposure        `json:"exposure,omitempty"`
	Wait           *Wait            `json:"wait,omitempty"`
	Activity       string           `json:"activity,omitempty"`
	ActivityDetail string           `json:"activity_detail,omitempty"`
	ActivitySince  *time.Time       `json:"activity_since,omitempty"`
	Scores         []Score          `json:"scores,omitempty"`
	ScoreTotal     *float64         `json:"score_total,omitempty"`
	Skips          []Skip           `json:"skips,omitempty"`
	Pending        []PendingCommand `json:"pending,omitempty"`
	LastPlanAt     *time.Time       `json:"last_plan_at,omitempty"`
}

type Override struct {
	Entity string          `json:"entity"`
	ID     int64           `json:"id"`
	Field  string          `json:"field"`
	Value  json.RawMessage `json:"value"`
}

type PreviewRequest struct {
	Start     *time.Time `json:"start,omitempty"`
	Overrides []Override `json:"overrides"`
}

type Reachability string

const (
	Online       Reachability = "online"
	Offline      Reachability = "offline"
	Unconfigured Reachability = "unconfigured"
)

type View struct {
	*Status
	Reachable  Reachability `json:"reachable"`
	LastAnswer *time.Time   `json:"last_answer,omitempty"`
	Since      *time.Time   `json:"since,omitempty"`
	Live       bool         `json:"live"`
	Error      string       `json:"error,omitempty"`
}

type frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
