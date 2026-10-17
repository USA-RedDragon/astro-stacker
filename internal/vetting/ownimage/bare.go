package ownimage

import (
	"context"
	"fmt"
	"log/slog"
)

// bareMasters reads each filter's master once and cuts it around every
// spot with no catalogued star, for its light to be measured against.
func bareMasters(ctx context.Context, d Data, object string, subs []Sub, plans []*spotPlan) error {
	var bare []*spotPlan
	for _, sp := range plans {
		if sp.inFrame && sp.star == nil {
			bare = append(bare, sp)
		}
	}
	m, ok := d.(Masters)
	if len(bare) == 0 || !ok {
		return nil
	}
	keys, err := m.Masters(ctx, object)
	if err != nil {
		return fmt.Errorf("the target's masters: %w", err)
	}
	filters := map[string]bool{}
	for _, s := range subs {
		filters[s.Filter] = true
	}
	for f := range filters {
		key := keys[f]
		if key == "" {
			continue
		}
		p, err := d.Load(ctx, key)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("Could not read a master for vetting", "key", key, "error", err)
			continue
		}
		for _, sp := range bare {
			if sp.masters == nil {
				sp.masters, sp.mSigma = map[string][]float32{}, map[string]float64{}
			}
			sp.masters[f] = cutStamp(p, sp.x, sp.y)
			if sp.tile != nil && len(sp.tile.shape) > 0 {
				if s, _, _, ok := fitShape(p, sp.tile.shape); ok {
					sp.mSigma[f] = s
				}
			}
		}
	}
	return nil
}
