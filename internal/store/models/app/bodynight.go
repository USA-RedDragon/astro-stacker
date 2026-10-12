package app

import "time"

type BodyNight struct {
	ID             int       `gorm:"primaryKey;autoIncrement"`
	Object         string    `gorm:"not null;uniqueIndex:idx_body_night,priority:1"`
	Filter         string    `gorm:"not null;uniqueIndex:idx_body_night,priority:2"`
	Night          time.Time `gorm:"type:date;not null;uniqueIndex:idx_body_night,priority:3"`
	StackID        int       `gorm:"index"`
	Kind           string
	Signature      string
	Method         int
	Subs           int
	EffectiveHours float64
	RefTime        *time.Time
	HeadX          *float64
	HeadY          *float64
	TailPA         *float64
	TailDX         *float64
	TailDY         *float64
	ArcsecPerPx    float64
	Longitude      *float64
	TrailPx        *float64
	RegionBins     int
	Levels         int
	Signal         float64
	NoiseA         float64
	NoiseB         float64
	NoiseNow       float64
	SNR            float64
	HeadSNR        *float64
	TailSNR        *float64
	GainPerHourPct float64
	LowConfidence  bool
	LowReason      string
	Saturated      bool `gorm:"default:false"`
	ImageSignature string
	ImageError     *string `gorm:"type:text"`
	MasterKey      *string
	StarlessKey    *string
	PreviewKey     *string
	Error          *string `gorm:"type:text"`
	MeasuredAt     time.Time
	UpdatedAt      time.Time
}
