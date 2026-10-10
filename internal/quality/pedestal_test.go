package quality_test

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
)

func TestPedestalsPreferMeasuredBias(t *testing.T) {
	t.Parallel()
	night := time.Date(2025, 1, 19, 0, 0, 0, 0, time.UTC)
	p := quality.Pedestals{Configured: 506, Biases: []quality.BiasLevel{
		{Gain: 100, Offset: 50, ADU: 502, Night: night.AddDate(0, 0, -30), Frames: 50},
		{Gain: 100, Offset: 50, ADU: 503, Night: night, Frames: 40},
	}}
	got := p.At(100, 50)
	if got.ADU != 503 || got.Source != quality.PedestalBias || !strings.Contains(got.Basis, "2025-01-19") {
		t.Errorf("At(100, 50) = %+v, want the newest master bias", got)
	}
	got = p.At(0, 240)
	if got.ADU != 2406 || got.Source != quality.PedestalConfigured {
		t.Errorf("At(0, 240) = %+v, want the configured pedestal scaled to offset 240", got)
	}
}
