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

	Type     string `gorm:"not null;index:idx_frame_match,priority:1"`
	Object   string `gorm:"index"`
	Filter   string `gorm:"index:idx_frame_match,priority:2"`
	Exposure *float64
	Gain     *float64 `gorm:"index:idx_frame_match,priority:3"`
	Offset   *float64 `gorm:"index:idx_frame_match,priority:4"`
	SetTemp  *float64
	CCDTemp  *float64
	BinX     *float64
	BinY     *float64
	Rotator  *float64
	Camera   string
	DateObs  *time.Time `gorm:"index"`
	// MountRA and MountDec are where the mount pointed, in degrees, from
	// the header; PointingRead is set once the header has been checked for
	// them, so frames indexed before these existed get them backfilled.
	MountRA      *float64
	MountDec     *float64
	PointingRead bool
	Night        *time.Time `gorm:"type:date;index"`
	// SkyADU, StarHFR and StarCount are measured from the pixels of lights
	// Target Scheduler has no record of, so they can be scored like the
	// rest; MeasuredAt is nil until then.
	SkyADU     *float64
	StarHFR    *float64
	StarCount  *int
	MeasuredAt *time.Time
	// LightLeak is the large-scale spread, in ADU, of a dark left out of
	// its master because light reached the sensor; nil when it's clean or
	// unchecked.
	LightLeak *float64
	// DarkSpread is the same measure for every dark checked, clean or
	// leaky, so a clean dark can be told from one never checked (nil):
	// darks are checked only when a master is built from them, and those
	// checked before this was recorded have nil.
	DarkSpread *float64

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
