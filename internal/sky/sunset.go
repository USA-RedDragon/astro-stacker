package sky

import "time"

const (
	SunsetAltitude = -0.833
	sunsetScanStep = 5 * time.Minute
	sunsetSpan     = 48 * time.Hour
)

func (s Site) setsBetween(a, b time.Time) bool {
	return s.SunAltitude(a) > SunsetAltitude && s.SunAltitude(b) <= SunsetAltitude
}

func (s Site) refineSunset(a, b time.Time) time.Time {
	for b.Sub(a) > time.Second {
		mid := a.Add(b.Sub(a) / 2)
		if s.SunAltitude(mid) > SunsetAltitude {
			a = mid
		} else {
			b = mid
		}
	}
	return b.Truncate(time.Second)
}

func (s Site) PreviousSunset(t time.Time) (time.Time, bool) {
	for b := t; t.Sub(b) < sunsetSpan; b = b.Add(-sunsetScanStep) {
		a := b.Add(-sunsetScanStep)
		if s.setsBetween(a, b) {
			return s.refineSunset(a, b), true
		}
	}
	return time.Time{}, false
}

func (s Site) NextSunset(t time.Time) (time.Time, bool) {
	for a := t; a.Sub(t) < sunsetSpan; a = a.Add(sunsetScanStep) {
		b := a.Add(sunsetScanStep)
		if s.setsBetween(a, b) {
			return s.refineSunset(a, b), true
		}
	}
	return time.Time{}, false
}

func (s Site) TonightSunset(t time.Time) (time.Time, bool) {
	if s.SunAltitude(t) > SunsetAltitude {
		return s.NextSunset(t)
	}
	return s.PreviousSunset(t)
}
