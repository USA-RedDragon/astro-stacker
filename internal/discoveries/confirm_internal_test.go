package discoveries

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ztflc"
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

func TestZTFLightCurveJudgesANewVariable(t *testing.T) {
	t.Parallel()
	g, dm := 15.8, -0.25
	other := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	res := &Result{Candidates: []Candidate{{RA: 189.3, Dec: 14.4, Sign: 1, SNR: 12, GaiaG: &g, DeltaMag: &dm}}}
	k := Known{Prior: []PriorChange{{RA: 189.3, Dec: 14.4, Sign: 1, Night: other, Group: GroupBroadband}}}
	ctx := context.Background()
	for _, tc := range []struct {
		kind ztflc.Kind
		err  error
		want string
	}{
		{ztflc.Variable, nil, ClassKnownVariable},
		{ztflc.Flat, nil, ClassLowConfidence},
		{ztflc.Unknown, nil, ClassNewVariable},
		{"", ztflc.ErrDown, ClassNewVariable},
	} {
		var asked float64
		k.ZTF = func(_ context.Context, _, _, amp float64) (ztflc.Verdict, error) {
			asked = amp
			return ztflc.Verdict{Kind: tc.kind, Amplitude: 0.06, Band: "r"}, tc.err
		}
		ls, _, checked, failed := Classify(ctx, res, k, nil)
		if ls[0].Class != tc.want || asked != 0.25 {
			t.Errorf("ZTF %q %v: %s (asked about %.2f mag), want %s", tc.kind, tc.err, ls[0].Class, asked, tc.want)
		}
		if (tc.err != nil) != (len(failed) == 1 && failed[0] == catalogZTF) || (tc.err == nil) != (len(checked) == 1) {
			t.Errorf("ZTF %q %v: checked %v failed %v", tc.kind, tc.err, checked, failed)
		}
	}
}
