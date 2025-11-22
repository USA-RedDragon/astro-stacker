package scheduler

import "encoding/json"

type Epoch int

// Must match NINA's enum
// https://github.com/isbeorn/nina/blob/8e53588ec0d0996e6e41de20d2a116a7036d7ec7/NINA.Astrometry/AstroUtil.cs#L962
const (
	EpochJNOW Epoch = iota
	EpochB1950
	EpochJ2000
	EpochJ2050
)

func (e Epoch) String() string {
	switch e {
	case EpochJNOW:
		return "JNOW"
	case EpochB1950:
		return "B1950"
	case EpochJ2000:
		return "J2000"
	case EpochJ2050:
		return "J2050"
	}
	return "Unknown"
}

func (e Epoch) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

type Target struct {
	ID               int      `json:"id" gorm:"column:Id;primaryKey"`
	Name             string   `json:"name" gorm:"column:name;size:255;not null"`
	Active           int      `json:"active" gorm:"column:active;not null"`
	RA               *float64 `json:"ra" gorm:"column:ra"`
	Dec              *float64 `json:"dec" gorm:"column:dec"`
	EpochCode        Epoch    `json:"epoch_code" gorm:"column:epochcode;not null"`
	Rotation         float64  `json:"rotation" gorm:"column:rotation"`
	RegionOfInterest float64  `json:"region_of_interest" gorm:"column:roi"`
	ProjectID        *int     `json:"project_id" gorm:"column:projectid"`
	UnusedOEO        *string  `json:"-" gorm:"column:unusedOEO;size:255"`
	GUID             *string  `json:"-" gorm:"column:guid;size:255"`
}

func (Target) TableName() string {
	return "target"
}
