package app

import "time"

// BinaryBook is what the user keeps about one confirmed variable star:
// its VSX state, notes, chosen name and VSX checklist.
type BinaryBook struct {
	SourceID       int  `gorm:"primaryKey;autoIncrement:false"`
	VSXSubmitted   bool `gorm:"not null;default:false"`
	VSXSubmittedAt *time.Time
	VSXRef         string `gorm:"not null;default:''"`
	Notes          string `gorm:"type:text;not null;default:''"`
	PrimaryName    string `gorm:"not null;default:''"`
	// Checklist is the VSX checklist as JSON {"key": true}.
	Checklist string `gorm:"type:text;not null;default:''"`
	UpdatedAt time.Time
}

// BinaryFacts is what the background job worked out for one confirmed
// variable star from the vet's caches, the catalogs and the masters: JSON
// in Facts, and the files it rendered.
type BinaryFacts struct {
	SourceID int `gorm:"primaryKey;autoIncrement:false"`
	// Version is the rules version that made it; VetRanAt the vet report
	// it was made from.
	Version  int `gorm:"not null;default:0"`
	VetRanAt time.Time
	State    string `gorm:"not null;default:''"`
	Error    string `gorm:"type:text;not null;default:''"`
	Facts    string `gorm:"type:text;not null;default:''"`
	// Files is the rendered files as JSON {"phased": {...}}.
	Files string `gorm:"type:text;not null;default:''"`
	// ChartStamp is the master the finding chart and share picture were
	// made from; a new master makes them again.
	ChartStamp string `gorm:"not null;default:''"`
	ComputedAt time.Time
}

// BinarySetting is one setting of the discovery pages, such as the
// discoverer line for VSX.
type BinarySetting struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"type:text;not null;default:''"`
}

// Eclipse request states.
const (
	EclipseWaiting = "waiting"
	EclipseSent    = "sent"
	EclipseFailed  = "failed"
	EclipseDone    = "done"
	EclipseCancel  = "cancelled"
)

// EclipseRequest asks the scheduler to image one predicted eclipse of a
// confirmed binary inside a time window.
type EclipseRequest struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	SourceID    int    `gorm:"not null;index"`
	Kind        string `gorm:"not null"`
	Mid         time.Time
	Start       time.Time
	End         time.Time
	Filter      string
	Exposure    float64
	Subs        int
	ProjectGUID string `gorm:"index"`
	TargetGUID  string
	CreateCmd   string
	CloseCmd    string
	State       string `gorm:"not null;index"`
	Note        string `gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ShareImage is the annotated picture of one discovery on its target's
// colour picture. Version changes whenever the picture is made again.
type ShareImage struct {
	SourceID  int `gorm:"primaryKey;autoIncrement:false"`
	Key       string
	Stamp     string `gorm:"not null;default:''"`
	Version   int    `gorm:"not null;default:0"`
	State     string `gorm:"not null;default:''"`
	Note      string `gorm:"type:text;not null;default:''"`
	Bytes     int
	UpdatedAt time.Time
}
