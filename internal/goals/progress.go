package goals

import (
	"context"
	"encoding/json"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

func DefaultGoal(filter string) Goal {
	return Goal{Filter: filter, Kind: KindSNR, SNR: DefaultSNRGoal, Depth: DefaultDepth(filter), PlateauStop: true}
}

func DefaultDepth(filter string) float64 {
	switch filter {
	case "H-a", "O-III", "S-II":
		return 25.5
	}
	return 25.8
}

func NoiseAt(a, b, hours float64) float64 {
	if hours <= 0 {
		return math.Inf(1)
	}
	return math.Sqrt(a*a/hours + b*b)
}

func GainPerHour(a, b, hours float64) float64 {
	if hours <= 0 || a <= 0 {
		return 100
	}
	return 100 * (1 - NoiseAt(a, b, hours+1)/NoiseAt(a, b, hours))
}

func HoursForSNR(hours, snr, target float64) float64 {
	if snr <= 0 || hours <= 0 {
		return math.Inf(1)
	}
	return math.Max(0, hours*(math.Pow(target/snr, 2)-1))
}

func HoursForDepth(hours, depth, target float64) float64 {
	if hours <= 0 {
		return math.Inf(1)
	}
	return math.Max(0, hours*(math.Pow(10, (target-depth)/1.25)-1))
}

func Evaluate(m app.GoalMeasurement, g Goal) Progress {
	p := Progress{
		Object: m.Object, Filter: m.Filter, TargetGUID: m.TargetGUID, Kind: g.Kind,
		SNR: m.SNR, EffectiveHours: m.EffectiveHours, GainPerHourPct: m.GainPerHourPct,
		LowConfidence: m.LowConfidence, Region: len(g.Region) > 0, MeasuredAt: m.MeasuredAt,
	}
	if m.Depth != nil {
		p.Depth = *m.Depth
	}
	if g.Kind == KindDepth && m.Depth != nil {
		p.Goal = g.Depth
		p.Achieved = *m.Depth
		p.HoursNeeded = HoursForDepth(m.EffectiveHours, *m.Depth, g.Depth)
		p.Progress = math.Pow(10, (*m.Depth-g.Depth)/1.25)
	} else {
		p.Kind = KindSNR
		p.Goal = g.SNR
		if p.Goal <= 0 {
			p.Goal = DefaultSNRGoal
		}
		p.Achieved = m.SNR
		p.HoursNeeded = HoursForSNR(m.EffectiveHours, m.SNR, p.Goal)
		if p.Goal > 0 {
			p.Progress = math.Pow(m.SNR/p.Goal, 2)
		}
	}
	if math.IsInf(p.HoursNeeded, 0) || math.IsNaN(p.HoursNeeded) {
		p.HoursNeeded = -1
	}
	p.Plateau = m.Subs > 0 && m.GainPerHourPct < PlateauGainPct
	p.Done = p.Progress >= 1 || (g.PlateauStop && p.Plateau)
	return p
}

func Lookup(ctx context.Context, appDB *gorm.DB, goalsByKey map[Key]Goal, keys []Key) (map[Key]Progress, error) {
	out := make(map[Key]Progress, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	objects := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if !seen[k.Object] {
			seen[k.Object] = true
			objects = append(objects, k.Object)
		}
	}
	var ms []app.GoalMeasurement
	if err := appDB.WithContext(ctx).Where("object IN ? AND error IS NULL", objects).Find(&ms).Error; err != nil {
		return nil, err
	}
	want := make(map[Key]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	for _, m := range ms {
		k := Key{Object: m.Object, Filter: m.Filter}
		if !want[k] {
			continue
		}
		g, ok := goalsByKey[k]
		if !ok {
			g = DefaultGoal(m.Filter)
		}
		out[k] = Evaluate(m, g)
	}
	return out, nil
}

func ParseRegion(s string) ([]Point, error) {
	if s == "" {
		return nil, nil
	}
	var pts []Point
	if err := json.Unmarshal([]byte(s), &pts); err != nil {
		return nil, err
	}
	return pts, nil
}
