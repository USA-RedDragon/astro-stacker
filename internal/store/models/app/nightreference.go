package app

import "time"

type NightReference struct {
	ID            int       `gorm:"primaryKey;autoIncrement"`
	Object        string    `gorm:"not null;uniqueIndex:idx_night_reference,priority:1"`
	Night         time.Time `gorm:"type:date;not null;uniqueIndex:idx_night_reference,priority:2"`
	FrameID       int       `gorm:"not null"`
	ObjectKey     string    `gorm:"not null"`
	WCS           *string   `gorm:"type:text"`
	SolveAttempts int
	SolveError    *string `gorm:"type:text"`
	SolvedAt      *time.Time
	CreatedAt     time.Time
}
