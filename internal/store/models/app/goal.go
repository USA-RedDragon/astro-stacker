package app

import "time"

type GoalMeasurement struct {
	ID             int    `gorm:"primaryKey;autoIncrement"`
	Object         string `gorm:"not null;uniqueIndex:idx_goal_measurement_object_filter"`
	Filter         string `gorm:"not null;uniqueIndex:idx_goal_measurement_object_filter"`
	TargetGUID     string `gorm:"index"`
	MethodRevision int
	Subs           int
	Levels         int
	EffectiveHours float64
	Sky            float64
	Signal         float64
	NoiseA         float64
	NoiseB         float64
	NoiseNow       float64
	SNR            float64
	Depth          *float64
	ZeroPoint      *float64
	GainPerHourPct float64
	HeldOutErrPct  *float64
	LowConfidence  bool
	RegionHash     string
	BandFraction   float64
	Points         string  `gorm:"type:text"`
	Error          *string `gorm:"type:text"`
	MeasuredAt     time.Time
	SubsTotal      int
	NebFraction    float64
	NoiseMask      string
	LowReason      string
	PixelScale     float64
	ZeroPointStars int
	Seconds        float64
}

type GaiaField struct {
	ID        int    `gorm:"primaryKey;autoIncrement"`
	Object    string `gorm:"not null;uniqueIndex"`
	RA        float64
	Dec       float64
	Radius    float64
	Stars     string `gorm:"type:text"`
	FetchedAt time.Time
}
