package coverage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"gorm.io/gorm"
)

// BiasFramesTarget is how many bias frames make a set.
const BiasFramesTarget = 30

// BiasMaxAgeDays is how many days apart lights and their bias may be before
// a new bias set is wanted.
const BiasMaxAgeDays = 30

const (
	BiasFramesNeededBasis = "The target of 30 bias frames per set matches the only bias set on record (30 frames, 2025-02-05) and the 30-frame flat sets; " +
		"the user chose the dark target the same way (to match the sets already taken)."
	BiasMaxAgeBasis = "A setting, not a measurement: only one bias set per gain and offset is on record, so bias drift has not been measured. " +
		"Bias frames take seconds each, so a monthly set is cheap."
)

const (
	reasonNoExposure   = "the lights have no exposure time in their header"
	reasonNoGainOffset = "the lights have no GAIN or OFFSET in their header"
	reasonNoSetTemp    = "the lights have no SET-TEMP in their header"
	reasonNoBinning    = "the lights have no XBINNING in their header"
)

type BiasBacklog struct {
	ComputedAt        time.Time         `json:"computed_at"`
	FramesNeeded      int               `json:"frames_needed"`
	FramesNeededBasis string            `json:"frames_needed_basis"`
	MaxAgeDays        int               `json:"max_age_days"`
	MaxAgeBasis       string            `json:"max_age_basis"`
	SessionGapHours   float64           `json:"session_gap_hours"`
	Combos            []BiasCombo       `json:"combos"`
	Unschedulable     []BiasUnscheduled `json:"unschedulable"`
}

type BiasCombo struct {
	ComboKey           string       `json:"combo_key"`
	Priority           int          `json:"priority"`
	Gain               float64      `json:"gain"`
	Offset             float64      `json:"offset"`
	Binning            float64      `json:"binning"`
	ReadoutMode        *string      `json:"readout_mode"`
	FramesNeeded       int          `json:"frames_needed"`
	FramesHave         int          `json:"frames_have"`
	LightsMissing      int          `json:"lights_missing"`
	LightsStale        int          `json:"lights_stale"`
	LightsThin         int          `json:"lights_thin"`
	Nights             int          `json:"nights"`
	LatestNight        string       `json:"latest_night"`
	NewestBiasAt       *time.Time   `json:"newest_bias_at"`
	CurrentBiasNight   *string      `json:"current_bias_night"`
	CurrentBiasAgeDays *int         `json:"current_bias_age_days"`
	Requests           []RequestRef `json:"requests"`
	Basis              string       `json:"basis"`
}

type BiasUnscheduled struct {
	Lights int    `json:"lights"`
	Reason string `json:"reason"`
}

// RequestRef names a calibration request merged into a backlog combo.
type RequestRef struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Night  string `json:"night"`
}

type biasLightGroup struct {
	Night       time.Time
	Gain        *float64
	Offset      *float64
	BinX        *float64
	ReadoutMode *string
	N           int
}

type biasFrame struct {
	Gain    *float64
	Offset  *float64
	BinX    *float64
	DateObs *time.Time
	Night   *time.Time
}

func (f biasFrame) at() time.Time { return takenAt(f.DateObs, val0(f.Night)) }

func val0(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

type biasSetup struct{ gain, offset, bin float64 }

func (su biasSetup) matches(f biasFrame) bool {
	return f.Gain != nil && f.Offset != nil && *f.Gain == su.gain && *f.Offset == su.offset && same(val(f.BinX), su.bin)
}

func BiasComboKey(gain, offset, bin float64) string {
	return strings.Join([]string{"bias", numText(gain), numText(offset), numText(bin)}, "|")
}

func dayDiff(a, b time.Time) int {
	return int(math.Round(math.Abs(a.Sub(b).Hours() / 24)))
}

type biasTally struct {
	missing, stale, thin int
	nights               map[string]bool
	latest               string
	readout              map[string]int
}

// BuildBiasBacklog lists, per gain, offset and binning, the lights whose
// bias set is missing, too old when a new one would be nearer, or short of
// BiasFramesTarget.
func BuildBiasBacklog(ctx context.Context, db *gorm.DB, now time.Time) (BiasBacklog, error) {
	sets, _, err := loadSets(ctx, db)
	if err != nil {
		return BiasBacklog{}, err
	}
	var lights []biasLightGroup
	if err := db.WithContext(ctx).Table("frames").
		Select(`night, gain, "offset", bin_x, readout_mode, COUNT(*) as n`).
		Where("type = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT").
		Group(`night, gain, "offset", bin_x, readout_mode`).
		Scan(&lights).Error; err != nil {
		return BiasBacklog{}, fmt.Errorf("load lights for the bias backlog: %w", err)
	}
	frames, err := loadBias(ctx, db)
	if err != nil {
		return BiasBacklog{}, err
	}
	return biasBacklog(lights, frames, sets, now), nil
}

func loadBias(ctx context.Context, db *gorm.DB) ([]biasFrame, error) {
	var frames []biasFrame
	if err := db.WithContext(ctx).Table("frames").
		Select(`gain, "offset", bin_x, date_obs, night`).
		Where("type = ? AND index_error IS NULL", biasType).
		Scan(&frames).Error; err != nil {
		return nil, fmt.Errorf("load bias frames: %w", err)
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].at().Before(frames[j].at()) })
	return frames, nil
}

