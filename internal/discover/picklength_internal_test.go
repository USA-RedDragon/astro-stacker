package discover

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
)

const lumName = "Luminance"

func TestAutomaticLength(t *testing.T) {
	t.Parallel()
	gain := 0
	plans := &planning.Snapshot{Templates: []planning.Template{{ID: 3, Name: "Lum", Filter: "L", Gain: &gain}}}
	lum := func() PickFilter {
		return PickFilter{Filter: "L", Template: &PickTemplate{ID: 3, Name: "Lum", Filter: "L"}}
	}
	s := &Service{}
	pf := lum()
	s.automaticLength(&pf, "L", plans)
	if pf.Automatic || len(pf.Lengths) != 0 || !strings.Contains(pf.LengthBasis, "not measured yet") {
		t.Errorf("without measured lengths: %+v", pf)
	}
	mode := sublength.ModeOff
	s.SubLengths = func() *sublength.Result {
		return &sublength.Result{Mode: mode, Site: []sublength.Row{
			{Filter: lumName, Gain: 0, Bucket: sublength.BucketDark, Recommendation: sublength.Recommendation{Exposure: 300, Basis: "dark sky"}},
			{Filter: lumName, Gain: 0, Bucket: sublength.BucketBright, Recommendation: sublength.Recommendation{Exposure: 120, Basis: "bright moon"}},
			{Filter: lumName, Gain: 100, Bucket: sublength.BucketDark, Recommendation: sublength.Recommendation{Exposure: 600}},
			{Filter: "Red", Gain: 0, Bucket: sublength.BucketDark, Recommendation: sublength.Recommendation{Exposure: 600}},
		}}
	}
	pf = lum()
	s.automaticLength(&pf, "L", plans)
	if pf.Automatic || !strings.HasPrefix(pf.LengthBasis, "Would be chosen per moon glare") || len(pf.Lengths) != 2 {
		t.Errorf("publishing off: %+v", pf)
	}
	mode = sublength.ModeOn
	pf = lum()
	s.automaticLength(&pf, "L", plans)
	if !pf.Automatic || len(pf.Lengths) != 2 || pf.Lengths[0].Exposure != 300 || pf.Lengths[1].Bucket != "bright" || pf.Lengths[1].Basis != "bright moon" {
		t.Errorf("publishing on: %+v", pf)
	}
	if pf.LengthBasis != "Chosen automatically per moon glare from the sky measured at this site: dark 300 s, bright 120 s. Once the target has its own subs, its own sky is used." {
		t.Errorf("basis %q", pf.LengthBasis)
	}
	pf = PickFilter{Filter: "L"}
	s.automaticLength(&pf, "L", nil)
	if len(pf.Lengths) != 2 || pf.Lengths[0].Gain != 0 {
		t.Errorf("no template: one gain's lengths, got %+v", pf.Lengths)
	}
}
