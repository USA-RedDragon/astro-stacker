package app

import "time"

// DiscoverySource is one spot on the sky that changed: the change
// candidates of a target at one place, across nights and filter groups, or
// one moving object's track. Its ID stays the same when a night is compared
// again and its candidates are stored anew. The position, Gaia magnitude,
// night and motion are those of the candidate that started it, so a rerun's
// candidates find it again.
type DiscoverySource struct {
	ID         int    `gorm:"primaryKey;autoIncrement"`
	Object     string `gorm:"not null;index"`
	Kind       string `gorm:"not null"`
	RA         float64
	Dec        float64
	GaiaG      *float64
	Night      time.Time `gorm:"type:date;not null"`
	At         *time.Time
	RateArcmin *float64
	PA         *float64
	Vetting    string `gorm:"not null;default:''"`
	VetNote    string `gorm:"type:text;not null;default:''"`
	VettedAt   *time.Time
	// VetChangedAt is when the vetting was last set or cleared on the
	// source; a candidate's vetting from before then is stale.
	VetChangedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
