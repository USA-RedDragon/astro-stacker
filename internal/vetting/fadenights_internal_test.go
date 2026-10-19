package vetting

import (
	"slices"
	"testing"
	"time"
)

func TestEveningDates(t *testing.T) {
	t.Parallel()
	jd := func(s string) float64 {
		at, _ := time.Parse(time.RFC3339, s)
		return 2440587.5 + float64(at.Unix())/86400
	}
	got := eveningDates([]float64{jd("2026-10-20T04:00:00Z"), jd("2026-10-20T11:30:00Z"), jd("2026-10-21T03:00:00Z")})
	if want := []string{"2026-10-19", "2026-10-20"}; !slices.Equal(got, want) {
		t.Errorf("evening dates %v, want %v: both points of one night share its evening", got, want)
	}
}
