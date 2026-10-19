package discoveries

import (
	"context"
	"testing"
)

// TestCatalogEntryFarFainterThanTheChangeIsNotIt replays real changes: 45489
// (M 22, 2021-06-29) lost 12.5 mag of light 4.5" from OGLE-BLG-ECL-423358,
// which peaks at 17.19; 27526 (Leo Triplet, 2026-02-19) is a 16.8 mag source
// 3.0" from (669493) 2012 XB93 at V 21.2; 48319 (Crab Nebula, 2026-09-08)
// brightened a G 16.0 star by 18.7 mag of light 0.9" from 2014 HY97 at V
// 24.6. (112) Iphigenia at V 13.65, 0.07" from 39200's 13.04 mag change (IC
// 4604 Panel 6, 2025-07-28), and (3080) Moisseiev at V 17.09, 0.04" from
// 27869's 18.49 mag change (IC 3393 Panel 2, 2026-01-18), stay known.
func TestCatalogEntryFarFainterThanTheChangeIsNotIt(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	const ra, dec = 150.0, 10.0
	for _, tc := range []struct {
		name      string
		c         Candidate
		v         *Variable
		b         *Body
		sep       float64
		wantClass string
	}{
		{"45489 OGLE-BLG-ECL-423358", Candidate{Sign: -1, SNR: 2051.4, NightMag: f(18.24), RefMag: f(12.52), DeltaMag: f(4.84), GaiaG: f(19.37)},
			&Variable{Name: "OGLE-BLG-ECL-423358", Type: "E", Max: 17.189, Min: 0.84, Amplitude: true, Band: "Ic"}, nil, 4.51, ClassLowConfidence},
		{"27526 2012 XB93", Candidate{Sign: 1, SNR: 45.9, NightMag: f(16.80)}, nil, &Body{Name: "2012 XB93", Number: "669493", V: 21.24}, 3.01, ""},
		{"48319 2014 HY97", Candidate{Sign: 1, SNR: 6.1, NightMag: f(15.71), RefMag: f(15.78), DeltaMag: f(-0.06), GaiaG: f(16.02)}, nil, &Body{Name: "2014 HY97", V: 24.6}, 0.9, ClassLowConfidence},
		{"39200 Iphigenia", Candidate{Sign: 1, SNR: 20, PresentIn: 8, SubsTested: 8, NightMag: f(13.04)}, nil, &Body{Name: "Iphigenia", Number: "112", V: 13.65}, 0.07, ClassKnownAsteroid},
		{"27869 Moisseiev", Candidate{Sign: 1, SNR: 20, PresentIn: 8, SubsTested: 8, NightMag: f(18.49)}, nil, &Body{Name: "Moisseiev", Number: "3080", V: 17.09}, 0.04, ClassKnownAsteroid},
		{"variable at its own light", Candidate{Sign: 1, SNR: 20, PresentIn: 8, SubsTested: 8, NightMag: f(16.9), RefMag: f(17.4), DeltaMag: f(-0.5), GaiaG: f(17.3)},
			&Variable{Name: "V1 Tst", Type: "EW", Max: 17.2, Min: 17.8}, nil, 1.0, ClassKnownVariable},
	} {
		c := tc.c
		c.RA, c.Dec = ra, dec
		var k Known
		if tc.v != nil {
			v := *tc.v
			v.RA, v.Dec = ra, dec+tc.sep/3600
			k.Variables = []Variable{v}
		}
		if tc.b != nil {
			b := *tc.b
			b.RA, b.Dec = ra, dec+tc.sep/3600
			k.Bodies = []Body{b}
		}
		ls := classifyChanges(context.Background(), t, &Result{Candidates: []Candidate{c}}, k)
		got := ls[0].Class
		if tc.wantClass == "" {
			if got == ClassKnownAsteroid || got == ClassKnownVariable {
				t.Errorf("%s: %s %+v", tc.name, got, ls[0].Matches)
			}
			continue
		}
		if got != tc.wantClass {
			t.Errorf("%s: %s, want %s %+v", tc.name, got, tc.wantClass, ls[0].Matches)
		}
	}
}
