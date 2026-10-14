package app

import "time"

// DustFlat is one flat set's dust check: its dust map, its catalog of dust
// shadows and how it compared with the flat before it.
type DustFlat struct {
	ID int `gorm:"primaryKey;autoIncrement"`
	// SetKey is the calibration_masters.set_key of the flat's master.
	SetKey  string     `gorm:"not null;uniqueIndex"`
	Night   *time.Time `gorm:"type:date;index"`
	Object  string
	Filter  string `gorm:"not null;index"`
	Rotator *float64
	// TakenAt is the DATE-OBS of the set's first frame.
	TakenAt time.Time `gorm:"not null;index"`
	Frames  int
	// GroupKey identifies the set among calibration sets (see
	// stacking.dustGroupKey), so a set is checked once.
	GroupKey string `gorm:"not null;index"`
	// MapKey is the smoothed dust map in the processed bucket.
	MapKey string `gorm:"not null"`
	// Width and Height are the map's, in map pixels; flats are compared
	// only with flats of the same size.
	Width, Height int
	// Sigma is the map's noise, as a fraction.
	Sigma float64
	// Catalog is the set's dust shadows, as JSON dustcheck.Blob.
	Catalog string `gorm:"type:text"`
	// PrevID is the flat it was compared with, nil for the first of its
	// filter; RotMatch says whether at the same rotator angle.
	PrevID    *int
	RotMatch  bool
	DiffSigma float64
	// Version is the dustcheck.Version it was checked with, 0 until its
	// comparison is recorded, so a failed check is done again.
	Version   int
	CheckedAt time.Time
}

// DustEvent is one blob in the difference between a flat and the one
// before it of its filter. Class says what it is; only new, gone and moved
// are dust changes, the rest are kept to show what was passed over.
type DustEvent struct {
	ID     int `gorm:"primaryKey;autoIncrement"`
	FlatID int `gorm:"not null;index"`
	PrevID int `gorm:"not null"`
	Filter string
	// TakenAt is the newer flat's; Night its night.
	TakenAt time.Time  `gorm:"not null;index"`
	Night   *time.Time `gorm:"type:date;index"`
	Class   string     `gorm:"not null"`
	// Scope is window, filter or unknown for a change (dustcheck.Scope*),
	// empty otherwise; Filters lists the filters the change shows in that
	// night, and GroupKey ties one change's events across filters.
	Scope    string
	Filters  string
	GroupKey string `gorm:"index"`
	X, Y     int
	Diameter int
	// Depth is the peak change in percent, negative for a new shadow.
	Depth              float64
	Edge, Sharp        float64
	PartnerX, PartnerY int
	RotMatch           bool
	DiffSigma          float64
	// CropKey is the PNG of the previous flat, the new one and their
	// difference around the change, in the processed bucket.
	CropKey    *string
	DetectedAt time.Time
}

// DustLightSub is one calibrated light's 4×4 binned copy, kept for its
// night's lights check.
type DustLightSub struct {
	ID         int       `gorm:"primaryKey;autoIncrement"`
	FrameID    int       `gorm:"not null;uniqueIndex"`
	Night      time.Time `gorm:"type:date;not null;index"`
	Object     string
	Filter     string
	FlatMaster string
	Key        string `gorm:"not null"`
	CreatedAt  time.Time
}

// DustLightCheck is one night's lights check: the lights' own flat divided
// by the flats they were calibrated with, and the shadows left over.
type DustLightCheck struct {
	ID     int       `gorm:"primaryKey;autoIncrement"`
	Night  time.Time `gorm:"type:date;not null;uniqueIndex"`
	Subs   int
	Fields int
	// Status is checked, too_few_subs, one_field, noisy or failed
	// (dustcheck.Lights*); a failed night is tried again when its subs
	// change.
	Status string
	// ResidualKey is the residual map in the processed bucket, PreviewKey
	// a stretched PNG of it.
	ResidualKey *string
	PreviewKey  *string
	// Discs are the residual shadows (negative) and brightenings, as JSON
	// dustcheck.Blob; Deepest is the largest residual depth in percent.
	Discs     string `gorm:"type:text"`
	Deepest   float64
	Sigma     float64
	CheckedAt time.Time
}
