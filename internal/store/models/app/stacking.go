package app

import "time"

// CalibrationMaster is a master bias, dark or flat built from one set of raw
// frames and stored in the processed bucket.
type CalibrationMaster struct {
	ID int `gorm:"primaryKey;autoIncrement"`
	// SetKey identifies the raw frames it was built from (their keys and
	// ETags), so a changed set gets a new master.
	SetKey    string `gorm:"not null;uniqueIndex"`
	Type      string `gorm:"not null"`
	ObjectKey string `gorm:"not null"`
	Frames    int    `gorm:"not null"`
	BuiltAt   time.Time
	// The setup of the set it was built from, so a light's master can be
	// compared with the set that matches it now (recalibrateDarks). Masters
	// built before these existed have them filled in when next used.
	Night    *time.Time `gorm:"type:date"`
	Filter   string
	Exposure *float64
	Gain     *float64
	Offset   *float64
	SetTemp  *float64
	BinX     *float64
}

// TargetReference is the frame every sub of a target is registered to, so
// all its filters' masters line up.
type TargetReference struct {
	ID        int    `gorm:"primaryKey;autoIncrement"`
	Object    string `gorm:"not null;uniqueIndex"`
	FrameID   int    `gorm:"not null"`
	ObjectKey string `gorm:"not null"` // calibrated reference in the processed bucket
	CreatedAt time.Time
	// WCS is the plate solution of the target's masters, as JSON header
	// cards; SolveAttempts counts tries, SolveError the last failure.
	WCS           *string `gorm:"type:text"`
	SolveAttempts int
	SolveError    *string `gorm:"type:text"`
	// Registration is how subs are registered to it (see
	// stacking.registrationStars); "" for references from before it was
	// recorded, all registered by matching stars.
	Registration string `gorm:"not null;default:''"`
	// SolveRevision is the stacking.solveRevision the reference was last
	// solved for distortion at; a reference that fell back to stars under an
	// older one is tried again.
	SolveRevision int
}

// Stack is the running master for one target and filter.
// Cover is a colour preview of a target, or of a project's mosaics
// (Subject "mosaic:" + project), from its filters' masters.
type Cover struct {
	ID         int    `gorm:"primaryKey;autoIncrement"`
	Subject    string `gorm:"not null;uniqueIndex"`
	Palette    string // RGB+Ha, RGB, SHO or HOO; +OIII when O-III is added to RGB
	PreviewKey string
	Version    int // stacking.CoverVersion it was composed by
	UpdatedAt  time.Time
}

// MosaicSubject is a project's mosaics' Cover subject.
func MosaicSubject(project string) string { return "mosaic:" + project }

// ReferenceReset counts how often a target's registration reference was
// replaced because too many of its subs wouldn't register to it.
type ReferenceReset struct {
	ID     int    `gorm:"primaryKey;autoIncrement"`
	Object string `gorm:"not null;uniqueIndex"`
	Count  int
	At     time.Time
}

// Mosaic is one filter's mosaic of a project's panel masters.
type Mosaic struct {
	ID            int    `gorm:"primaryKey;autoIncrement"`
	Project       string `gorm:"not null;uniqueIndex:idx_mosaic_project_filter"`
	Filter        string `gorm:"not null;uniqueIndex:idx_mosaic_project_filter"`
	ProjectGUID   string `gorm:"index"`
	SeamSignature string
	// Panels is how many panels went in, of PanelsTotal in the project.
	Panels      int
	PanelsTotal int
	// EffectiveSeconds sums the panel masters' score-weighted exposure.
	EffectiveSeconds float64
	NoiseMedian      *float64
	NoiseP90         *float64
	NoiseMax         *float64
	NoiseMaxPanel    int `gorm:"default:0"`
	NoiseTiles       int `gorm:"default:0"`
	NoiseAt          *time.Time
	// Crop is the part of the canvas every panel's data fills, in pixels.
	CropX, CropY, CropW, CropH int
	Width                      int
	Height                     int
	// Signature identifies the panel masters it was built from, so it is
	// rebuilt only when one of them changes.
	Signature  string
	MasterKey  *string
	XISFKey    *string // the mosaic for PixInsight: upright, plate solved
	PreviewKey *string
	LinearKey  *string
	Error      *string `gorm:"type:text"`
	UpdatedAt  time.Time
}