func biasBacklog(lights []biasLightGroup, frames []biasFrame, sets []calmatch.Set, now time.Time) BiasBacklog {
	b := BiasBacklog{
		ComputedAt: now.UTC(), FramesNeeded: BiasFramesTarget, FramesNeededBasis: BiasFramesNeededBasis,
		MaxAgeDays: BiasMaxAgeDays, MaxAgeBasis: BiasMaxAgeBasis, SessionGapHours: SessionGap.Hours(),
		Combos: []BiasCombo{}, Unschedulable: []BiasUnscheduled{},
	}
	tallies := map[biasSetup]*biasTally{}
	unsched := map[string]int{}
	for _, l := range lights {
		switch {
		case l.Gain == nil || l.Offset == nil:
			unsched[reasonNoGainOffset] += l.N
			continue
		case l.BinX == nil:
			unsched[reasonNoBinning] += l.N
			continue
		}
		g := calmatch.Group{Night: l.Night, Exposure: math.NaN(), Gain: *l.Gain, Offset: *l.Offset,
			SetTemp: math.NaN(), BinX: *l.BinX, Rotator: math.NaN()}
		m := calmatch.Choose(g, sets).Bias
		toNow := dayDiff(now, l.Night)
		missing := m.Quality == calmatch.Missing
		stale := !missing && m.AgeDays > BiasMaxAgeDays && toNow < m.AgeDays
		thin := !missing && !stale && m.Set.Master == "" && m.Set.Count > 0 && m.Set.Count < BiasFramesTarget && toNow <= m.AgeDays
		if !missing && !stale && !thin {
			continue
		}
		su := biasSetup{gain: g.Gain, offset: g.Offset, bin: g.BinX}
		t := tallies[su]
		if t == nil {
			t = &biasTally{nights: map[string]bool{}, readout: map[string]int{}}
			tallies[su] = t
		}
		switch {
		case missing:
			t.missing += l.N
		case stale:
			t.stale += l.N
		default:
			t.thin += l.N
		}
		night := l.Night.Format(time.DateOnly)
		t.nights[night] = true
		if night > t.latest {
			t.latest = night
		}
		if l.ReadoutMode != nil && *l.ReadoutMode != "" {
			t.readout[*l.ReadoutMode] += l.N
		}
	}
	for reason, n := range unsched {
		b.Unschedulable = append(b.Unschedulable, BiasUnscheduled{Lights: n, Reason: reason})
	}
	sort.Slice(b.Unschedulable, func(i, j int) bool {
		if b.Unschedulable[i].Lights != b.Unschedulable[j].Lights {
			return b.Unschedulable[i].Lights > b.Unschedulable[j].Lights
		}
		return b.Unschedulable[i].Reason < b.Unschedulable[j].Reason
	})
	for su, t := range tallies {
		c := newBiasCombo(su, mostCommon(t.readout))
		c.LightsMissing, c.LightsStale, c.LightsThin = t.missing, t.stale, t.thin
		c.Nights, c.LatestNight = len(t.nights), t.latest
		fillBiasProgress(&c, su, frames, sets, now)
		c.Basis = fmt.Sprintf("%d lights at gain %s, offset %s, bin %s have no bias to calibrate with, %d have a bias set more than %d days "+
			"from their night where one taken now would be nearer, and %d have a bias set of fewer than %d frames.",
			c.LightsMissing, numText(su.gain), numText(su.offset), numText(su.bin), c.LightsStale, BiasMaxAgeDays, c.LightsThin, BiasFramesTarget)
		b.Combos = append(b.Combos, c)
	}
	sort.SliceStable(b.Combos, func(i, j int) bool {
		a, c := b.Combos[i], b.Combos[j]
		if a.LightsMissing != c.LightsMissing {
			return a.LightsMissing > c.LightsMissing
		}
		if a.LightsStale != c.LightsStale {
			return a.LightsStale > c.LightsStale
		}
		if a.LightsThin != c.LightsThin {
			return a.LightsThin > c.LightsThin
		}
		if a.LatestNight != c.LatestNight {
			return a.LatestNight > c.LatestNight
		}
		return a.ComboKey < c.ComboKey
	})
	for i := range b.Combos {
		b.Combos[i].Priority = i + 1
	}
	return b
}

func newBiasCombo(su biasSetup, readout *string) BiasCombo {
	return BiasCombo{
		ComboKey: BiasComboKey(su.gain, su.offset, su.bin), Gain: su.gain, Offset: su.offset, Binning: su.bin,
		ReadoutMode: readout, FramesNeeded: BiasFramesTarget, Requests: []RequestRef{},
	}
}

func mostCommon(counts map[string]int) *string {
	mode, most := "", 0
	for r, n := range counts {
		if n > most || (n == most && r < mode) {
			mode, most = r, n
		}
	}
	if mode == "" {
		return nil
	}
	return &mode
}

// fillBiasProgress sets the bias frames of the setup taken in the open
// session, the newest one, and the newest bias set the stacker could use.
func fillBiasProgress(c *BiasCombo, su biasSetup, frames []biasFrame, sets []calmatch.Set, now time.Time) {
	var last time.Time
	open := 0
	for _, f := range frames {
		if !su.matches(f) {
			continue
		}
		at := f.at()
		if c.NewestBiasAt == nil || at.After(*c.NewestBiasAt) {
			a := at.UTC()
			c.NewestBiasAt = &a
		}
		if !last.IsZero() && at.Sub(last) > SessionGap {
			open = 0
		}
		last = at
		open++
	}
	if !last.IsZero() && now.Sub(last) <= SessionGap {
		c.FramesHave = open
	}
	var newest *calmatch.Set
	for i, s := range sets {
		if s.Type != biasType || !calmatch.Buildable(s) || !same(s.Gain, su.gain) || !same(s.Offset, su.offset) || !same(s.BinX, su.bin) {
			continue
		}
		if newest == nil || s.Night.After(newest.Night) {
			newest = &sets[i]
		}
	}
	if newest != nil {
		night := newest.Night.Format(time.DateOnly)
		age := dayDiff(now, newest.Night)
		c.CurrentBiasNight, c.CurrentBiasAgeDays = &night, &age
	}
}
