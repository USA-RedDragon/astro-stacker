package planning

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	pluginDelayGrading     = 80
	pluginExposureThrottle = 125

	BasisGoal        = "goal"
	BasisAccepted    = "accepted"
	BasisProvisional = "acquired, grading delayed"
	BasisThrottle    = "acquired, no grading"

	StatusMeasured    = "measured"
	StatusFailed      = "failed"
	StatusNoMaster    = "no-master"
	StatusNotMeasured = "not-measured"
)

type Prefs struct {
	DelayGrading     float64 `json:"delayGrading"`
	ExposureThrottle float64 `json:"exposureThrottle"`
	Source           string  `json:"source"`
}

type Measurement struct {
	SNR            float64           `json:"snr"`
	Signal         float64           `json:"signal"`
	Noise          float64           `json:"noise"`
	NoiseMask      string            `json:"noiseMask"`
	Pairs          int               `json:"pairs"`
	Levels         int               `json:"levels"`
	Subs           int               `json:"subs"`
	SubsTotal      int               `json:"subsTotal"`
	EffectiveHours float64           `json:"effectiveHours"`
	BandFraction   float64           `json:"bandFraction"`
	NebFraction    float64           `json:"nebFraction"`
	HeldOutErrPct  *float64          `json:"heldOutErrPct"`
	GainPerHourPct *float64          `json:"gainPerHourPct"`
	Plateau        bool              `json:"plateau"`
	Depth          *float64          `json:"depth"`
	DepthBand      string            `json:"depthBand,omitempty"`
	DepthApprox    bool              `json:"depthApprox"`
	DepthReason    string            `json:"depthReason,omitempty"`
	ZeroPointStars int               `json:"zeroPointStars"`
	LowConfidence  bool              `json:"lowConfidence"`
	LowReason      string            `json:"lowReason,omitempty"`
	Region         bool              `json:"region"`
	MeasuredAt     time.Time         `json:"measuredAt"`
	Points         []goals.DrawPoint `json:"points"`
}

func measurementOf(m app.GoalMeasurement) *Measurement {
	out := &Measurement{
		SNR: m.SNR, Signal: m.Signal, Noise: m.NoiseNow, NoiseMask: m.NoiseMask, Levels: m.Levels,
		Subs: m.Subs, SubsTotal: m.SubsTotal, EffectiveHours: m.EffectiveHours, BandFraction: m.BandFraction,
		NebFraction: m.NebFraction, HeldOutErrPct: m.HeldOutErrPct, Depth: m.Depth, DepthApprox: m.DepthApprox,
		ZeroPointStars: m.ZeroPointStars, LowConfidence: m.LowConfidence, LowReason: m.LowReason,
		Region: m.RegionHash != "", MeasuredAt: m.MeasuredAt, Points: []goals.DrawPoint{},
	}
	if m.Depth == nil {
		out.DepthReason = goals.NoDepthReason(m)
	} else {
		out.DepthBand = m.DepthBand
		if out.DepthBand == "" {
			out.DepthBand = goals.BandGaiaG
		}
	}
	if m.Subs > 0 && m.NoiseA > 0 && m.EffectiveHours > 0 {
		g := m.GainPerHourPct
		out.GainPerHourPct = &g
		out.Plateau = g < goals.PlateauGainPct
	}
	if m.Points != "" {
		_ = json.Unmarshal([]byte(m.Points), &out.Points)
	}
	out.Pairs = len(out.Points)
	return out
}

func loadPrefs(ctx context.Context, db *gorm.DB) map[string]Prefs {
	out := map[string]Prefs{}
	if !tableExists(db, "profilepreference") {
		return out
	}
	var rows []struct {
		ProfileID string   `gorm:"column:profile_id"`
		Delay     *float64 `gorm:"column:delay"`
		Throttle  *float64 `gorm:"column:throttle"`
	}
	if err := db.WithContext(ctx).Table("profilepreference").
		Select(`"profileId" AS profile_id, "delayGrading" AS delay, "exposureThrottle" AS throttle`).Scan(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		p := Prefs{DelayGrading: pluginDelayGrading, ExposureThrottle: pluginExposureThrottle, Source: "profile"}
		if r.Delay != nil {
			p.DelayGrading = *r.Delay
		}
		if r.Throttle != nil {
			p.ExposureThrottle = *r.Throttle
		}
		out[r.ProfileID] = p
	}
	return out
}

func prefsFor(all map[string]Prefs, profile string) Prefs {
	if p, ok := all[profile]; ok {
		return p
	}
	return Prefs{DelayGrading: pluginDelayGrading, ExposureThrottle: pluginExposureThrottle, Source: "plugin defaults: no profile preference row"}
}

func percentage(num, denom float64) float64 {
	if denom == 0 {
		return 0
	}
	return math.Min(100, num/denom*100)
}

func planPercent(p Plan, goal *goals.Progress, grader bool, prefs Prefs) (float64, string) {
	if goal != nil {
		if goal.Done {
			return 100, BasisGoal
		}
		return math.Min(clamp01(goal.Progress)*100, 99.9), BasisGoal
	}
	if grader {
		threshold := 0.0
		if p.Desired != 0 {
			threshold = float64(p.Acquired) / float64(p.Desired) * 100
		}
		if prefs.DelayGrading > 0 && threshold < prefs.DelayGrading {
			return percentage(float64(p.Acquired), float64(p.Desired)), BasisProvisional
		}
		return percentage(float64(p.Accepted), float64(p.Desired)), BasisAccepted
	}
	if p.Acquired == 0 {
		return 0, BasisThrottle
	}
	throttleAt := math.Trunc(prefs.ExposureThrottle / 100 * float64(p.Desired))
	return math.Min(100, float64(p.Acquired)/throttleAt*100), BasisThrottle
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return math.Min(1, v)
}

func noveltyScore(progress, hours float64) float64 {
	s := clamp01(1 - clamp01(progress))
	if hours >= 1 {
		s *= 0.8
	}
	return s
}
