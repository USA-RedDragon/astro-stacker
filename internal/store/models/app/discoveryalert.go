package app

import "time"

// DiscoveryAlert is one new discovery candidate the Telegram alert has
// covered, kept by night, class and position so a rerun that finds the same
// source again does not alert again. Baseline rows are the candidates that
// were already stored when the alerts were first turned on; they are never
// sent.
type DiscoveryAlert struct {
	ID          int       `gorm:"primaryKey;autoIncrement"`
	MessageID   *int      `gorm:"index"`
	Night       time.Time `gorm:"type:date;not null;index"`
	Class       string    `gorm:"not null"`
	Kind        string    `gorm:"not null"`
	RA          float64
	Dec         float64
	Object      string `gorm:"not null"`
	Group       string `gorm:"column:filter_group;not null"`
	RunID       int    `gorm:"index"`
	CandidateID int
	// At, RateArcmin and PA are a moving object's middle detection time
	// and motion, so the same mover found again on its path is known.
	At         *time.Time
	RateArcmin *float64
	PA         *float64
	Baseline   bool
	Caption    string `gorm:"type:text"`
	Photo      []byte
	CreatedAt  time.Time
}

// DiscoveryAlertMessage is one Telegram message covering a run's new
// candidates, or a summary when a run found too many to list. SendingAt is
// set just before the message goes out; a row with it set and no SentAt may
// have reached Telegram and is never sent again.
type DiscoveryAlertMessage struct {
	ID        int `gorm:"primaryKey;autoIncrement"`
	RunID     int `gorm:"index"`
	Summary   bool
	Count     int
	ClaimedAt time.Time
	SendingAt *time.Time
	SentAt    *time.Time
	Attempts  int
	Error     *string `gorm:"type:text"`
	Text      string  `gorm:"type:text"`
}

// DiscoveryAlertState is the one row that says when the alerts were first
// turned on.
type DiscoveryAlertState struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	SeededAt time.Time
}
