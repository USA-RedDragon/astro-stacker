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
}
