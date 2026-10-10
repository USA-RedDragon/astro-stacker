package app

import "time"

const (
	MosaicSourceWizard  = "wizard"
	MosaicSourceAdopted = "adopted"
)

type MosaicPanel struct {
	ID          int       `gorm:"primaryKey;autoIncrement" json:"-"`
	ProjectGUID string    `gorm:"not null;uniqueIndex:idx_mosaic_panel_target,priority:1;index" json:"projectGuid"`
	TargetGUID  string    `gorm:"not null;uniqueIndex:idx_mosaic_panel_target,priority:2" json:"targetGuid"`
	Project     string    `json:"project"`
	Target      string    `json:"target"`
	Panel       int       `json:"panel"`
	Row         int       `json:"row"`
	Col         int       `json:"col"`
	RA          float64   `json:"ra"`
	Dec         float64   `json:"dec"`
	Rotation    float64   `json:"rotation"`
	WidthDeg    float64   `json:"widthDeg"`
	HeightDeg   float64   `json:"heightDeg"`
	Footprint   string    `gorm:"type:text" json:"footprint"`
	Neighbours  string    `gorm:"type:text" json:"neighbours"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const (
	AdoptionProposed = "proposed"
	AdoptionAuto     = "auto"
	AdoptionAccepted = "accepted"
	AdoptionRejected = "rejected"
)

type MosaicAdoption struct {
	ID          int        `gorm:"primaryKey;autoIncrement" json:"id"`
	Subject     string     `gorm:"not null;uniqueIndex" json:"subject"`
	ProjectGUID string     `gorm:"index" json:"projectGuid"`
	Project     string     `json:"project"`
	Kind        string     `json:"kind"`
	Confidence  string     `json:"confidence"`
	Issue       string     `gorm:"type:text" json:"issue"`
	Suggestion  string     `gorm:"type:text" json:"suggestion"`
	Proposal    string     `gorm:"type:text" json:"proposal"`
	Fingerprint string     `json:"fingerprint"`
	Status      string     `gorm:"not null;index" json:"status"`
	Clean       bool       `json:"clean"`
	DecidedBy   string     `json:"decidedBy"`
	DecidedAt   *time.Time `json:"decidedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

const (
	LinkHeader        = "header"
	LinkAcquiredImage = "acquiredimage"
	LinkName          = "name"
)

type FrameTarget struct {
	ID          int    `gorm:"primaryKey;autoIncrement"`
	FrameID     int    `gorm:"not null;uniqueIndex"`
	Object      string `gorm:"index"`
	TargetGUID  string `gorm:"not null;index"`
	ProjectGUID string `gorm:"index"`
	Method      string `gorm:"not null"`
	LinkedAt    time.Time
}

type MosaicSeam struct {
	ID          int       `gorm:"primaryKey;autoIncrement" json:"-"`
	Project     string    `gorm:"not null;uniqueIndex:idx_mosaic_seam,priority:1" json:"project"`
	Filter      string    `gorm:"not null;uniqueIndex:idx_mosaic_seam,priority:2" json:"filter"`
	PanelA      int       `gorm:"not null;uniqueIndex:idx_mosaic_seam,priority:3" json:"panelA"`
	PanelB      int       `gorm:"not null;uniqueIndex:idx_mosaic_seam,priority:4" json:"panelB"`
	ProjectGUID string    `gorm:"index" json:"projectGuid"`
	TargetA     string    `json:"targetA"`
	TargetB     string    `json:"targetB"`
	Level       float64   `json:"level"`
	Difference  float64   `json:"difference"`
	SlopeX      float64   `json:"slopeX"`
	SlopeY      float64   `json:"slopeY"`
	Step        float64   `json:"step"`
	Profile     string    `gorm:"type:text" json:"profile"`
	NoiseA      float64   `json:"noiseA"`
	NoiseB      float64   `json:"noiseB"`
	NoiseRatio  float64   `json:"noiseRatio"`
	Samples     int       `json:"samples"`
	OK          bool      `json:"ok"`
	Problems    string    `gorm:"type:text" json:"problems"`
	Signature   string    `json:"signature"`
	MeasuredAt  time.Time `json:"measuredAt"`
}

type MosaicPanelHealth struct {
	ID          int       `gorm:"primaryKey;autoIncrement" json:"-"`
	Project     string    `gorm:"not null;uniqueIndex:idx_mosaic_panel_health,priority:1" json:"project"`
	Filter      string    `gorm:"not null;uniqueIndex:idx_mosaic_panel_health,priority:2" json:"filter"`
	Panel       int       `gorm:"not null;uniqueIndex:idx_mosaic_panel_health,priority:3" json:"panel"`
	ProjectGUID string    `gorm:"index" json:"projectGuid"`
	TargetGUID  string    `json:"targetGuid"`
	Object      string    `json:"object"`
	Noise       float64   `json:"noise"`
	GapFraction float64   `json:"gapFraction"`
	GapDeg2     float64   `json:"gapDeg2"`
	GapWhere    string    `json:"gapWhere"`
	Signature   string    `json:"signature"`
	MeasuredAt  time.Time `json:"measuredAt"`
}
