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
	Key   string `gorm:"column:name;primaryKey"`
	Value string `gorm:"type:text;not null;default:''"`
}

// Eclipse request states.
const (
	EclipseWaiting = "waiting"
	EclipseSent    = "sent"
	EclipseFailed  = "failed"
	EclipseDone    = "done"
	EclipseCancel  = "cancelled"
	// EclipseMoved is a request withdrawn because newer survey data moved
	// its eclipse out of its window; the eclipse is asked for again at its
	// new time.
	EclipseMoved = "moved"
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
	// Attempt counts earlier requests of the same eclipse that failed or
	// were cancelled; each attempt is a project of its own.
	Attempt int `gorm:"not null;default:0"`
	// Auto says the app asked for it to time the star's eclipses each
	// season, not the user.
	Auto      bool `gorm:"not null;default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Eclipse timing states.
const (
	TimingGood     = "good"
	TimingDoubtful = "doubtful"
	TimingNone     = "none"
)

// EclipseTiming is one eclipse of a confirmed binary timed in the user's
// own frames: the middle in BJD TDB with its one sigma error in days, or,
// with State none, why the frames gave no middle.
type EclipseTiming struct {
	ID       int    `gorm:"primaryKey;autoIncrement"`
	SourceID int    `gorm:"not null;index"`
	Kind     string `gorm:"not null"`
	// Predicted is the ephemeris's middle, in UTC, when it was measured.
	Predicted time.Time
	// Night is the local date the night began.
	Night  string `gorm:"not null;default:''"`
	Filter string `gorm:"not null;default:''"`
	BJD    float64
	Err    float64
	Method string `gorm:"not null;default:''"`
	// CheckBJD and CheckErr are the second method's middle and error, 0
	// when it could not run.
	CheckBJD float64
	CheckErr float64
	Depth    float64
	Points   int
	// Frames is how many frames covered the eclipse when it was measured;
	// more frames later measure it again.
	Frames    int
	State     string `gorm:"not null;index"`
	Note      string `gorm:"type:text;not null;default:''"`
	RequestID *int
	// Version is the timing rules' version; an older one is measured
	// again.
	Version    int `gorm:"not null;default:0"`
	MeasuredAt time.Time
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
