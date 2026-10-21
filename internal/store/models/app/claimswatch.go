package app

import "time"

// ClaimMention is one place outside the app that lists or names a
// confirmed star: a VSX entry, a TNS report, a SIMBAD object or paper, an
// ADS or arXiv paper, an ATel, a Gaia alert or a VizieR table. Baseline
// mentions were already there at the star's first check of that place.
type ClaimMention struct {
	ID        int    `gorm:"primaryKey;autoIncrement"`
	SourceID  int    `gorm:"not null;uniqueIndex:idx_claim_mention"`
	Where     string `gorm:"column:place;not null;uniqueIndex:idx_claim_mention"`
	Key       string `gorm:"not null;uniqueIndex:idx_claim_mention"`
	StarName  string `gorm:"not null;default:''"`
	Title     string `gorm:"type:text;not null;default:''"`
	Detail    string `gorm:"type:text;not null;default:''"`
	URL       string `gorm:"type:text;not null;default:''"`
	Dated     *time.Time
	FirstSeen time.Time `gorm:"not null"`
	LastSeen  time.Time `gorm:"not null"`
	Baseline  bool      `gorm:"not null;default:false"`
	Own       bool      `gorm:"not null;default:false"`
	Mark      string    `gorm:"not null;default:''"`
	MarkedAt  *time.Time
}

// ClaimCheck is the last look at one place for one star.
type ClaimCheck struct {
	SourceID  int    `gorm:"primaryKey;autoIncrement:false"`
	Where     string `gorm:"column:place;primaryKey"`
	State     string `gorm:"not null"`
	Note      string `gorm:"type:text;not null;default:''"`
	CheckedAt time.Time
	// FirstAt is the first look that worked; what it found is the baseline.
	FirstAt *time.Time
}

// ClaimWatchState keeps what a place's check needs between runs, such as
// the newest ATel read or the VizieR tables already seen.
type ClaimWatchState struct {
	Key       string `gorm:"primaryKey"`
	Value     string `gorm:"type:text;not null;default:''"`
	UpdatedAt time.Time
}

// Claim mention marks the user sets, and Own for the user's own entries.
const (
	ClaimMarkSeen   = "seen"
	ClaimMarkCiting = "citing"
)

// Claim check states.
const (
	ClaimChecked    = "checked"
	ClaimFailed     = "failed"
	ClaimNotChecked = "not_checked"
)
