package mosaics

import (
	"math"
	"time"
)

// Horizon is a custom horizon sampled every Step degrees of azimuth from
// north through east, starting at azimuth 0.
type Horizon struct {
	Step      float64   `json:"azimuthStep"`
	Altitudes []float64 `json:"altitudes"`
}

func (h *Horizon) Valid() bool {
	if h == nil || !(h.Step > 0) || len(h.Altitudes) == 0 || math.Abs(float64(len(h.Altitudes))*h.Step-360) > 1e-6 {
		return false
	}
	for _, a := range h.Altitudes {
		if math.IsNaN(a) || math.IsInf(a, 0) || a < -90 || a > 90 {
			return false
		}
	}
	return true
}

// At interpolates linearly between the samples on either side of az,
// wrapping from the last sample back to azimuth 0.
func (h *Horizon) At(az float64) float64 {
	n := len(h.Altitudes)
	x := wrap360(az) / h.Step
	i := int(math.Floor(x))
	f := x - float64(i)
	a, b := h.Altitudes[i%n], h.Altitudes[(i+1)%n]
	return a + (b-a)*f
}

// Limit is the lowest altitude a target may be imaged at. It mirrors Target
// Scheduler's HorizonDefinition: with no horizon it is the flat minimum
// altitude; with a horizon it is min(90, max(horizon(az)+Offset, Min)).
type Limit struct {
	Min     float64
	Horizon *Horizon
	Offset  float64
}

func Flat(minAlt float64) Limit { return Limit{Min: minAlt} }

func Custom(h *Horizon, offset, minAlt float64) Limit {
	return Limit{Min: minAlt, Horizon: h, Offset: offset}
}

func (l Limit) At(az float64) float64 {
	if l.Horizon == nil {
		return l.Min
	}
	return math.Min(90, math.Max(l.Horizon.At(az)+l.Offset, l.Min))
}

// HorizonReport is what the observatory scheduler last said about the custom
// horizon of a NINA profile. Horizon is nil when the profile has none.
type HorizonReport struct {
	ProfileID string
	Custom    bool
	Horizon   *Horizon
	FetchedAt time.Time
}
