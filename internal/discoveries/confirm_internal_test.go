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
		{"the same change on another night", []PriorChange{{RA: 312.5, Dec: 30.5 + 1.5/3600, Sign: 1, SNR: 9, Night: other, Group: GroupBroadband}}, ClassNewVariable},
		{"the same change in another filter group", []PriorChange{{RA: 312.5, Dec: 30.5, Sign: 1, SNR: 9, Night: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), Group: testNarrowband}}, ClassNewVariable},
		{"the opposite change on another night", []PriorChange{{RA: 312.5, Dec: 30.5, Sign: -1, SNR: 9, Night: other, Group: GroupBroadband}}, ClassLowConfidence},
		{"a change elsewhere", []PriorChange{{RA: 312.5, Dec: 30.5 + 10.0/3600, Sign: 1, SNR: 9, Night: other, Group: testNarrowband}}, ClassLowConfidence},
		{"a weak change on another night", []PriorChange{{RA: 312.5, Dec: 30.5, Sign: 1, SNR: 5.5, Night: other, Group: GroupBroadband}}, ClassLowConfidence},
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
	k := Known{Prior: []PriorChange{{RA: 189.3, Dec: 14.4, Sign: 1, SNR: 9, Night: other, Group: GroupBroadband}}}
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
		ls := classifyChanges(ctx, t, res, k)
		if ls[0].Class != tc.want || asked != tc.asked {
			t.Errorf("%s: %s, ZTF asked %v, want %s, asked %v", tc.name, ls[0].Class, asked, tc.want, tc.asked)
		}
	}
}

func TestZTFIsAskedAboutTheStrongestChangesOnly(t *testing.T) {
	t.Parallel()
	g, dm := 15.0, 0.2
	res := &Result{}
	for i := range maxZTFCandidates + 5 {
		res.Candidates = append(res.Candidates, Candidate{RA: 10 + float64(i)/100, Dec: 41, Sign: -1, SNR: 20 + float64(i), PresentIn: 20, SubsTested: 20, GaiaG: &g, DeltaMag: &dm})
	}
	asked := 0
	k := Known{ZTF: func(context.Context, float64, float64, float64) (ztflc.Verdict, error) {
		asked++
		return ztflc.Verdict{Kind: ztflc.Variable, Amplitude: 0.5, Band: "r"}, nil
	}}
	ls := classifyChanges(context.Background(), t, res, k)
	if asked != maxZTFCandidates || ls[0].Class != ClassLowConfidence || ls[len(ls)-1].Class != ClassNewVariable {
		t.Errorf("asked %d times; weakest %s, strongest %s", asked, ls[0].Class, ls[len(ls)-1].Class)
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
	brighter := Candidate{RA: 323.27678, Dec: 58.63426, Sign: 1, SNR: 64.1, PresentIn: 5, SubsTested: 5, LowConf: true, GaiaG: &g, DeltaMag: &up}
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

func TestZTFAlwaysJudgesAConfirmedNewVariable(t *testing.T) {
	t.Parallel()
	g, dm := 15.0, 0.2
	other := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	res := &Result{}
	for i := range maxZTFCandidates {
		res.Candidates = append(res.Candidates, Candidate{RA: 10 + float64(i)/100, Dec: 41, Sign: -1, SNR: 50 + float64(i), PresentIn: 20, SubsTested: 20, GaiaG: &g, DeltaMag: &dm})
	}
	confirmed := Candidate{RA: 12, Dec: 41, Sign: -1, SNR: 9, PresentIn: 20, SubsTested: 20, GaiaG: &g, DeltaMag: &dm}
	res.Candidates = append(res.Candidates, confirmed)
	k := Known{Prior: []PriorChange{{RA: 12, Dec: 41, Sign: -1, SNR: 9, Night: other, Group: GroupBroadband}},
		ZTF: func(_ context.Context, ra, _, _ float64) (ztflc.Verdict, error) {
			if ra == 12 {
				return ztflc.Verdict{Kind: ztflc.Flat, Amplitude: 0.03, Band: "r"}, nil
			}
			return ztflc.Verdict{Kind: ztflc.Unknown}, nil
		}}
	ls := classifyChanges(context.Background(), t, res, k)
	if c := ls[len(ls)-1]; c.RA != 12 || c.Class != ClassLowConfidence {
		t.Errorf("a confirmed change ZTF shows flat, after %d held ones: %s", maxZTFCandidates, c.Class)
	}
}

func TestSmallOrWeakChangesOnStarsStayHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := 14.1
	other := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	variable := func(context.Context, float64, float64, float64) (ztflc.Verdict, error) {
		return ztflc.Verdict{Kind: ztflc.Variable, Amplitude: 0.3, Band: "r"}, nil
	}
	mag := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name  string
		c     Candidate
		prior []PriorChange
		want  string
	}{
		{"Caldwell 14's 0.08 brightening confirmed the same night", Candidate{Sign: 1, SNR: 62.5, DeltaMag: mag(-0.084), GaiaG: &g}, []PriorChange{{Sign: 1, SNR: 30, Night: other, Group: testNarrowband}}, ClassLowConfidence},
		{"a 0.2 brightening confirmed the same night", Candidate{Sign: 1, SNR: 62.5, DeltaMag: mag(-0.2), GaiaG: &g}, []PriorChange{{Sign: 1, SNR: 30, Night: other, Group: testNarrowband}}, ClassNewVariable},
		{"a 13 sigma fading ZTF calls variable", Candidate{Sign: -1, SNR: 13.5, DeltaMag: mag(0.15), GaiaG: &g}, nil, ClassLowConfidence},
		{"a 0.07 fading at 50 sigma ZTF calls variable", Candidate{Sign: -1, SNR: 50, DeltaMag: mag(0.07), GaiaG: &g}, nil, ClassLowConfidence},
		{"Orion's brightened nebular knot", Candidate{Sign: 1, SNR: 14.7, DeltaMag: mag(-0.189)}, nil, ClassLowConfidence},
		{"a source where nothing was", Candidate{Sign: 1, SNR: 14.7}, nil, ClassNewVariable},
	} {
		tc.c.RA, tc.c.Dec, tc.c.PresentIn, tc.c.SubsTested = 35.2, 57.1, 28, 28
		for i := range tc.prior {
			tc.prior[i].RA, tc.prior[i].Dec = 35.2, 57.1
		}
		if ls := classifyChanges(ctx, t, &Result{Candidates: []Candidate{tc.c}}, Known{Prior: tc.prior, ZTF: variable}); ls[0].Class != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, ls[0].Class, tc.want)
		}
	}
}

