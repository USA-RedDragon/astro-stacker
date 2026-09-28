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
	Night    *time.Time `gorm:"type:date;index"`

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
