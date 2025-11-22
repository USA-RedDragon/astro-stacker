package app

import "time"

type ImageProcessingStatus string

const (
	StatusPending    ImageProcessingStatus = "pending"
	StatusDownloaded ImageProcessingStatus = "downloaded"
	StatusProcessing ImageProcessingStatus = "processing"
	StatusStacked    ImageProcessingStatus = "stacked"
	StatusFailed     ImageProcessingStatus = "failed"
)

func (ips ImageProcessingStatus) IsValid() bool {
	switch ips {
	case StatusPending, StatusDownloaded, StatusProcessing, StatusStacked, StatusFailed:
		return true
	default:
		return false
	}
}

type ImageProcess struct {
	ID                int                   `gorm:"primaryKey;autoIncrement"`
	AcquiredImageID   int                   `gorm:"not null;uniqueIndex"`
	Status            ImageProcessingStatus `gorm:"not null;index"`
	FilePath          string                `gorm:"not null"`
	Error             *string               `gorm:"type:text"`
	DownloadedAt      *time.Time
	ProcessedAt       *time.Time
	PreStackedImageID *int `gorm:"index"`

	PreStackedImage *PreStackedImage `gorm:"foreignKey:PreStackedImageID"`
}
