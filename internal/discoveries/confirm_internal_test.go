package discoveries

import (
	"context"
	"testing"
	"time"
)

const testNarrowband = "H-a"

func TestOneNightsChangeOnAStarIsHeldUntilConfirmed(t *testing.T) {
	t.Parallel()
	g := 15.8
	c := Candidate{RA: 312.5, Dec: 30.5, Sign: 1, SNR: 9, GaiaG: &g}
	res := &Result{Candidates: []Candidate{c}}
	ctx := context.Background()
	other := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		prior []PriorChange
		want  string
	}{
		{"one night alone", nil, ClassLowConfidence},
		{"the same change on another night", []PriorChange{{RA: 312.5, Dec: 30.5 + 1.5/3600, Sign: 1, Night: other, Group: GroupBroadband}}, ClassNewVariable},
		{"the same change in another filter group", []PriorChange{{RA: 312.5, Dec: 30.5, Sign: 1, Night: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), Group: testNarrowband}}, ClassNewVariable},
		{"the opposite change on another night", []PriorChange{{RA: 312.5, Dec: 30.5, Sign: -1, Night: other, Group: GroupBroadband}}, ClassLowConfidence},
		{"a change elsewhere", []PriorChange{{RA: 312.5, Dec: 30.5 + 10.0/3600, Sign: 1, Night: other, Group: testNarrowband}}, ClassLowConfidence},
	} {
		ls := classifyChanges(ctx, t, res, Known{Prior: tc.prior})
		if ls[0].Class != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, ls[0].Class, tc.want)
		}
	}
	res.Candidates[0].GaiaG = nil
	if ls := classifyChanges(ctx, t, res, Known{}); ls[0].Class != ClassNewVariable {
		t.Errorf("a source where no star was before is not held for another night: %s", ls[0].Class)
	}
}