type Stack struct {
	ID     int    `gorm:"primaryKey;autoIncrement"`
	Object string `gorm:"not null;uniqueIndex:idx_stack_target_filter"`
	Filter string `gorm:"not null;uniqueIndex:idx_stack_target_filter"`
	Width  int
	Height int

	Subs int
	// ExposureSeconds is the total exposure of the subs in the master;
	// EffectiveSeconds weights each by its score.
	ExposureSeconds  float64
	EffectiveSeconds float64
	WeightSum        float64
	BackgroundSum    float64
	// ScaleExposure is the exposure the master is scaled to, the longest
	// sub exposure, so it reads like one sub of that length.
	ScaleExposure float64
	// RebuiltAtSubs is the sub count at the last full rebuild.
	RebuiltAtSubs int
	// NeedsRebuild is set when some of its subs are being calibrated again,
	// so the next update rebuilds it without their old versions.
	NeedsRebuild bool
	// Crop is the frame less its ragged, nearly empty borders, in pixels;
	// previews show only this. CropVersion is the rule that made it.
	CropX, CropY, CropW, CropH int
	CropVersion                int
	// MasterVersion is how the master file was written (see
	// stacking.MasterVersion).
	MasterVersion int
	// RejectMethod is the pixel rejection its state was last rebuilt with
	// (see stacking.rejectMethod).
	RejectMethod int
	// GainMethod is how its state was last rebuilt onto one camera gain
	// (see stacking.gainMethod). GainScales is what that rebuild measured
	// for a master mixing gains, as JSON {"gain": scale}; empty for one
	// holding a single gain.
	GainMethod int
	GainScales string `gorm:"type:text"`
	// ScoreMethod is how its subs were last scored (see
	// stacking.scoreMethod); its subs are scored again under a newer one.
	ScoreMethod int

	StateKey   *string // accumulator planes
	MasterKey  *string // linear master FITS for download
	XISFKey    *string // the master for PixInsight: upright, plate solved
	PreviewKey *string // auto-stretched JPEG
	LinearKey  *string // small linear preview for palette mixing in the browser
	// FittedKey is the master, as XISF, linear fitted to FitReference, the filter of
	// its group (colour or narrowband): FitOffset + FitScale×master.
	// FitSignature identifies the target's masters it was fitted from, or
	// tried with when it couldn't be fitted; FitError then says why.
	FittedKey    *string
	FitReference string
	FitOffset    float64
	FitScale     float64
	FitSignature string
	FitError     *string `gorm:"type:text"`
	// Comet* are a comet target's master aligned on the comet instead of
	// the stars; CometSignature identifies the master it was stacked from.
	CometKey                                       *string
	CometXISFKey                                   *string
	CometPreviewKey                                *string
	CometLinearKey                                 *string
	CometSubs                                      int
	CometCropX, CometCropY, CometCropW, CometCropH int
	CometSignature                                 string
	CometError                                     *string `gorm:"type:text"`
	// CometMethod is the stacking.cometMethod the comet master was made
	// by; nil for those made before it was recorded, by an older one.
	CometMethod *int
	UpdatedAt   time.Time
}

// Why a light is or isn't in a master.
const (
	StackStatusAdded        = "added"
	StackStatusLowScore     = "low_score"
	StackStatusUnmeasured   = "unmeasured"
	StackStatusRejected     = "rejected"    // graded as rejected in Target Scheduler
	StackStatusNoMetadata   = "no_metadata" // no scheduler record to score it
	StackStatusCalibration  = "calibration" // no usable flat, dark or bias yet
	StackStatusRegistration = "registration"
	StackStatusFailed       = "failed"
	StackStatusDead         = "dead"        // failed MaxAttempts times; left out until reset
	StackStatusOffTarget    = "off_target"  // the mount pointed elsewhere, and the sub didn't register
	StackStatusRecalibrate  = "recalibrate" // stacked with no dark or a worse one than now matches
	StackStatusMoon         = "moon"        // breaks its filter's moon avoidance
	StackStatusDuplicate    = "duplicate"   // the same file as an earlier light of the target
)

// StackFrame records what happened to one light.
type StackFrame struct {
	ID            int    `gorm:"primaryKey;autoIncrement"`
	FrameID       int    `gorm:"not null;uniqueIndex"`
	StackID       *int   `gorm:"index"`
	Status        string `gorm:"not null;index"`
	Score         float64
	Weight        float64
	Exposure      float64
	RegisteredKey *string
	DarkScale     float64
	Used          int
	Rejected      int
	Saturated     int
	Error         *string `gorm:"type:text"`
	ProcessedAt   time.Time
	// Attempts counts failed tries (failed or registration); NextAttemptAt
	// is when the sub is next picked up, nil for final states.
	Attempts      int
	NextAttemptAt *time.Time `gorm:"index"`
	// NoDark is set when it was calibrated without a dark, none matching.
	NoDark bool
	// BiasMaster, DarkMaster and FlatMaster are the masters it was
	// calibrated with: calibration_masters.set_key, or an imported master's
	// key. They are nil for a light calibrated before they were recorded
	// (it is not known which), and DarkMaster for one calibrated without a
	// dark or delivered calibrated.
	BiasMaster *string
	DarkMaster *string
	FlatMaster *string
}

type RegisteredOrphan struct {
	Key    string `gorm:"primaryKey"`
	SeenAt time.Time
}

// PublicFrame is the frame the public site shows of a target: its newest
// light in a master, rendered small, stretched and watermarked
// (publicframe.Render) and stored in the processed bucket, so serving it
// is only streaming those bytes.
type PublicFrame struct {
	ID      int    `gorm:"primaryKey;autoIncrement"`
	Object  string `gorm:"not null;uniqueIndex"`
	FrameID int    `gorm:"not null"`
	Filter  string
	DateObs *time.Time `gorm:"index"`
	Key     string     `gorm:"not null"` // the JPEG in the processed bucket
	// ETag is the JPEG's, quoted, for conditional requests.
	ETag string `gorm:"not null"`
	// Revision is the publicframe.Revision it was rendered at; frames from
	// an older one are rendered again.
	Revision int
	// Sigma is the frame's sky noise and Amplitude the watermark's peak,
	// in DN of the stretched frame.
	Sigma      float64
	Amplitude  float64
	RenderedAt time.Time
}
