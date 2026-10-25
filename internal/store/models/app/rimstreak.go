package app

import "time"

// RimStreak is one light's dust rim streak measurement
// (dustcheck.RimStreak) and the share of its weight it keeps for it.
type RimStreak struct {
	ID      int       `gorm:"primaryKey;autoIncrement"`
	FrameID int       `gorm:"not null;uniqueIndex"`
	Night   time.Time `gorm:"type:date;not null;index"`
	Object  string
	Filter  string
	// Depth is in percent of the sky, negative for a dark streak; Z how
	// far it stands out from the rim's noise. X and Y are its shadow's
	// centre and Angle and Radius where on its rim it lies, in sensor
	// pixels and degrees.
	Depth  float64
	Z      float64
	X, Y   int
	Angle  float64
	Radius int
	// Factor is the share of its weight the light keeps (0: left out);
	// Note says why, in plain words, empty for a light that keeps it all.
	Factor float64
	Note   string `gorm:"type:text"`
	// Version is the stacking.RimVersion it was measured with.
	Version    int
	MeasuredAt time.Time
}
