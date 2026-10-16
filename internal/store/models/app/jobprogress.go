package app

import "time"

// JobProgress is the progress a batch Job's worker writes for the
// processing indicator; Report is an activity.Report as JSON.
type JobProgress struct {
	Kind      string `gorm:"primaryKey"`
	Worker    string `gorm:"primaryKey"`
	Report    string `gorm:"type:text;not null;default:''"`
	UpdatedAt time.Time
}
