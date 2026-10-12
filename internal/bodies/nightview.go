package bodies

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"gorm.io/gorm"
)

const NightViewBasis = "Per night, from the published night table: peak_altitude is the body's highest altitude in astronomical darkness (sun below -18 degrees), " +
	"dark_hours the longest stretch of astronomical darkness it spends above the project's minimum altitude, positions interpolated between the nightly rows every 10 minutes. " +
	"A night is usable when the body is bright enough and dark_hours reaches the project's minimum time. " +
	"This view does not apply the horizon, moon avoidance or twilight level of the exposure plans; the season on the project list comes from the plugin, which does."

// NightDetail is one night of a body's season.
type NightDetail struct {
	Night     string   `json:"night"`
	Mag       *float64 `json:"mag"`
	MagLimit  *float64 `json:"mag_limit"`
	Bright    bool     `json:"bright"`
	PeakAlt   *float64 `json:"peak_altitude"`
	DarkHours float64  `json:"dark_hours"`
	Usable    bool     `json:"usable"`
}

// NightView is a body target's nights over its season.
type NightView struct {
	TargetGUID     string        `json:"target_guid"`
	Name           string        `json:"name"`
	Designation    string        `json:"designation"`
	Kind           string        `json:"kind"`
	ComputedAt     *time.Time    `json:"computed_at"`
	MinAltitude    float64       `json:"min_altitude"`
	MinimumMinutes int           `json:"minimum_minutes"`
	MagLimit       float64       `json:"mag_limit"`
	BrightNights   int           `json:"bright_nights"`
	UsableNights   int           `json:"usable_nights"`
	FirstUsable    *string       `json:"first_usable"`
	LastUsable     *string       `json:"last_usable"`
	Brightest      *float64      `json:"brightest_mag"`
	BrightestNight *string       `json:"brightest_night"`
	Basis          string        `json:"basis"`
	Nights         []NightDetail `json:"nights"`
}

func interpolateNight(rows []NightRow, t time.Time) (float64, float64, bool) {
	i := sort.Search(len(rows), func(j int) bool { return rows[j].At.After(t) }) - 1
	if i < 0 || i+1 >= len(rows) {
		return 0, 0, false
	}
	a, b := rows[i], rows[i+1]
	span := b.At.Sub(a.At).Seconds()
	if span <= 0 || span > 2*NightStep.Seconds() {
		return 0, 0, false
	}
	f := t.Sub(a.At).Seconds() / span
	d := math.Mod(b.RA-a.RA+540, 360) - 180
	return math.Mod(a.RA+f*d+360, 360), a.Dec + f*(b.Dec-a.Dec), true
}

func nightNoon(site sky.Site, night time.Time) time.Time {
	return time.Date(night.Year(), night.Month(), night.Day(), 12, 0, 0, 0, time.UTC).Add(-localMean(site))
}

// BuildNightView works out the nights of a body from its rows, sorted by
// time, from first on.
func BuildNightView(site sky.Site, rows []NightRow, first time.Time, minAlt float64, minMinutes int) []NightDetail {
	byNight := map[string]NightRow{}
	for _, r := range rows {
		byNight[r.Night] = r
	}
	out := []NightDetail{}
	for i := range SeasonNights {
		date := first.AddDate(0, 0, i)
		night := date.Format(time.DateOnly)
		row, ok := byNight[night]
		if !ok {
			continue
		}
		d := NightDetail{Night: night, Mag: row.Mag, MagLimit: row.MagLimit, Bright: row.Bright()}
		n := site.NightOf(nightNoon(site, date), sky.DefaultStep)
		peak := math.Inf(-1)
		var run, longest float64
		for _, at := range n.Dark {
			ra, dec, ok := interpolateNight(rows, at)
			if !ok {
				run = 0
				continue
			}
			alt := site.Altitude(ra, dec, at)
			peak = max(peak, alt)
			if alt >= minAlt {
				run += n.Step.Hours()
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		if !math.IsInf(peak, -1) {
			p := math.Round(peak*10) / 10
			d.PeakAlt = &p
		}
		d.DarkHours = math.Round(longest*100) / 100
		d.Usable = d.Bright && longest > 0 && longest*60 >= float64(minMinutes)
		out = append(out, d)
	}
	return out
}

func summarize(v *NightView) {
	for _, d := range v.Nights {
		if d.Bright {
			v.BrightNights++
		}
		if d.Mag != nil && (v.Brightest == nil || *d.Mag < *v.Brightest) {
			m, n := *d.Mag, d.Night
			v.Brightest, v.BrightestNight = &m, &n
		}
		if d.Usable {
			v.UsableNights++
			n := d.Night
			if v.FirstUsable == nil {
				v.FirstUsable = &n
			}
			v.LastUsable = &n
		}
	}
}

type projectLimits struct {
	MinimumAltitude *float64 `gorm:"column:minimumaltitude"`
	MinimumTime     *int     `gorm:"column:minimumtime"`
}

func limitsOf(ctx context.Context, sched *gorm.DB, guid string) (float64, int, error) {
	var l []projectLimits
	if err := sched.WithContext(ctx).Table("target t").Select("p.minimumaltitude AS minimumaltitude, p.minimumtime AS minimumtime").
		Joins(`JOIN project p ON t.projectid = p."Id"`).Where("lower(t.guid) = ?", strings.ToLower(guid)).Limit(1).Scan(&l).Error; err != nil {
		return 0, 0, fmt.Errorf("load the project of %s: %w", guid, err)
	}
	if len(l) == 0 {
		return 0, 0, nil
	}
	alt, minutes := 0.0, 0
	if l[0].MinimumAltitude != nil {
		alt = *l[0].MinimumAltitude
	}
	if l[0].MinimumTime != nil {
		minutes = *l[0].MinimumTime
	}
	return alt, minutes, nil
}

// View is a target's nights from the latest computed table, with its
// project's minimum altitude and minimum time; false when the target has
// no rows.
func (s *NightService) View(ctx context.Context, guid string) (NightView, bool, error) {
	res := s.Latest()
	if res == nil || res.site == nil {
		return NightView{}, false, nil
	}
	rows := res.Nights(guid)
	sort.Slice(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
	if len(rows) == 0 {
		return NightView{}, false, nil
	}
	t, _, err := TargetByGUID(ctx, s.Sched, guid)
	if err != nil {
		return NightView{}, false, err
	}
	minAlt, minutes, err := limitsOf(ctx, s.Sched, guid)
	if err != nil {
		return NightView{}, false, err
	}
	first, err := time.Parse(time.DateOnly, res.From)
	if err != nil {
		return NightView{}, false, err
	}
	v := NightView{TargetGUID: rows[0].TargetGUID, Name: t.Name, Designation: rows[0].Designation, Kind: t.Kind, ComputedAt: res.ComputedAt,
		MinAltitude: minAlt, MinimumMinutes: minutes, MagLimit: MagLimit(t.Kind), Basis: NightBasis + " " + NightViewBasis}
	v.Nights = BuildNightView(*res.site, rows, first, minAlt, minutes)
	summarize(&v)
	return v, true, nil
}
