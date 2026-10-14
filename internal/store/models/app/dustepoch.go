package app

import "time"

// DustEpoch starts a dust epoch: from its first night on, the dust shadows
// on the sensor differ from the nights before, so flats are not taken across
// it (calmatch.Epochs). Rows are edited by hand as new dust is dated.
type DustEpoch struct {
	ID    int       `gorm:"primaryKey;autoIncrement"`
	Start time.Time `gorm:"type:date;not null;uniqueIndex"`
	// Fallback is what lights of the epoch get when it has no flat in
	// their filter: calmatch.FallbackPrevious, calmatch.FallbackNext or ""
	// for the nearest night.
	Fallback string `gorm:"not null;default:''"`
	// Note says what changed, how the boundary was dated and why the
	// fallback was chosen.
	Note string `gorm:"type:text;not null;default:''"`
}

func (DustEpoch) TableName() string { return "dust_epochs" }
