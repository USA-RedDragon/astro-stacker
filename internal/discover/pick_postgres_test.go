package discover_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestPickOnPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "stacker", "stacker")
	ctx := t.Context()
	s := pickFixtureOn(t, db)
	p, err := s.Pick(ctx, "M16", pickPlans(), noGoal())
	if err != nil {
		t.Fatal(err)
	}
	if p.Palette != hooSII || len(p.Rules) != 1 || !p.Rules[0].Derived {
		t.Fatalf("pick %+v", p)
	}
	ha := filterOf(p, "H-α")
	if ha.Exposure == nil || *ha.Exposure != 300 || ha.Subs != 7 || ha.Hours.Hours != nil || ha.Hours.Unknown == "" || ha.Hours.Points != 4 {
		t.Errorf("H-α %+v", ha)
	}
	g, err := s.Pick(ctx, "M81", pickPlans(), depthGoal())
	if err != nil {
		t.Fatal(err)
	}
	l := filterOf(g, "L")
	if l.Hours.Hours == nil || math.IsNaN(*l.Hours.Hours) || l.Hours.Points != 1 || l.Subs != 13 {
		t.Errorf("L %+v", l)
	}
	d, err := s.Pick(ctx, "B33", pickPlans(), noGoal())
	if err != nil {
		t.Fatal(err)
	}
	if l := filterOf(d, "L"); l.Exposure == nil || *l.Exposure != 120 {
		t.Errorf("dust L %+v", l)
	}
}
