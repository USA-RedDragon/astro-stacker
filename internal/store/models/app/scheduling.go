package app

import "time"

// GlobalSchedulingSetting is the one saved row of the global scheduling
// settings that every Target Scheduler project is kept equal to.
type GlobalSchedulingSetting struct {
	ID                    int       `gorm:"primaryKey;autoIncrement:false" json:"-"`
	DitherEvery           int       `gorm:"not null" json:"ditherEvery"`
	FilterSwitchFrequency int       `gorm:"not null" json:"filterSwitchFrequency"`
	FlatsHandling         int       `gorm:"not null" json:"flatsHandling"`
	SmartExposureOrder    bool      `gorm:"not null" json:"smartExposureOrder"`
	MeridianWindow        int       `gorm:"not null" json:"meridianWindow"`
	MaximumAltitude       float64   `gorm:"not null" json:"maximumAltitude"`
	EnableGrader          bool      `gorm:"not null" json:"enableGrader"`
	SavedBy               string    `json:"savedBy"`
	SavedAt               time.Time `json:"savedAt"`
}

// SchedulingOriginal is a project's own scheduling values from before the
// stacker first overwrote them, kept so they can be put back.
type SchedulingOriginal struct {
	ProjectGUID string    `gorm:"primaryKey" json:"projectGuid"`
	ProjectID   int       `json:"projectId"`
	Name        string    `json:"name"`
	Values      string    `gorm:"type:text;not null" json:"values"`
	SavedAt     time.Time `json:"savedAt"`
}
