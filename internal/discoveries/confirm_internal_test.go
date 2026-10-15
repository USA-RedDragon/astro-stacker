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
		{ztflc.Variable, nil, ClassNewVariable},
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

func TestZTFLightCurveConfirmsAOneNightChangeOnAStar(t *testing.T) {
	t.Parallel()
	g, dm := 15.0, 0.24
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		snr     float64
		subs    int
		lowConf bool
		kind    ztflc.Kind
		want    string
		asked   bool
	}{
		{"varies in ZTF", 113, 28, false, ztflc.Variable, ClassNewVariable, true},
		{"flat in ZTF", 113, 28, false, ztflc.Flat, ClassLowConfidence, true},
		{"ZTF cannot tell", 113, 28, false, ztflc.Unknown, ClassLowConfidence, true},
		{"strong in five subs, varies in ZTF", 40, 5, true, ztflc.Variable, ClassNewVariable, true},
		{"in three subs", 40, 3, true, ztflc.Variable, ClassLowConfidence, false},
		{"weak", 6, 28, true, ztflc.Variable, ClassLowConfidence, false},
		{"within the null test", 9, 28, false, ztflc.Variable, ClassLowConfidence, false},
		{"in three of five subs", 17.3, 5, true, ztflc.Variable, ClassLowConfidence, false},
	} {
		present := tc.subs
		if tc.name == "in three of five subs" {
			present = 3
		}
		res := &Result{Candidates: []Candidate{{RA: 10.88928, Dec: 41.66243, Sign: -1, SNR: tc.snr, PresentIn: present, SubsTested: tc.subs, LowConf: tc.lowConf, GaiaG: &g, DeltaMag: &dm}}}
		res.ChangeNull.MaxSNR = 9.5
		asked := false
		k := Known{ZTF: func(context.Context, float64, float64, float64) (ztflc.Verdict, error) {
			asked = true
			return ztflc.Verdict{Kind: tc.kind, Amplitude: 0.53, Band: "r"}, nil
		}}
		ls, _, _, _ := Classify(ctx, res, k, nil)
		if ls[0].Class != tc.want || asked != tc.asked {
			t.Errorf("%s: %s, ZTF asked %v, want %s, asked %v", tc.name, ls[0].Class, asked, tc.want, tc.asked)
		}
	}
}

func TestHeldChangesAreFadingsAskedOfTheBrokers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, up, down := 14.85, -0.061, 0.163
	variable := func(asked *int) ZTFLookup {
		return func(context.Context, float64, float64, float64) (ztflc.Verdict, error) {
			*asked++
			return ztflc.Verdict{Kind: ztflc.Variable, Amplitude: 0.48, Band: "g"}, nil
		}
	}
	asked := 0
	brighter := Candidate{RA: 323.27678, Dec: 58.63426, Sign: 1, SNR: 17.3, PresentIn: 5, SubsTested: 5, LowConf: true, GaiaG: &g, DeltaMag: &up}
	if ls := classifyChanges(ctx, t, &Result{Candidates: []Candidate{brighter}}, Known{ZTF: variable(&asked)}); ls[0].Class != ClassLowConfidence || asked != 0 {
		t.Errorf("a one-night brightening: %s, ZTF asked %d times", ls[0].Class, asked)
	}
	fainter := brighter
	fainter.Sign, fainter.DeltaMag = -1, &down
	tns := func(context.Context, float64, float64, float64) ([]Alert, []string, []string) {
		return []Alert{{Source: sourceTNS, ID: "AT 2025xyz", SepArc: 0.4}}, []string{sourceAlerce, sourceFink, sourceTNS}, nil
	}
	ls, _, checked, _ := Classify(ctx, &Result{Candidates: []Candidate{fainter}}, Known{ZTF: variable(&asked)}, tns)
	if ls[0].Class != ClassKnownTransient || len(checked) < 3 {
		t.Errorf("a held fading TNS lists: %s, checked %v", ls[0].Class, checked)
	}
}
