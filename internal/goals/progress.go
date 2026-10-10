package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

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

func FloorMeasurable(levels int) bool {
	return levels >= MinFloorLevels
}

func FloorReason(levels int) string {
	return fmt.Sprintf("noise floor not measurable from %d draw levels", levels)
}

func IsPlateau(m app.GoalMeasurement) bool {
	return m.Subs > 0 && FloorMeasurable(m.Levels) && m.GainPerHourPct < PlateauGainPct
}

func RefitWithoutFloor(m *app.GoalMeasurement) bool {
	if m.Error != nil || m.Subs <= 0 || m.EffectiveHours <= 0 || FloorMeasurable(m.Levels) || m.Points == "" {
		return false
	}
	var pts []DrawPoint
	if json.Unmarshal([]byte(m.Points), &pts) != nil {
		return false
	}
	a, ok := FitSqrtT(pts)
	if !ok {
		return false
	}
	before := *m
	m.NoiseA, m.NoiseB = a, 0
	m.NoiseNow = NoiseAt(a, 0, m.EffectiveHours)
	m.GainPerHourPct = GainPerHour(a, 0, m.EffectiveHours)
	m.SNR = m.Signal / m.NoiseNow
	if m.ZeroPoint != nil {
		if d, ok := Depth(*m.ZeroPoint, m.NoiseNow, m.PixelScale); ok {
			m.Depth = &d
		}
	}
	if reason := FloorReason(m.Levels); !slices.Contains(strings.Split(m.LowReason, "; "), reason) {
		if m.LowReason != "" {
			m.LowReason += "; "
		}
		m.LowReason += reason
	}
	m.LowConfidence = true
	return m.NoiseA != before.NoiseA || m.NoiseB != before.NoiseB || m.NoiseNow != before.NoiseNow ||
		m.GainPerHourPct != before.GainPerHourPct || m.SNR != before.SNR || !sameDepth(m.Depth, before.Depth) ||
		m.LowReason != before.LowReason || m.LowConfidence != before.LowConfidence
}

func sameDepth(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func HoursForSNR(hours, snr, target float64) float64 {
	if snr <= 0 || hours <= 0 {
		return math.Inf(1)
	}
	return math.Max(0, hours*((target/snr)*(target/snr)-1))
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
		LowConfidence: m.LowConfidence, LowReason: m.LowReason, Region: len(g.Region) > 0, MeasuredAt: m.MeasuredAt,
	}
	if m.Depth != nil {
		p.Depth = *m.Depth
		p.DepthSystem, p.DepthBand, p.DepthApprox = m.DepthSystem, m.DepthBand, m.DepthApprox
		if p.DepthSystem == "" {
			p.DepthSystem, p.DepthBand, p.DepthApprox = SystemGaiaG, BandGaiaG, IsNarrowband(m.Filter)
		}
	}
	switch {
	case g.Kind == KindDepth && m.Depth != nil:
		p.Goal = g.Depth
		p.Achieved = *m.Depth
		p.HoursNeeded = HoursForDepth(m.EffectiveHours, *m.Depth, g.Depth)
		p.Progress = math.Pow(10, (*m.Depth-g.Depth)/1.25)
	case g.Kind == KindDepth:
		p.Goal = g.Depth
		p.HoursNeeded = -1
		p.Unmeasured = NoDepthReason(m)
	default:
		p.Kind = KindSNR
		p.Goal = g.SNR
		if p.Goal <= 0 {
			p.Goal = DefaultSNRGoal
		}
		p.Achieved = m.SNR
		p.HoursNeeded = HoursForSNR(m.EffectiveHours, m.SNR, p.Goal)
		if p.Goal > 0 {
			p.Progress = (m.SNR / p.Goal) * (m.SNR / p.Goal)
		}
	}
	if math.IsInf(p.HoursNeeded, 0) || math.IsNaN(p.HoursNeeded) {
		p.HoursNeeded = -1
	}
	p.Plateau = IsPlateau(m)
	p.Done = p.Progress >= 1 || (g.PlateauStop && p.Plateau)
	return p
}

func NoDepthReason(m app.GoalMeasurement) string {
	switch {
	case m.PixelScale <= 0:
		return "no depth: the master has no plate solution to calibrate against"
	case m.ZeroPointStars < MinZeroPointStars:
		return fmt.Sprintf("no depth: %d Gaia stars matched, %d needed", m.ZeroPointStars, MinZeroPointStars)
	}
	return "no depth: the zero point could not be measured"
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
