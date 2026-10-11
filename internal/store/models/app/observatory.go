package app

import "time"

type ObservatoryContact struct {
	ID         int `gorm:"primaryKey"`
	LastAnswer time.Time
}

// ObservatoryHorizon is the last custom horizon the observatory scheduler
// reported for a NINA profile, kept so it survives restarts while the
// observatory PC is offline.
type ObservatoryHorizon struct {
	ProfileID   string    `gorm:"primaryKey"`
	Custom      bool      `gorm:"not null;default:false"`
	AzimuthStep float64   `gorm:"not null;default:0"`
	Altitudes   []float64 `gorm:"serializer:json"`
	FetchedAt   time.Time `gorm:"not null;index"`
}
