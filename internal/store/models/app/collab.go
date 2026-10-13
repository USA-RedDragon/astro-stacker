package app

import "time"

type CollabSwitch struct {
	ID      int `gorm:"primaryKey"`
	Enabled bool
	SetBy   string
	SetAt   time.Time
}
