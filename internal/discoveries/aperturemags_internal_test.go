package discoveries

import (
	"math"
	"testing"
)

func TestApertureMagsAgree(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		zp, ref, diff, sigma float64
		refMag, nightMag, dm float64
		newLight             float64
	}{
		{"241804 IC 4604 2025-05-20", 10.88573009215695, 0.0017895783664425818, 0.00211449691385496, 0.00011387809731843799,
			17.7538532894235, 16.906934633769566, -0.8469186556539334, 17.57271245309938},
		{"224269 IC 4604 2025-05-21", 10.848656264526007, 0.0013159343184088348, 0.0008550710452254885, 0.00008799573242616175,
			18.05057073180319, 17.507004023492545, -0.5435667083106459, 18.518650763472955},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dm, rm, nm := apertureMags(tc.ref, tc.diff, tc.sigma, &tc.zp)
			if dm == nil || rm == nil || nm == nil {
				t.Fatalf("got nil: dm %v ref %v night %v", dm, rm, nm)
			}
			near := func(what string, got, want float64) {
				if math.Abs(got-want) > 1e-6 {
					t.Errorf("%s = %.6f, want %.6f", what, got, want)
				}
			}
			near("refMag", *rm, tc.refMag)
			near("nightMag", *nm, tc.nightMag)
			near("deltaMag", *dm, tc.dm)
			near("deltaMag against nightMag-refMag", *dm, *nm-*rm)
			added := -2.5 * math.Log10(math.Pow(10, -0.4**nm)-math.Pow(10, -0.4**rm))
			near("added light", added, tc.newLight)
			near("added light from deltaMag", *rm-2.5*math.Log10(math.Pow(10, -0.4**dm)-1), tc.newLight)
		})
	}
	if dm, rm, nm := apertureMags(0.0001, 0.001, 0.0001, nil); dm != nil || rm != nil || nm != nil {
		t.Errorf("reference at 1 sigma and no zero point: got %v %v %v, want all nil", dm, rm, nm)
	}
}
