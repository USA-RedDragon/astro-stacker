package app

import "time"

type HorizonsCache struct {
	Key       string `gorm:"primaryKey"`
	Command   string `gorm:"index"`
	Start     time.Time
	Stop      time.Time `gorm:"index"`
	Step      string
	Result    string `gorm:"type:text"`
	FetchedAt time.Time
}

type CometElements struct {
	Source    string `gorm:"primaryKey"`
	Body      string `gorm:"type:text"`
	FetchedAt time.Time
}
