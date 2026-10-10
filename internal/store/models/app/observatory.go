package app

import "time"

type ObservatoryContact struct {
	ID         int `gorm:"primaryKey"`
	LastAnswer time.Time
}
