package skybright

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	SourceMeasured = "measured"
	SourceConfig   = "config"
	SourceNone     = "none"

	Band            = "Gaia G"
	MaxNights       = 10
	MinNightFrames  = 5
	History         = 365 * 24 * time.Hour
	defaultTTL      = 30 * time.Minute
	methodText      = "Each sub's sky level divided by its master's zero point, fitted on Gaia G magnitudes of the field's stars, so the sky reads in Gaia G mag/arcsec² as if it had the stars' median colour; median per night, then the median of the newest clear nights"
	methodMoonText  = methodText + "; subs taken with the moon up are left out"
	reasonNoSamples = "no L, R, G or B master with a Gaia zero point has been measured yet"
	reasonFewFrames = "no night has enough measured broadband subs"
	reasonMoon      = "every measured broadband sub was taken with the moon up"
)

func filterPreference() []string { return []string{"L", "G", "R", "B"} }

func Broadband(filter string) bool {
	f := rigsource.CanonicalFilter(filter)
	for _, b := range filterPreference() {
		if f == b {
			return true
		}
	}
	return false
}

type Night struct {
	Night  string  `json:"night"`
	Mag    float64 `json:"mag"`
	Frames int     `json:"frames"`
}

type Basis struct {
	Source   string  `json:"source"`
	Method   string  `json:"method"`
	Filter   string  `json:"filter"`
	Band     string  `json:"band"`
	Nights   int     `json:"nights"`
	Frames   int     `json:"frames"`
	From     *string `json:"from"`
	To       *string `json:"to"`
	PerNight []Night `json:"perNight"`
	Reason   *string `json:"reason"`
}

type Value struct {
	Mag   *float64 `json:"skyBrightness"`
	Basis Basis    `json:"skyBrightnessBasis"`
}

type SiteFunc func(ctx context.Context) (lat, lon float64, ok bool)

func none(reason string) Value {
	r := reason
	return Value{Basis: Basis{Source: SourceNone, Method: methodText, Band: Band, PerNight: []Night{}, Reason: &r}}
}

func Configured(mag float64) Value {
	m := mag
	return Value{Mag: &m, Basis: Basis{Source: SourceConfig, Method: "set in discover.sky-brightness", Band: Band, PerNight: []Night{}}}
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func Measure(ctx context.Context, db *gorm.DB, site SiteFunc, now time.Time) (Value, error) {
	if db == nil || !db.Migrator().HasTable(&app.SkySample{}) {
		return none(reasonNoSamples), nil
	}
	var rows []app.SkySample
	if err := db.WithContext(ctx).Where("night IS NOT NULL AND date_obs >= ?", now.Add(-History)).Order("night DESC").Find(&rows).Error; err != nil {
		return Value{}, err
	}
	var lat, lon float64
	known := false
	if site != nil {
		lat, lon, known = site(ctx)
	}
	perFilter := map[string]map[string][]float64{}
	seen, dark := false, false
	for _, r := range rows {
		f := rigsource.CanonicalFilter(r.Filter)
		if !Broadband(f) || math.IsNaN(r.SkyMag) || math.IsInf(r.SkyMag, 0) {
			continue
		}
		seen = true
		if known && r.DateObs != nil && moon.At(*r.DateObs).Altitude(*r.DateObs, lat, lon) > 0 {
			continue
		}
		dark = true
		if perFilter[f] == nil {
			perFilter[f] = map[string][]float64{}
		}
		k := r.Night.Format(time.DateOnly)
		perFilter[f][k] = append(perFilter[f][k], r.SkyMag)
	}
	if !seen {
		return none(reasonNoSamples), nil
	}
	if !dark {
		return none(reasonMoon), nil
	}
	var filter string
	var keys []string
	var byNight map[string][]float64
	for _, f := range filterPreference() {
		for k, v := range perFilter[f] {
			if len(v) >= MinNightFrames {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			filter, byNight = f, perFilter[f]
			break
		}
	}
	if len(keys) == 0 {
		return none(reasonFewFrames), nil
	}
	slices.Sort(keys)
	slices.Reverse(keys)
	keys = keys[:min(len(keys), MaxNights)]
	v := Value{Basis: Basis{Source: SourceMeasured, Method: methodText, Filter: filter, Band: Band, PerNight: []Night{}}}
	if known {
		v.Basis.Method = methodMoonText
	}
	mags := make([]float64, 0, len(keys))
	for _, k := range keys {
		m := median(byNight[k])
		mags = append(mags, m)
		v.Basis.PerNight = append(v.Basis.PerNight, Night{Night: k, Mag: math.Round(m*100) / 100, Frames: len(byNight[k])})
		v.Basis.Frames += len(byNight[k])
	}
	mag := math.Round(median(mags)*100) / 100
	from, to := keys[len(keys)-1], keys[0]
	v.Mag, v.Basis.Nights, v.Basis.From, v.Basis.To = &mag, len(keys), &from, &to
	return v, nil
}

type Source struct {
	DB       *gorm.DB
	Site     SiteFunc
	Override float64
	TTL      time.Duration
	Now      func() time.Time

	mu  sync.Mutex
	at  time.Time
	val *Value
}

func (s *Source) Get(ctx context.Context) Value {
	if s.Override > 0 {
		return Configured(s.Override)
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.val != nil && now.Sub(s.at) < ttl {
		return *s.val
	}
	v, err := Measure(ctx, s.DB, s.Site, now)
	if err != nil {
		if s.val != nil {
			return *s.val
		}
		return none(err.Error())
	}
	s.val, s.at = &v, now
	return v
}
