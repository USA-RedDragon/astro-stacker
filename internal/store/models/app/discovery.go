package app

import "time"

// DiscoveryRun is one night of one target compared with the target's other
// nights, for one filter group (broadband, or one narrowband filter).
type DiscoveryRun struct {
	ID            int       `gorm:"primaryKey;autoIncrement"`
	Object        string    `gorm:"not null;uniqueIndex:idx_discovery_run"`
	Night         time.Time `gorm:"type:date;not null;uniqueIndex:idx_discovery_run;index"`
	Group         string    `gorm:"column:filter_group;not null;uniqueIndex:idx_discovery_run"`
	Filters       string
	Method        int
	SubsHash      string
	State         string `gorm:"not null;index"`
	Stage         string
	Error         *string `gorm:"type:text"`
	Attempts      int
	NextTry       *time.Time
	Requested     bool
	NightSubs     int
	RefSubs       int
	RefNights     int
	HFRNight      float64
	HFRRef        float64
	Convolved     string
	ApSigma       float64
	ZeroPoint     *float64
	Depth         *float64
	SubDepth      *float64
	AstroRMS      *float64
	AstroStars    int
	PixelScale    float64
	FieldRA       float64
	FieldDec      float64
	MidTime       *time.Time
	Funnel        string `gorm:"type:text"`
	Checked       string
	Failed        string
	Candidates    int
	NewCandidates int
	Tracks        int
	KnownTracks   int
	BodiesInField int
	SubDetections int
	Seconds       float64
	StartedAt     *time.Time
	FinishedAt    *time.Time
	UpdatedAt     time.Time
}

const (
	DiscoveryQueued  = "queued"
	DiscoveryRunning = "running"
	DiscoveryDone    = "done"
	DiscoveryFailed  = "failed"
	DiscoverySkipped = "skipped"
)

// DiscoveryCandidate is something that changed on a night: a source
// brighter or fainter than the reference (kind change) or a moving object
// linked across the night's subs (kind mover).
type DiscoveryCandidate struct {
	ID       int       `gorm:"primaryKey;autoIncrement"`
	RunID    int       `gorm:"not null;index"`
	Object   string    `gorm:"not null;index"`
	Night    time.Time `gorm:"type:date;not null;index"`
	Group    string    `gorm:"column:filter_group;not null"`
	Kind     string    `gorm:"not null"`
	Class    string    `gorm:"not null;index"`
	X        float64
	Y        float64
	RA       float64
	Dec      float64
	Sign     int
	SNR      float64 `gorm:"index"`
	DiffFlux float64
	RefFlux  float64
	DeltaMag *float64
	NightMag *float64
	RefMag   *float64
	GaiaG    *float64
	Dipole   float64
	// PresentIn of SubsTested of the night's subs show the change.
	PresentIn  int
	SubsTested int
	Matches    string `gorm:"type:text"`
	Light      string `gorm:"type:text"`
	Track      string `gorm:"type:text"`
	RateArcmin *float64
	OffsetArc  *float64
	CutoutKey  string
	Vetting    string `gorm:"not null;default:''"`
	VetNote    string `gorm:"type:text;not null;default:''"`
	VettedAt   *time.Time
	CreatedAt  time.Time
}

const (
	DiscoveryKindChange = "change"
	DiscoveryKindMover  = "mover"
)

// Vetting states.
const (
	VetReal     = "real"
	VetArtifact = "artifact"
	VetKnown    = "known"
	VetUnsure   = "unsure"
)
