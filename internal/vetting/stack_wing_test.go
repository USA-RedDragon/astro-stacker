package vetting

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

const (
	night76482 = "2026-10-05"
	sayWing    = "Artifact: a bright star's wing"
)

// curve76482 is 76482's stack (Orion, 2026-10-05, 9 H-a frames beside a
// G 16.0 star 3.3 px away) as its stored vet measured it: 1.0 sigma in
// the wing fit where a fake of the reported light, magnitude 16.8, shows
// at 5.3, empty places of the plain stack under 0.7 in 99 of 100, and
// 2026-10-06 at 0.2 where the fake shows at 2.5.
func curve76482() (*ownimage.FluxCurve, map[string]reported) {
	light := math.Pow(10, 0.4*(25-16.8))
	e := light / 2.5
	fc := curveOf(ownimage.FluxNight{Night: night76482, Filter: ha, N: 9, Flux: 0.8 * e, Err: e, Local: e, Empty99: 0.7 * e, Flagged: true},
		ownimage.FluxNight{Night: "2026-10-06", Filter: ha, N: 12, Flux: 0.2 * e, Err: e, Local: e, Empty99: 2.6 * e})
	we := light / 5.3
	fc.Wing = &ownimage.Wing{G: 16.0, SepPx: 3.3, Night: night76482, Filter: ha, Excess: we, Sigma: 1.0, Err: we, Floor: we, NightErr: we / 3, N: 9, Far: 9, Measured: true}
	reps := map[string]reported{ha + "\x00" + night76482: {mag: 16.8, ok: true}}
	return fc, reps
}

func TestWingStackIsNotJudgedAgainstPlainControls(t *testing.T) {
	t.Parallel()
	fc, reps := curve76482()
	f := ownimage.StackFacts{Obs: 1.0, Peak: 1.0, Inj: 5.3, Control99: 0.7, HasControl: true}
	wing, _, _ := wingFacts(fc, reps, &f, "")
	if !wing {
		t.Fatal("76482 not in wing mode")
	}
	if v, why := ownimage.JudgeStack(f); v != ownimage.StackRefuted {
		t.Errorf("wing mode 1.0 sigma against a fake at %.1f: %s (%s), want %s", f.Inj, v, why, ownimage.StackRefuted)
	}
}

func TestStored76482IsABrightStarsWing(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/vet76482_report.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(text, &rep); err != nil {
		t.Fatal(err)
	}
	fc, reps := curve76482()
	c, v := transientStack(fc, reps)
	if v != ownimage.StackRefuted || c.Status != check.Fail {
		t.Fatalf("76482's stack: %s %s %s", v, c.Status, c.Evidence)
	}
	replaced := false
	for gi := range rep.Groups {
		for ci := range rep.Groups[gi].Checks {
			if rep.Groups[gi].Checks[ci].ID == check.TransStack {
				rep.Groups[gi].Checks[ci], replaced = c, true
			}
		}
	}
	if !replaced {
		t.Fatal("no stack check in 76482's stored report")
	}
	if s := transientSuggest(rep.Groups, transientFacts{stackV: v}); s.State != app.VetArtifact || s.Say != sayWing {
		t.Errorf("76482: %+v", s)
	}
}
