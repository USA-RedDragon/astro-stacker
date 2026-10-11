package mosaicplan_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func TestStrategyInForceFollowsPanelDeficitAndEvenPanels(t *testing.T) {
	t.Parallel()
	rig := newSeasonsRig(t)
	cases := []struct {
		setup      string
		even, on   bool
		panels     int
		spreads    bool
		strategy   mosaics.Strategy
		defaultsTo mosaics.Strategy
	}{
		{`UPDATE ruleweight SET weight = 75 WHERE name = 'Panel Deficit' AND projectid = 1`, false, true, 2, false, mosaics.StrategyWeakest, mosaics.StrategyWeakest},
		{`INSERT INTO ruleweight (name, weight, projectid) VALUES ('Even Panels', 100, 1)`, true, true, 2, false, mosaics.StrategyWeakest, mosaics.StrategyWeakest},
		{fivePanels(), true, true, 5, true, mosaics.StrategyEven, mosaics.StrategyEven},
		{`UPDATE target SET active = 0 WHERE "Id" = 105`, true, true, 4, true, mosaics.StrategyEven, mosaics.StrategyEven},
		{`UPDATE target SET active = 0 WHERE "Id" = 104`, true, true, 3, false, mosaics.StrategyWeakest, mosaics.StrategyWeakest},
		{`UPDATE target SET active = 1 WHERE "Id" IN (104, 105)`, true, true, 5, true, mosaics.StrategyEven, mosaics.StrategyEven},
		{`UPDATE ruleweight SET weight = 0 WHERE name = 'Panel Deficit' AND projectid = 1`, true, false, 5, true, mosaics.StrategyOff, mosaics.StrategyOff},
		{`UPDATE ruleweight SET weight = 0 WHERE name = 'Even Panels' AND projectid = 1`, false, false, 5, false, mosaics.StrategyOff, mosaics.StrategyOff},
	}
	for _, tc := range cases {
		plan := rig.plan(tc.setup, nil)
		d, err := rig.svc.Detail(context.Background(), markarian)
		if err != nil {
			t.Fatal(err)
		}
		b := d.Balancing
		if b.EvenPanels != tc.even || b.On != tc.on || b.EnabledPanels != tc.panels || b.EvenSpreads != tc.spreads || b.EvenMinPanels != 4 ||
			plan.StrategyInForce != string(tc.strategy) || plan.Strategy != string(tc.defaultsTo) {
			t.Errorf("%s: balancing %+v, in force %s, strategy %s", tc.setup, b, plan.StrategyInForce, plan.Strategy)
		}
	}
}

func fivePanels() string {
	q := `INSERT INTO target VALUES `
	for i := 3; i <= 5; i++ {
		if i > 3 {
			q += ", "
		}
		q += fmt.Sprintf(`(%d, 'Markarian Chain Panel %d', 1, %f, 13.0, 0, 1, 'm%d')`, 100+i, i, 12.45-float64(i-1)*0.2, i)
	}
	return q
}

func TestEvenPanelsSpreadOnlyFourOrMorePanels(t *testing.T) {
	t.Parallel()
	for panels, want := range map[int]bool{0: false, 2: false, 3: false, 4: true, 5: true, 15: true} {
		if got := mosaics.EvenPanelsSpread(true, panels); got != want {
			t.Errorf("%d panels with Even Panels on: %v, want %v", panels, got, want)
		}
		if mosaics.EvenPanelsSpread(false, panels) {
			t.Errorf("%d panels with Even Panels off spread", panels)
		}
	}
}
