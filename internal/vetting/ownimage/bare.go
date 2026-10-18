package ownimage

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// broadbandClear is the filter every source shows in, whose master best
// shows a galaxy's nucleus.
const broadbandClear = "Luminance"

// bareMasters reads each filter's master once and cuts it around every
// spot with no catalogued star, for its light and the places around it to
// be measured against, finds the nucleus of any galaxy the spot sits by
// and how far the spot is from the covered area's edge.
func bareMasters(ctx context.Context, d Data, object string, subs []Sub, plans []*spotPlan) error {
	var bare []*spotPlan
	for _, sp := range plans {
		if sp.inFrame && sp.star == nil && sp.bare != nil {
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
	seen := map[string]bool{}
	var filters []string
	for _, s := range subs {
		if !seen[s.Filter] {
			seen[s.Filter] = true
			filters = append(filters, s.Filter)
		}
	}
	sort.SliceStable(filters, func(a, b int) bool { return filters[a] == broadbandClear && filters[b] != broadbandClear })
	for _, f := range filters {
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
			wide := cutWide(p, sp)
			b := sp.bare
			if len(b.wide) == 0 {
				b.setNucleus(sp, wide)
			}
			b.wide[f] = wide
			if e := edgeDistance(p, sp.x, sp.y); !b.edgeOK || e < b.edge {
				b.edge, b.edgeOK = e, true
			}
			if sp.tile != nil && len(sp.tile.shape) > 0 {
				if s, _, _, ok := fitShape(p, sp.tile.shape); ok {
					sp.mSigma[f] = s
				}
			}
		}
	}
	return nil
}
