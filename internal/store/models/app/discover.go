package app

import "time"

const (
	XrefConfirmed = "confirmed"
	XrefRejected  = "rejected"
)

type ObjectXref struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	Subject     string `gorm:"not null;uniqueIndex:idx_object_xref_subject_object"`
	ObjectID    string `gorm:"not null;uniqueIndex:idx_object_xref_subject_object;index"`
	SubjectName string
	ObjectName  string
	Decision    string `gorm:"not null"`
	Method      string
	Confidence  float64
	DecidedAt   time.Time
}

type StarfrontCache struct {
	Key       string `gorm:"primaryKey"`
	Body      string `gorm:"type:text"`
	ETag      string
	FetchedAt time.Time
}
