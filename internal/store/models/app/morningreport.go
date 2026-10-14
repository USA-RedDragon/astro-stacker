package app

import "time"

// MorningReport records a night's morning report so it goes out once. Night
// is the evening's date, YYYY-MM-DD. SendingAt is set just before the
// message goes to Telegram; a row with it set and no SentAt may have gone
// out and is never sent again. A row with neither SendingAt nor an Error
// whose claim is older than the claim lease was left by a send that
// stopped while rendering and is tried again.
type MorningReport struct {
	Night     string `gorm:"primaryKey"`
	ClaimedAt time.Time
	SendingAt *time.Time
	SentAt    *time.Time
	Attempts  int
	Error     *string `gorm:"type:text"`
	Text      string  `gorm:"type:text"`
}
