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

func TestProgressCarriesTheFurthestPlateau(t *testing.T) {
	t.Parallel()
	one, near, far := 1.0, 0.6, 0.07
	judged := map[string]planning.Judgement{
		p1: {Target: p1, Least: planning.Least{Target: p1, Enough: true, Progress: &one, Filter: "L", Filters: 2,
			Plateau: planning.LeastPlateau{Target: p1, Filter: "H-a", Progress: &far, Filters: 2}}},
		p2: {Target: p2, Least: planning.Least{Target: p2, Progress: &near, Filter: "G", Filters: 1,
			Plateau: planning.LeastPlateau{Target: p2, Filter: "G", Progress: &near, Filters: 1, Unmeasured: 0}}},
	}
	p := progressOf(judged, []string{p1, p2})
	if p.Target != p2 || p.Plateau.Target != p1 || p.Plateau.Filter != "H-a" || *p.Plateau.Progress != far || p.Plateau.Filters != 3 {
		t.Fatalf("%+v %+v", p, p.Plateau)
	}
}
