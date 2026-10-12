package bodies

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	// DarkSunAltitude is the Sun altitude below which a track row is dark.
	DarkSunAltitude = -18.0
	TrackBasis      = "Positions, rates and angles are JPL Horizons' topocentric table for tonight at the observatory, the same table the plugin centres on. tail_pa is Horizons' PsAng, the position angle of the extended Sun-to-body radius vector, where an ion tail points; velocity_pa is PsAMV, the position angle of the negative heliocentric velocity, the side a dust tail curves toward; motion_pa is the direction the body moves across the sky, from the RA and Dec rates. Angles are degrees east of north."
)

var ErrNoDesignation = errors.New("designation is required")

type TrackRow struct {
	At         time.Time `json:"at"`
	RA         float64   `json:"ra"`
	Dec        float64   `json:"dec"`
	Alt        float64   `json:"alt"`
	Dark       bool      `json:"dark"`
	RateMin    float64   `json:"rate_arcsec_per_min"`
	MotionPA   *float64  `json:"motion_pa"`
	TailPA     *float64  `json:"tail_pa"`
	VelocityPA *float64  `json:"velocity_pa"`
	Mag        *float64  `json:"mag"`
}

type TrackView struct {
	Designation string     `json:"designation"`
	Kind        string     `json:"kind"`
	Command     string     `json:"horizons_command"`
	Night       NightSpan  `json:"night"`
	FetchedAt   *time.Time `json:"fetched_at"`
	Cached      bool       `json:"cached"`
	Angles      bool       `json:"angles"`
	Best        *TrackRow  `json:"best"`
	Rows        []TrackRow `json:"rows"`
	Basis       string     `json:"basis"`
}

// MotionPA is the position angle, degrees east of north, of a motion of
// raRate (dRA*cos(Dec), east positive) and decRate (north positive), or
// false for no motion.
func MotionPA(raRate, decRate float64) (float64, bool) {
	if raRate == 0 && decRate == 0 || !finite(raRate) || !finite(decRate) {
		return 0, false
	}
	return math.Mod(math.Atan2(raRate, decRate)*180/math.Pi+360, 360), true
}

// TrackRows turns a Horizons table into track rows seen from site, and
// picks the dark row where the body is highest, or the highest row with
// the Sun down when no row is dark.
func TrackRows(site sky.Site, tab horizons.Table) ([]TrackRow, *TrackRow) {
	rows := make([]TrackRow, 0, len(tab.Rows))
	best, bestNight := -1, -1
	for _, r := range tab.Rows {
		sun := site.SunAltitude(r.At)
		tr := TrackRow{At: r.At, RA: r.RA, Dec: r.Dec, Alt: round(site.Altitude(r.RA, r.Dec, r.At), 2), Dark: sun < DarkSunAltitude,
			RateMin: round(math.Hypot(r.RARate, r.DecRate)/60, 4), TailPA: r.PsAng, VelocityPA: r.PsAMV, Mag: r.Mag}
		if pa, ok := MotionPA(r.RARate, r.DecRate); ok {
			pa = round(pa, 3)
			tr.MotionPA = &pa
		}
		rows = append(rows, tr)
		i := len(rows) - 1
		if tr.Dark && (best < 0 || tr.Alt > rows[best].Alt) {
			best = i
		}
		if sun < 0 && (bestNight < 0 || tr.Alt > rows[bestNight].Alt) {
			bestNight = i
		}
	}
	if best < 0 {
		best = bestNight
	}
	if best < 0 {
		return rows, nil
	}
	b := rows[best]
	return rows, &b
}

type Tracker struct {
	Horizons *horizons.Client
	Site     SiteFunc
	Now      func() time.Time
	Command  func(designation, kind string) string
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Track is a body's Horizons table for tonight, the night the position
// tables are built for, so an active target's table comes from the cache.
func (t *Tracker) Track(ctx context.Context, designation, kind string) (TrackView, error) {
	d := strings.TrimSpace(designation)
	k := strings.TrimSpace(kind)
	if k == "" {
		k = KindComet
	}
	view := TrackView{Designation: d, Kind: k, Basis: TrackBasis, Rows: []TrackRow{}}
	if d == "" {
		return view, ErrNoDesignation
	}
	if t.Site == nil || t.Horizons == nil {
		return view, ErrNoSite
	}
	site, err := t.Site(ctx)
	if err != nil {
		return view, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	command := horizons.CommandFor
	if t.Command != nil {
		command = t.Command
	}
	view.Command = command(d, k)
	view.Night = Spans(site, t.now().UTC(), 1)[0]
	tab, err := t.Horizons.EphemerisWithAngles(ctx, horizons.Request{Command: view.Command, Site: site, Start: view.Night.Start, Stop: view.Night.Stop, Step: CoarseStep})
	if err != nil {
		return view, err
	}
	view.Cached, view.Angles = tab.Cached, tab.Angles
	if !tab.FetchedAt.IsZero() {
		f := tab.FetchedAt
		view.FetchedAt = &f
	}
	view.Rows, view.Best = TrackRows(site, tab)
	return view, nil
}