func TestANightOfManyNewSourcesHoldsThem(t *testing.T) {
	t.Parallel()
	mk := func(n int) *Result {
		res := &Result{}
		for i := range n {
			res.Candidates = append(res.Candidates, Candidate{RA: 313 + float64(i)/100, Dec: 46.5, Sign: 1, SNR: 20, PresentIn: 7, SubsTested: 7})
		}
		return res
	}
	ctx := context.Background()
	if ls := classifyChanges(ctx, t, mk(maxNewSources), Known{}); ls[0].Class != ClassNewVariable {
		t.Errorf("%d new sources: %s", maxNewSources, ls[0].Class)
	}
	ls := classifyChanges(ctx, t, mk(maxNewSources+1), Known{})
	if ls[0].Class != ClassLowConfidence || ls[0].Matches[len(ls[0].Matches)-1].Catalog != catalogMany {
		t.Errorf("%d new sources: %s %+v", maxNewSources+1, ls[0].Class, ls[0].Matches)
	}
	night := mk(3300)
	for i := range night.Candidates {
		night.Candidates[i].SubSharp = 4.4
		night.Candidates[i].LowConf = true
	}
	for i := range 30 {
		night.Candidates[i].SubSharp, night.Candidates[i].LowConf = 2.5, false
	}
	nova := Candidate{RA: 312.9, Dec: 45.1, Sign: 1, SNR: 15, PresentIn: 7, SubsTested: 7, SubSharp: 1.2, SkyFixed: true}
	night.Candidates = append(night.Candidates, nova)
	ls = classifyChanges(ctx, t, night, Known{})
	held, defects := 0, 0
	for _, l := range ls {
		switch {
		case l.RA == nova.RA:
			if l.Class != ClassNewVariable {
				t.Errorf("the real transient on a night of 3,300 defects: %s %+v", l.Class, l.Matches)
			}
		case l.Class != ClassLowConfidence:
			t.Errorf("a defect stayed %s", l.Class)
		case l.Matches[len(l.Matches)-1].Catalog == catalogMany:
			held++
		case l.Matches[len(l.Matches)-1].Catalog == catalogDefect:
			defects++
		}
	}
	if held != 30 || defects != 3270 {
		t.Errorf("%d held for the night, %d as defect pixels", held, defects)
	}
}
