package skyevents

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// HorizonsStep is the table step for designated bodies; the search
// interpolates between rows.
const HorizonsStep = time.Hour

const horizonsStepText = "60 m"

// HorizonsCommand is the Horizons COMMAND for a designated body.
func HorizonsCommand(designation, kind string) string {
	d := strings.TrimSpace(designation)
	if kind == bodies.KindAsteroid && d != "" && !strings.ContainsAny(d, "=;") {
		return "DES=" + d + ";"
	}
	return horizons.Command(d)
}

// Table is a body's hourly positions, kept to place it at any time.
type Table struct {
	rows []horizons.Row
}

func (t Table) at(at time.Time) (horizons.Row, bool) {
	rows := t.rows
	i := sort.Search(len(rows), func(i int) bool { return !rows[i].At.Before(at) })
	if i < len(rows) && rows[i].At.Equal(at) {
		return rows[i], true
	}
	if i == 0 || i == len(rows) {
		return horizons.Row{}, false
	}
	a, b := rows[i-1], rows[i]
	span := b.At.Sub(a.At)
	if span <= 0 || span > 2*HorizonsStep {
		return horizons.Row{}, false
	}
	f := float64(at.Sub(a.At)) / float64(span)
	dra := math.Remainder(b.RA-a.RA, 360)
	out := horizons.Row{At: at, RA: math.Mod(a.RA+f*dra+360, 360), Dec: a.Dec + f*(b.Dec-a.Dec), RARate: a.RARate, DecRate: a.DecRate}
	if a.Mag != nil && b.Mag != nil {
		m := *a.Mag + f*(*b.Mag-*a.Mag)
		out.Mag = &m
	}
	return out, true
}

// HorizonsTable asks Horizons for a designated body's hourly table over the
// grid and drops the older tables of the same body this search left in the
// cache.
func HorizonsTable(ctx context.Context, hz *horizons.Client, site sky.Site, designation, kind string, from, to time.Time) (Table, error) {
	cmd := HorizonsCommand(designation, kind)
	req := horizons.Request{Command: cmd, Site: site, Start: from.UTC(), Stop: to.UTC(), Step: HorizonsStep}
	tab, err := hz.Ephemeris(ctx, req)
	if err != nil {
		return Table{}, err
	}
	if hz.Cache != nil {
		if err := hz.Cache.WithContext(ctx).Where("command = ? AND step = ? AND start <> ?", cmd, horizonsStepText, from.UTC()).
			Delete(&app.HorizonsCache{}).Error; err != nil {
			return Table{}, fmt.Errorf("drop older event tables: %w", err)
		}
	}
	return Table{rows: tab.Rows}, nil
}

func bodyKind(kind string) Kind {
	if kind == bodies.KindAsteroid {
		return KindAsteroid
	}
	return KindComet
}

// TableTrack places a designated body on the grid.
func TableTrack(name, designation, kind string, tab Table, g Grid) Track {
	t := Track{Name: name, Kind: bodyKind(kind), Designation: designation, Source: SourceHorizons, Samples: make([]Sample, len(g.Times))}
	for i, at := range g.Times {
		if !g.Dark[i] {
			continue
		}
		r, ok := tab.at(at)
		if !ok {
			continue
		}
		mag := math.NaN()
		if r.Mag != nil {
			mag = *r.Mag
		}
		t.Samples[i] = Sample{OK: true, RA: r.RA, Dec: r.Dec, Alt: g.Observers[i].Altitude(r.RA, r.Dec), Mag: mag}
	}
	return t
}

// placer gives a member's position at any time, for the moving frame centre.
type placer func(at time.Time) (disc, bool)

func ephemerisPlacer(e *ephem.Ephemeris, b ephem.Body, site sky.Site) placer {
	return func(at time.Time) (disc, bool) {
		p, err := e.Observe(b, ephem.NewObserver(site, at))
		if err != nil {
			return disc{}, false
		}
		return disc{ra: p.RA, dec: p.Dec, radius: p.Diameter / 2}, true
	}
}

func tablePlacer(tab Table) placer {
	return func(at time.Time) (disc, bool) {
		r, ok := tab.at(at)
		return disc{ra: r.RA, dec: r.Dec}, ok
	}
}
