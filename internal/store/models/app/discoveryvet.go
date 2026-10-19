package app

import "time"

// DiscoveryVet is the automatic vetting of one discovery source: its state
// and, when done, the report as JSON. InputHash names the candidates and
// survey answers it saw, so a changed source or a survey answer that
// arrived later is vetted again. Pipeline and Build name the code that
// wrote the report.
type DiscoveryVet struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	SourceID    int    `gorm:"not null;uniqueIndex"`
	State       string `gorm:"not null;index"`
	Version     int    `gorm:"not null;default:0"`
	InputHash   string `gorm:"not null;default:''"`
	Pipeline    string `gorm:"not null;default:''"`
	Build       string `gorm:"not null;default:''"`
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

// VetBuild is the vetting code the newest vetting run announced: its
// build and the pipeline versions it vets with. A run from an older
// build that is still going reads it before it saves, and stops instead
// of writing verdicts from code a newer deploy replaced.
type VetBuild struct {
	ID          int    `gorm:"primaryKey"`
	Build       string `gorm:"not null;default:''"`
	Pipelines   string `gorm:"not null;default:''"`
	AnnouncedAt time.Time
}
