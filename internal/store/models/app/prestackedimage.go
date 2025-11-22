package app

import "time"

type PreStackedImage struct {
	ID            int       `gorm:"primaryKey;autoIncrement"`
	TargetID      int       `gorm:"not null;index"`
	FilterName    string    `gorm:"not null;index"`
	ImageCount    int       `gorm:"not null"`
	FilePath      string    `gorm:"not null"` // Path in MinIO
	ThumbnailPath string    `gorm:"not null"`
	ProcessedAt   time.Time `gorm:"not null"`
	LastUpdatedAt time.Time `gorm:"not null"`
	FileSize      int64     `gorm:"not null"` // Bytes
}
