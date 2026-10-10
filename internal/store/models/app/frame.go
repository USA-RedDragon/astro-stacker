package app

import "time"

// Frame is one image file in the object store, indexed from its header.
// Calibration matching and the per-night coverage view both read this table.
type Frame struct {
	ID           int       `gorm:"primaryKey;autoIncrement"`
	Key          string    `gorm:"not null;uniqueIndex"`
	ETag         string    `gorm:"not null"`
	Size         int64     `gorm:"not null"`
	LastModified time.Time `gorm:"not null"`

	Type           string `gorm:"not null;index:idx_frame_match,priority:1"`
	Object         string `gorm:"index"`
	Filter         string `gorm:"index:idx_frame_match,priority:2"`
	Exposure       *float64
	Gain           *float64 `gorm:"index:idx_frame_match,priority:3"`
	Offset         *float64 `gorm:"index:idx_frame_match,priority:4"`
	SetTemp        *float64
	CCDTemp        *float64
	BinX           *float64
	BinY           *float64
	Rotator        *float64
	Camera         string
	TSProject      *string    `gorm:"column:ts_project"`
	TSTarget       *string    `gorm:"column:ts_target;index"`
	TSExposurePlan *string    `gorm:"column:ts_exposure_plan"`
	TSPanel        *int       `gorm:"column:ts_panel"`
	DateObs        *time.Time `gorm:"index"`
	// MountRA and MountDec are where the mount pointed, in degrees, from
	// the header; PointingRead is set once the header has been checked for
	// them, so frames indexed before these existed get them backfilled.
	// Without RA and DEC keywords the pointing is the centre of the plate
	// solution in the header, or the target's position; PointingRev is the
	// PointingRevision a light without a pointing was last read at, so a
	// new way of finding one reads those again.
	MountRA      *float64
	MountDec     *float64
	PointingRead bool
	PointingWCS  bool // read at revision 1; kept for rows before PointingRev
	PointingRev  int

	Night *time.Time `gorm:"type:date;index"`

	Width        *int
	Height       *int
	FocalLength  *float64
	PixelSize    *float64
	Telescope    *string
	BayerPattern *string
	GeometryRev  *int

	// SkyADU, StarHFR and StarCount are measured from the pixels of lights
	// Target Scheduler has no record of, so they can be scored like the
	// rest; MeasuredAt is nil until then.
	SkyADU     *float64
	StarHFR    *float64
	StarCount  *int
	MeasuredAt *time.Time
	// Photometry is the flux of the light's brightest stars, by rank, as
	// JSON (measure.Photometry), for its transparency; PhotometryRev is the
	// measure.PhotometryRevision it was measured at, nil until then. A light
	// that couldn't be measured has the revision and no Photometry.
	Photometry    *string `gorm:"type:text"`
	PhotometryRev *int
	PhotometryErr *string `gorm:"type:text"`
	// LightLeak is the large-scale spread, in ADU, of a dark rejected by
	// darkcheck (light reached the sensor, or it was off its setpoint);
	// nil when it's clean or unchecked.
	LightLeak *float64
	// DarkSpread is the same measure for every dark measured, clean or
	// rejected; nil until the dark is measured.
	DarkSpread *float64

	CalMedianADU   *float64
	CalSpreadADU   *float64
	CalNoiseADU    *float64
	CalMeasuredAt  *time.Time
	CalCheck       *string `gorm:"index"`
	CalCheckReason *string `gorm:"type:text"`
	ReadoutMode    *string

	// IndexError is set when the header could not be read; the frame is
	// retried when its ETag changes.
	IndexError *string `gorm:"type:text"`
	IndexedAt  time.Time

	// PreviewKey is the auto-stretched JPEG in the processed bucket. The
	// indexer clears it when the frame changes, so it is rendered again.
	PreviewKey   *string
	PreviewError *string `gorm:"type:text"`
	PreviewAt    *time.Time
}
