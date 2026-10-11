package app

import "time"

// GlobalRuleWeight is one weight of the global rule weight set that every
// Target Scheduler project is kept equal to.
type GlobalRuleWeight struct {
	Name    string    `gorm:"primaryKey" json:"name"`
	Weight  float64   `gorm:"not null" json:"weight"`
	SavedBy string    `json:"savedBy"`
	SavedAt time.Time `json:"savedAt"`
}
