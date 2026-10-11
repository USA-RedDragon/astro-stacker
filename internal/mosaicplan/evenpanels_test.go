package mosaicplan_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func TestStrategyInForceFollowsPanelDeficitAndEvenPanels(t *testing.T) {
	t.Parallel()
	rig := newSeasonsRig(t)
	cases := []struct {
		setup      string
		even, on   bool
		strategy   mosaics.Strategy
		defaultsTo mosaics.Strategy
	}{
		{`UPDATE ruleweight SET weight = 75 WHERE name = 'Panel Deficit' AND projectid = 1`, false, true, mosaics.StrategyWeakest, mosaics.StrategyWeakest},
		{`INSERT INTO ruleweight (name, weight, projectid) VALUES ('Even Panels', 100, 1)`, true, true, mosaics.StrategyEven, mosaics.StrategyEven},
		{`UPDATE ruleweight SET weight = 0 WHERE name = 'Panel Deficit' AND projectid = 1`, true, false, mosaics.StrategyOff, mosaics.StrategyOff},
		{`UPDATE ruleweight SET weight = 0 WHERE name = 'Even Panels' AND projectid = 1`, false, false, mosaics.StrategyOff, mosaics.StrategyOff},
	}
	for _, tc := range cases {
		plan := rig.plan(tc.setup, nil)
		d, err := rig.svc.Detail(context.Background(), markarian)
		if err != nil {
			t.Fatal(err)
		}
		if d.Balancing.EvenPanels != tc.even || d.Balancing.On != tc.on || plan.StrategyInForce != string(tc.strategy) || plan.Strategy != string(tc.defaultsTo) {
			t.Errorf("%s: balancing %+v, in force %s, strategy %s", tc.setup, d.Balancing, plan.StrategyInForce, plan.Strategy)
		}
	}
}
