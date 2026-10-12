package app

import "time"

// SkyEventRun is the latest event search, kept so the page has events
// right after a restart.
type SkyEventRun struct {
	ID         int `gorm:"primaryKey"`
	ComputedAt time.Time
	Result     string `gorm:"type:text"`
}

// SkyEventChoice is what the user chose for one event: its priority, and
// the project "Plan it" created.
type SkyEventChoice struct {
	EventID       string `gorm:"primaryKey"`
	Bodies        string
	Objects       string
	Priority      string
	ProjectGUID   string
	TargetGUID    string
	TargetName    string
	CommandID     string
	WindowCommand string
	Designation   string
	Moving        bool
	IgnoreMoon    bool
	WindowStart   *time.Time
	WindowEnd     *time.Time
	PlannedAt     *time.Time
	UpdatedAt     time.Time
	// AutoPlannedAt is when the stacker claimed the event to plan it by
	// itself; it is never cleared once a command was sent, so an event is
	// planned automatically at most once.
	AutoPlannedAt  *time.Time
	AutoCommandID  *string
	AutoDeclinedAt *time.Time
	AutoError      *string
}
