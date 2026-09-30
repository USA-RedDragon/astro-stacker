package app

import "time"

// What became of a verdict sent to Target Scheduler.
const (
	TSVerdictSent       = "sent"       // written; Target Scheduler does not show it yet
	TSVerdictApplied    = "applied"    // Target Scheduler shows it
	TSVerdictMoot       = "moot"       // Target Scheduler rejected the sub itself
	TSVerdictOverridden = "overridden" // someone put the sub back after it was applied; left alone
)

// Verdicts, as Target Scheduler's grading statuses: reject a sub, or accept
// again one the stacker rejected.
const (
	TSVerdictAccept = 1
	TSVerdictReject = 2
)

// TSVerdict is the stacker's record of a verdict on one of Target
// Scheduler's acquired images, sent through the scheduler database's
// stacker_verdict table (see stacking.SendVerdicts). The row there only
// carries the verdict to the observatory; this one remembers what came of
// it, which the observatory's trigger must never see.
type TSVerdict struct {
	AcquiredImageID int `gorm:"primaryKey;autoIncrement:false"`
	FrameID         int `gorm:"not null;index"`
	ExposurePlanID  int `gorm:"not null;index"`
	Verdict         int `gorm:"not null"`
	Reason          string
	State           string `gorm:"not null;index"`
	// Attempts counts sends; NextAttemptAt is when a verdict not yet
	// applied is sent again.
	Attempts      int
	SentAt        time.Time
	NextAttemptAt *time.Time
	AppliedAt     *time.Time
	UpdatedAt     time.Time
}

// TableName keeps the table name clear of gorm's pluralisation of "TS".
func (TSVerdict) TableName() string { return "ts_verdicts" }
