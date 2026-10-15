package app

import "time"

// DiscoveryVet is the automatic vetting of one discovery source: its state
// and, when done, the report as JSON. InputHash names the candidates and
// survey answers it saw, so a changed source or a survey answer that
// arrived later is vetted again.
type DiscoveryVet struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	SourceID    int    `gorm:"not null;uniqueIndex"`
	State       string `gorm:"not null;index"`
	Version     int    `gorm:"not null;default:0"`
	InputHash   string `gorm:"not null;default:''"`
	Suggested   string `gorm:"not null;default:''"`
	Confidence  string `gorm:"not null;default:''"`
	Pending     string `gorm:"not null;default:''"`
	Report      string `gorm:"type:text;not null;default:''"`
	Error       string `gorm:"type:text;not null;default:''"`
	RequestedAt *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
