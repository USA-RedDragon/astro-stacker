package ownimage

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

func TestLightShareFindsOneOrTwoFrames(t *testing.T) {
	t.Parallel()
	night := func(snr ...float64) Night {
		n := Night{Sign: 1}
		for k, z := range snr {
			n.Light = append(n.Light, discoveries.LightPoint{FrameID: k + 1, Flux: z, FluxErr: 1})
		}
		return n
	}
	// 41941: one Green sub at 11.8 sigma, the other 11 near zero.
	share, _, _, k := lightShare(night(11.8, 0.4, -0.3, 0.2, 0.5, -0.6, 0.1, 0.3, -0.2, 0.6, 0.2, -0.4))
	if k != 12 || share <= lightShareMax {
		t.Errorf("single-frame flash: share %.2f of %d", share, k)
	}
	share, _, _, k = lightShare(night(3.1, 2.4, 2.8, 3.5, 2.2, 2.9, 3.3, 2.6))
	if k != 8 || share > 0.62 {
		t.Errorf("a steady source's top two hold %.2f", share)
	}
	sp := &spotPlan{spot: Spot{Nights: []Night{night(4, 3, 2), night(30, 5, 5, 5, 5, 5)}}}
	if _, ok := fewFrames(sp); ok {
		t.Error("a steady three-frame night or a satellite on a weaker night failed it")
	}
	sp.spot.Nights = append(sp.spot.Nights, night(70, 1, 0, 0.5, -0.3, 0.2))
	if c, ok := fewFrames(sp); !ok || c.Status != check.Fail {
		t.Errorf("one frame carrying the strongest night: %+v", c)
	}
	g := 14.0
	sp.star = &Star{G: g}
	if _, ok := fewFrames(sp); ok {
		t.Error("a star's change was judged by its frames' shares")
	}
}
