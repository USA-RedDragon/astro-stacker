// Package morningreport builds the observatory's morning report on the night
// that just ended and sends it to Telegram once a day.
package morningreport

import (
	"time"
	// The image has no zoneinfo; the report's time zone loads from this.
	_ "time/tzdata"
)

// Schedule is when a report is due: Hour:Minute in Loc, every day.
type Schedule struct {
	Loc    *time.Location
	Hour   int
	Minute int
}

// at is the due time on the civil date of day, in s.Loc.
func (s Schedule) at(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), s.Hour, s.Minute, 0, 0, s.Loc)
}

// Window is the span a night's report covers: from the due time on the
// night's evening date to the due time the next day, so consecutive reports
// leave no gap. A night is named by its evening's civil date.
func (s Schedule) Window(night time.Time) (start, end time.Time) {
	return s.at(night), s.at(night.AddDate(0, 0, 1))
}

// Due is the night whose report came due most recently at or before now, and
// when it came due.
func (s Schedule) Due(now time.Time) (night, dueAt time.Time) {
	local := now.In(s.Loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	dueAt = s.at(today)
	if local.Before(dueAt) {
		today = today.AddDate(0, 0, -1)
		dueAt = s.at(today)
	}
	return today.AddDate(0, 0, -1), dueAt
}

// Next is the first due time after now.
func (s Schedule) Next(now time.Time) time.Time {
	_, due := s.Due(now)
	d := due.In(s.Loc)
	return s.at(time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, time.UTC))
}

// SameDay reports whether now is still on the civil date the report was due,
// the only day a missed report is sent late.
func (s Schedule) SameDay(dueAt, now time.Time) bool {
	a, b := dueAt.In(s.Loc), now.In(s.Loc)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// NightKey is a night's civil date as YYYY-MM-DD.
func NightKey(night time.Time) string { return night.Format(time.DateOnly) }
