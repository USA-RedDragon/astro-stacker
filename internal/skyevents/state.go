package skyevents

import "time"

const (
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	StateUndone    = "undone"
)

// PlanState is what became of the user's "Plan it".
type PlanState struct {
	State       string    `json:"state"`
	Priority    string    `json:"priority"`
	ProjectGUID string    `json:"project_guid"`
	TargetGUID  string    `json:"target_guid"`
	CommandID   string    `json:"command_id"`
	PlannedAt   time.Time `json:"planned_at"`
	Message     *string   `json:"message"`
	Automatic   bool      `json:"automatic"`
}

const (
	AutoWaiting  = "waiting"
	AutoPlanned  = "planned"
	AutoDeclined = "declined"
	AutoFailed   = "failed"
	AutoSkipped  = "skipped"
)

// AutoPlan is what the stacker did, or will do, about planning a rare event
// by itself.
type AutoPlan struct {
	State  string     `json:"state"`
	Reason *string    `json:"reason"`
	At     *time.Time `json:"at"`
}
