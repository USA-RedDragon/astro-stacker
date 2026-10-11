package app

import "time"

const (
	CalibrationFlats = "flats"
	CalibrationDarks = "darks"
	CalibrationBias  = "bias"
)

const (
	CalibrationOpen      = "open"
	CalibrationDone      = "done"
	CalibrationCancelled = "cancelled"
)

// CalibrationRequest asks the observatory for flats, darks or bias frames
// for one object's lights of one night. It is done once the frames are
// indexed.
type CalibrationRequest struct {
	ID          string    `gorm:"primaryKey"`
	Type        string    `gorm:"not null;index"`
	Object      string    `gorm:"not null;index"`
	TargetID    *int      `gorm:"column:target_id"`
	Night       time.Time `gorm:"type:date;not null"`
	Status      string    `gorm:"not null;index"`
	CreatedAt   time.Time `gorm:"not null;index"`
	CreatedBy   string
	DoneAt      *time.Time
	CancelledAt *time.Time
	CancelledBy *string
}

func (CalibrationRequest) TableName() string { return "calibration_requests" }
