package quality

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

const (
	PedestalBias       = "bias"
	PedestalConfigured = "configured"
	PedestalCalibrated = "calibrated"
)

type BiasLevel struct {
	Gain   float64
	Offset float64
	ADU    float64
	Night  time.Time
	Frames int
}

type Pedestals struct {
	Configured float64
	Biases     []BiasLevel
}

type Pedestal struct {
	ADU    float64
	Source string
	Basis  string
}

func (p Pedestals) At(gain, offset float64) Pedestal {
	var best *BiasLevel
	if offset > 0 && !math.IsNaN(gain) {
		for i := range p.Biases {
			b := &p.Biases[i]
			if b.Gain == gain && b.Offset == offset && (best == nil || b.Night.After(best.Night)) {
				best = b
			}
		}
	}
	if best != nil {
		return Pedestal{ADU: best.ADU, Source: PedestalBias, Basis: fmt.Sprintf("median of the %s master bias (%d frames, gain %g, offset %g)",
			best.Night.Format(time.DateOnly), best.Frames, best.Gain, best.Offset)}
	}
	adu := PedestalAt(p.Configured, offset)
	basis := fmt.Sprintf("configured %g ADU at offset %d", p.Configured, pedestalOffset)
	if offset > 0 && offset != pedestalOffset {
		basis += fmt.Sprintf(", plus %g ADU per offset step to offset %g", pedestalPerOffset, offset)
	}
	return Pedestal{ADU: adu, Source: PedestalConfigured, Basis: basis + "; no master bias measured at this gain and offset"}
}

func LoadPedestals(ctx context.Context, appDB *gorm.DB, configured float64) (Pedestals, error) {
	out := Pedestals{Configured: configured}
	var rows []struct {
		Gain      float64
		Offset    float64
		MedianADU float64
		Night     *time.Time
		Frames    int
	}
	if err := appDB.WithContext(ctx).Table("calibration_masters").
		Select(`gain, "offset", median_adu, night, frames`).
		Where(`type = ? AND median_adu IS NOT NULL AND gain IS NOT NULL AND "offset" IS NOT NULL`, "BIAS").
		Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("load bias levels: %w", err)
	}
	for _, r := range rows {
		b := BiasLevel{Gain: r.Gain, Offset: r.Offset, ADU: r.MedianADU, Frames: r.Frames}
		if r.Night != nil {
			b.Night = *r.Night
		}
		out.Biases = append(out.Biases, b)
	}
	return out, nil
}
