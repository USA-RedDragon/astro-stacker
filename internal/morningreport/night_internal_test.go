package morningreport

import (
	"testing"
	"time"
)

const (
	night1013 = "2026-10-13"
	night1014 = "2026-10-14"
	filterL   = "Luminance"
	noLog     = "no log"
	filterR   = "Red"
	due1015   = "2026-10-15T11:00:00-05:00"
	objM15    = "M 15"
	objLagoon = "Lagoon"
)

func chicago(t *testing.T) Schedule {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return Schedule{Loc: loc, Hour: 11}
}

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestWindowRunsFromDueTimeToDueTime(t *testing.T) {
	t.Parallel()
	s := chicago(t)
	cases := []struct {
		night string
		start string
		end   string
		hours float64
	}{
		{night1013, "2026-10-13T16:00:00Z", "2026-10-14T16:00:00Z", 24},
		// Daylight saving ends on Sunday 2026-11-01: the night is an hour longer.
		{"2026-10-31", "2026-10-31T16:00:00Z", "2026-11-01T17:00:00Z", 25},
		// It starts on Sunday 2027-03-14: the night is an hour shorter.
		{"2027-03-13", "2027-03-13T17:00:00Z", "2027-03-14T16:00:00Z", 23},
	}
	for _, c := range cases {
		start, end := s.Window(day(c.night))
		if got := start.UTC().Format(time.RFC3339); got != c.start {
			t.Errorf("%s: start %s, want %s", c.night, got, c.start)
		}
		if got := end.UTC().Format(time.RFC3339); got != c.end {
			t.Errorf("%s: end %s, want %s", c.night, got, c.end)
		}
		if h := end.Sub(start).Hours(); h != c.hours {
			t.Errorf("%s: %v hours, want %v", c.night, h, c.hours)
		}
		if start.In(s.Loc).Hour() != 11 || end.In(s.Loc).Hour() != 11 {
			t.Errorf("%s: window %s to %s is not 11:00 to 11:00 local", c.night, start.In(s.Loc), end.In(s.Loc))
		}
	}
}

func TestDueIsTheNightThatEndedAtTheLastDueTime(t *testing.T) {
	t.Parallel()
	s := chicago(t)
	cases := []struct {
		now   string
		night string
		due   string
	}{
		{"2026-10-15T10:59:59-05:00", night1013, "2026-10-14T11:00:00-05:00"},
		{due1015, night1014, due1015},
		{"2026-10-15T23:59:00-05:00", night1014, due1015},
		{"2026-10-16T00:30:00-05:00", night1014, due1015},
		{"2026-11-01T11:00:00-06:00", "2026-10-31", "2026-11-01T11:00:00-06:00"},
	}
	for _, c := range cases {
		now, err := time.Parse(time.RFC3339, c.now)
		if err != nil {
			t.Fatal(err)
		}
		night, due := s.Due(now)
		if got := NightKey(night); got != c.night {
			t.Errorf("at %s: night %s, want %s", c.now, got, c.night)
		}
		want, _ := time.Parse(time.RFC3339, c.due)
		if !due.Equal(want) {
			t.Errorf("at %s: due %s, want %s", c.now, due, want)
		}
		if _, end := s.Window(night); !end.Equal(due) {
			t.Errorf("at %s: the night's window ends %s, not when its report is due %s", c.now, end, due)
		}
	}
}

func TestNextCrossesDaylightSavingAt11(t *testing.T) {
	t.Parallel()
	s := chicago(t)
	now, _ := time.Parse(time.RFC3339, "2026-10-31T12:00:00-05:00")
	next := s.Next(now)
	if want, _ := time.Parse(time.RFC3339, "2026-11-01T11:00:00-06:00"); !next.Equal(want) {
		t.Fatalf("next %s, want %s", next, want)
	}
	if got := next.Sub(now); got != 24*time.Hour {
		t.Fatalf("waits %s across the change, want 24h", got)
	}
	before, _ := time.Parse(time.RFC3339, "2026-10-14T09:00:00-05:00")
	if want, _ := time.Parse(time.RFC3339, "2026-10-14T11:00:00-05:00"); !s.Next(before).Equal(want) {
		t.Fatalf("next from 09:00 is %s, want 11:00 the same day", s.Next(before))
	}
}

func TestSameDay(t *testing.T) {
	t.Parallel()
	s := chicago(t)
	due, _ := time.Parse(time.RFC3339, due1015)
	late, _ := time.Parse(time.RFC3339, "2026-10-15T23:59:59-05:00")
	next, _ := time.Parse(time.RFC3339, "2026-10-16T00:00:00-05:00")
	if !s.SameDay(due, late) {
		t.Error("23:59 is the same day")
	}
	if s.SameDay(due, next) {
		t.Error("midnight is the next day")
	}
}
