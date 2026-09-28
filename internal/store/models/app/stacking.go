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
}

// Stack is the running master for one target and filter.
// Cover is a colour preview of a target, or of a project's mosaics
// (Subject "mosaic:" + project), from its filters' masters.
type Cover struct {
	ID         int    `gorm:"primaryKey;autoIncrement"`
	Subject    string `gorm:"not null;uniqueIndex"`
	Palette    string // RGB+Ha, RGB, SHO or HOO
	PreviewKey string
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
	ID      int    `gorm:"primaryKey;autoIncrement"`
	Project string `gorm:"not null;uniqueIndex:idx_mosaic_project_filter"`
	Filter  string `gorm:"not null;uniqueIndex:idx_mosaic_project_filter"`
	// Panels is how many panels went in, of PanelsTotal in the project.
	Panels      int
	PanelsTotal int
	// EffectiveSeconds sums the panel masters' score-weighted exposure.
	EffectiveSeconds float64
	// Crop is the part of the canvas every panel's data fills, in pixels.
	CropX, CropY, CropW, CropH int
	Width                      int
	Height                     int
	// Signature identifies the panel masters it was built from, so it is
	// rebuilt only when one of them changes.
	Signature  string
	MasterKey  *string
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
	// Crop is the frame less its ragged, nearly empty borders, in pixels;
	// previews show only this. CropVersion is the rule that made it.
	CropX, CropY, CropW, CropH int
	CropVersion                int
	// MasterVersion is how the master file was written (see
	// stacking.MasterVersion).
	MasterVersion int

	StateKey   *string // accumulator planes
	MasterKey  *string // linear master FITS for download
	PreviewKey *string // auto-stretched JPEG
	LinearKey  *string // small linear preview for palette mixing in the browser
	// FittedKey is the master linear fitted to FitReference, the filter of
	// its group (colour or narrowband) with the most effective exposure: FitOffset + FitScale×master.
	// FitSignature identifies the target's masters it was fitted from.
	FittedKey    *string
	FitReference string
	FitOffset    float64
	FitScale     float64
	FitSignature string
	UpdatedAt    time.Time
}

// Why a light is or isn't in a master.
const (
	StackStatusAdded        = "added"
	StackStatusLowScore     = "low_score"
	StackStatusRejected     = "rejected"    // graded as rejected in Target Scheduler
	StackStatusNoMetadata   = "no_metadata" // no scheduler record to score it
	StackStatusCalibration  = "calibration" // no usable flat, dark or bias yet
	StackStatusRegistration = "registration"
	StackStatusFailed       = "failed"
	StackStatusDead         = "dead"       // failed MaxAttempts times; left out until reset
	StackStatusOffTarget    = "off_target" // the mount pointed elsewhere
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
}
