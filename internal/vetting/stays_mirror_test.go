package vetting

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// TestStaysMirror is source 299: Barnard's E's 2026-10-10 and 2026-10-13
// are each other's only Red and Green reference, so the change sitting at
// the same spot on both nights is one measurement, not a repeat.
func TestStaysMirror(t *testing.T) {
	t.Parallel()
	s := &Service{Bodies: func(context.Context, float64, float64, float64, time.Time) ([]discoveries.Body, error) {
		return nil, nil
	}}
	sum := discoverysource.Summary{Source: app.DiscoverySource{RA: 293.8657, Dec: 11.0485}, Members: []app.DiscoveryCandidate{
		{RunID: 1, Night: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		{RunID: 10, Night: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
	}}
	for _, c := range []struct {
		name  string
		curve *ownimage.Curve
		want  string
	}{
		{"mirrored", &ownimage.Curve{FlaggedNights: 2, Detections: 1}, "No known asteroid or comet was near it."},
		{"independent", &ownimage.Curve{FlaggedNights: 2, Detections: 2}, "No known asteroid or comet was near it and the change sat at the same spot on all 2 nights."},
		{"no curve", nil, "No known asteroid or comet was near it and the change sat at the same spot on all 2 nights."},
	} {
		if got := s.movingCheck(context.Background(), sum, nil, surveys.Fetch{}, c.curve).Evidence; got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
