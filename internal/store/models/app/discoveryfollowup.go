package app

import "time"

// DiscoveryFollowup is a request to image an alerted candidate's field
// again and what came of it. Night is the night the candidate was found;
// Until is the last night the follow-up may be imaged on. The follow-up is
// a project of its own in Target Scheduler, ProjectGUID, with one target
// named like the original so its subs join the same comparisons; one
// project serves every candidate of a field and night.
type DiscoveryFollowup struct {
	ID          int       `gorm:"primaryKey;autoIncrement"`
	AlertID     int       `gorm:"not null;uniqueIndex"`
	Object      string    `gorm:"not null;index"`
	Group       string    `gorm:"column:filter_group;not null"`
	Night       time.Time `gorm:"type:date;not null"`
	Until       time.Time `gorm:"type:date;not null"`
	Kind        string    `gorm:"not null"`
	Class       string    `gorm:"not null"`
	Sign        int
	Mag         *float64
	RA          float64
	Dec         float64
	At          *time.Time
	RateArcmin  *float64
	PA          *float64
	ProjectGUID string `gorm:"index"`
	TargetGUID  string
	Filter      string
	Exposure    float64
	Subs        int
	CreateCmd   string
	CloseCmd    string
	State       string `gorm:"not null;index"`
	Note        string `gorm:"type:text"`
	// Look is why the last later look at the field could not settle it.
	Look        string     `gorm:"type:text"`
	FollowNight *time.Time `gorm:"type:date"`
	FollowRunID *int
	SeenID      *int
	SeenSepArc  *float64
	SeenMag     *float64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
}

// Follow-up states. A request is waiting until a later look at the field
// has been compared, then confirmed or not confirmed; expired when none came
// by Until, failed when the scheduler refused it.
const (
	FollowupWaiting      = "waiting"
	FollowupConfirmed    = "confirmed"
	FollowupNotConfirmed = "not_confirmed"
	FollowupExpired      = "expired"
	FollowupFailed       = "failed"
)
