package discover

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
)

const (
	p1 = "Panel 1"
	p2 = "Panel 2"
)

func TestJudgeSkipsTargetsWithoutEnabledPlans(t *testing.T) {
	t.Parallel()
	one := 1.0
	judged := map[string]planning.Judgement{
		p1: {Target: p1, Done: true, Least: planning.Least{Target: p1, Enough: true, Progress: &one, Filter: "L", Filters: 1}},
		p2: {Target: p2, Least: planning.Least{Target: p2, NotMeasured: planning.NoPlansReason}},
	}
	targets := []string{p2, p1}
	if c, text, _ := judge(Tally{Filters: 1}, judged, targets); c != CompletionDone {
		t.Fatalf("completion %s: %s", c, text)
	}
	p := progressOf(judged, targets)
	if p == nil || p.Target != p1 || !p.Enough || p.Percent == nil || *p.Percent != 1 {
		t.Fatalf("progress %+v", p)
	}
	only := map[string]planning.Judgement{p2: judged[p2]}
	if c, _, _ := judge(Tally{}, only, []string{p2}); c == CompletionDone {
		t.Fatal("a subject whose only target has no enabled plans was judged done")
	}
}
