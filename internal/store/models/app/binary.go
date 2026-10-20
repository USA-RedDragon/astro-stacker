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
	// StarType is the kind the user named a confirmed star that is not an
	// eclipsing binary, a binaries.StarType key.
	StarType string `gorm:"not null;default:''"`
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
	// Settle is what is still owed the scheduler once the project is no
	// longer wanted, withdraw or close, empty once done.
	Settle string `gorm:"not null;default:''"`
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

// DipWatch links a scheduler project that watches a dipping star through
// one window to the star's discovery source, so the Now page can show
// the window and the depths of the star's past dips.
type DipWatch struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	SourceID    int    `gorm:"not null;index"`
	ProjectGUID string `gorm:"not null;uniqueIndex"`
	// Start and End bound the window a dip can come in.
	Start time.Time
	End   time.Time
	// DepthLow and DepthHigh span the depths of the star's past dips, in
	// magnitudes.
	DepthLow  float64
	DepthHigh float64
	// ActiveShare is the share of past windows that held a dip, 0 when
	// not known.
	ActiveShare float64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DipperEphemeris is the dip cycle of a confirmed dipper as worked out
// from its past dips, the record of those dips, and whether the app asks
// for imaging on every window the site can see.
type DipperEphemeris struct {
	SourceID int `gorm:"primaryKey;autoIncrement:false"`
	// Period is the cycle in days and EpochMJD the middle of one window,
	// a UTC modified Julian date.
	Period   float64
	EpochMJD float64
	// WindowHours is how wide the window a dip can come in is.
	WindowHours float64
	// DepthLow and DepthHigh span the past dips' depths in magnitudes,
	// DurLowHours and DurHighHours their lengths.
	DepthLow     float64
	DepthHigh    float64
	DurLowHours  float64
	DurHighHours float64
	// ActiveShare is the share of windows that hold a dip, 0 when not
	// known.
	ActiveShare float64
	// PastSurveys names the surveys of the past record, "ZTF, ATLAS and
	// TESS"; PastDips counts its dips over PastYears years, in PastActive
	// of PastCovered windows the surveys covered.
	PastSurveys string `gorm:"not null;default:''"`
	PastDips    int    `gorm:"not null;default:0"`
	PastYears   int    `gorm:"not null;default:0"`
	PastActive  int    `gorm:"not null;default:0"`
	PastCovered int    `gorm:"not null;default:0"`
	// EveryWindow says the app asks for imaging on every window the site
	// can see.
	EveryWindow bool `gorm:"not null;default:false"`
	UpdatedAt   time.Time
}

// Dip window result states.
const (
	DipSeen   = "dip"
	DipNone   = "no-dip"
	DipUnsure = "unsure"
)

// DipResult is what the user's own frames show in one past window of a
// dipper: how many subs and how much time covered it, and whether the
// star dipped.
type DipResult struct {
	ID       int `gorm:"primaryKey;autoIncrement"`
	SourceID int `gorm:"not null;uniqueIndex:idx_dip_result"`
	Cycle    int `gorm:"not null;uniqueIndex:idx_dip_result"`
	Mid      time.Time
	// Subs and Seconds are the subs of the star's field inside the
	// window and their exposure; Measured is how many gave a point.
	Subs     int
	Seconds  float64
	Measured int
	State    string `gorm:"not null;default:''"`
	Note     string `gorm:"type:text;not null;default:''"`
	// Depth is the dip's depth in magnitudes when one was seen, and
	// Smallest the least dip the subs could show.
	Depth      float64
	Smallest   float64
	MeasuredAt time.Time
}
