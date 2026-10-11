package discover_test

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
)

func TestPickShowsTheAutomaticLengths(t *testing.T) {
	t.Parallel()
	s, _ := pickFixture(t)
	p, err := s.Pick(context.Background(), "B33", pickPlans(), snrGoal())
	if err != nil {
		t.Fatal(err)
	}
	if l := filterOf(p, "L"); l.Automatic || len(l.Lengths) != 0 || !strings.Contains(l.LengthBasis, "not measured yet") {
		t.Errorf("without measured lengths: %+v", l)
	}
	mode := sublength.ModeOff
	s.SubLengths = func() *sublength.Result {
		return &sublength.Result{Mode: mode, Site: []sublength.Row{
			{Filter: "Luminance", Gain: 0, Bucket: sublength.BucketDark, Recommendation: sublength.Recommendation{Exposure: 300, Basis: "dark sky"}},
			{Filter: "Luminance", Gain: 0, Bucket: sublength.BucketBright, Recommendation: sublength.Recommendation{Exposure: 120, Basis: "bright moon"}},
			{Filter: "Red", Gain: 0, Bucket: sublength.BucketDark, Recommendation: sublength.Recommendation{Exposure: 600}},
		}}
	}
	p, err = s.Pick(context.Background(), "B33", pickPlans(), snrGoal())
	if err != nil {
		t.Fatal(err)
	}
	if l := filterOf(p, "L"); l.Automatic || !strings.HasPrefix(l.LengthBasis, "Would be chosen per moon glare") || len(l.Lengths) != 2 {
		t.Errorf("publishing off: %+v", l)
	}
	mode = sublength.ModeOn
	p, err = s.Pick(context.Background(), "B33", pickPlans(), snrGoal())
	if err != nil {
		t.Fatal(err)
	}
	l := filterOf(p, "L")
	if !l.Automatic {
		t.Error("publishing on: not automatic")
	}
	if len(l.Lengths) != 2 || l.Lengths[0].Exposure != 300 || l.Lengths[1].Bucket != "bright" || l.Lengths[1].Basis != "bright moon" {
		t.Errorf("lengths %+v", l.Lengths)
	}
	if l.LengthBasis != "Chosen automatically per moon glare from the sky measured at this site: dark 300 s, bright 120 s. Once the target has its own subs, its own sky is used." {
		t.Errorf("basis %q", l.LengthBasis)
	}
	if l.Exposure == nil || *l.Exposure != 120 || l.ExposureBasis != "median of your 3 L subs on reflection and dark nebulae" {
		t.Errorf("the median of past subs stays as it was: %+v", l)
	}
}
