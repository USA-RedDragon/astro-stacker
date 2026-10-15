package app

import "time"

// DiscoveryReport keeps what the report page of a find adds to it: the
// forced photometry on earlier nights at its place, the TNS search, what
// was sent to TNS and the name TNS gave it. It is keyed by object, night,
// class and position, so it outlives a rerun that stores the candidate
// again under a new id.
type DiscoveryReport struct {
	ID          int       `gorm:"primaryKey;autoIncrement"`
	Object      string    `gorm:"not null;index"`
	Night       time.Time `gorm:"type:date;not null"`
	Class       string    `gorm:"not null"`
	RA          float64
	Dec         float64
	SpotState   string
	SpotError   string `gorm:"type:text"`
	Spot        string `gorm:"type:text"`
	SpotAt      *time.Time
	TNSFound    *string `gorm:"type:text"`
	TNSError    string  `gorm:"type:text"`
	TNSAt       *time.Time
	TNSName     string
	SentAt      *time.Time
	SentReport  string `gorm:"type:text"`
	SentReply   string `gorm:"type:text"`
	SentReplyID string
	// FollowupSentAt and the rest record the photometry report sent for a
	// find TNS already has.
	FollowupSentAt  *time.Time
	FollowupReport  string `gorm:"type:text"`
	FollowupReply   string `gorm:"type:text"`
	FollowupReplyID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
