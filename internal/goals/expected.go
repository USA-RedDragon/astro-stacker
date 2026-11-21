package goals

import (
	"context"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"gorm.io/gorm"
)

const (
	// ExpectedHistory is how far back the subs that tell a target's expected score under a sky go: over a
	// year, so every Moon phase at every altitude of a season is in. 120 days held out more good subs in the
	// backtest of September to November 2026 (3.4% against 1.6%).
	ExpectedHistory = 400 * 24 * time.Hour
	// ExpectedOwnSubs is how many of a target's own past subs in a filter under a sky cell its own
	// estimate needs; ExpectedPoolSubs is what the filter-wide and class-wide estimates need.
	ExpectedOwnSubs  = 10
	ExpectedPoolSubs = 20
	// ExpectedOwnQuantile and ExpectedPoolQuantile are the quantiles of the past score ratios taken as the
	// expected one: a sub at the better end of usual, so a filter is held out only when even a good sub
	// under this sky would fail its cut. The pooled estimates know the target less well and take a higher
	// quantile.
	ExpectedOwnQuantile  = 0.7
	ExpectedPoolQuantile = 0.8
	// ExpectedRecentWindow, ExpectedRecentSubs and ExpectedRecentMax: when a target has at least
	// ExpectedRecentSubs scored subs in the filter within the window before now, the median of its newest
	// ExpectedRecentMax is the expected score instead of the history, as it measures tonight's sky itself.
	ExpectedRecentWindow = time.Hour
	ExpectedRecentSubs   = 3
	ExpectedRecentMax    = 5
)

// illumEdges, moonAltEdges and altEdges are the inner edges of the bins of the Moon's lit fraction, the
// Moon's altitude and the target's altitude (degrees).
func illumEdges() []float64   { return []float64{0.25, 0.5, 0.75, 0.9} }
func moonAltEdges() []float64 { return []float64{15, 35} }
func altEdges() []float64     { return []float64{30, 45, 60} }

func binOf(v float64, edges []float64) int {
	n := 0
	for _, e := range edges {
		if v >= e {
			n++
		}
	}
	return n
}

// SkyState is the sky a sub is taken under, as far as its expected score goes: the Moon's altitude and
// lit fraction, and the target's altitude.
type SkyState struct {
	MoonAltitude float64
	Illumination float64
	Altitude     float64
}

// SkyAt is the sky at site at t for a target at ra, dec (degrees).
func SkyAt(site sky.Site, t time.Time, ra, dec float64) SkyState {
	g := moon.At(t)
	m := g.Topocentric(t, site.Latitude, site.Longitude)
	return SkyState{MoonAltitude: m.Altitude(t, site.Latitude, site.Longitude), Illumination: g.Illumination, Altitude: site.Altitude(ra, dec, t)}
}

// SkyCell is a SkyState's bin: Moon -1 while the Moon is down, else its lit fraction bin times 10 plus its
// altitude bin; Alt is the target's altitude bin.
type SkyCell struct {
	Moon int
	Alt  int
}

func (s SkyState) Cell() SkyCell {
	c := SkyCell{Moon: -1, Alt: binOf(s.Altitude, altEdges())}
	if s.MoonAltitude > 0 {
		c.Moon = binOf(s.Illumination, illumEdges())*10 + binOf(s.MoonAltitude, moonAltEdges())
	}
	return c
}

const anyAlt = -1

// The sources Ratio names for the subs an expected score came from.
const (
	SourceTarget     = "target"
	SourceTargetMoon = "target under this Moon"
	SourceFilter     = "filter"
	SourceFilterMoon = "filter under this Moon"
	SourceClassMoon  = "class under this Moon"
)

type skyKey struct {
	scope  string
	filter string
	moon   int
	alt    int
}

func classScope(filter string) string {
	if IsNarrowband(filter) {
		return "narrowband"
	}
	return "broadband"
}

// SkyRatios holds past subs' scores as a fraction of their object's best, by sky cell: per object and
// filter, per filter, and per narrowband or broadband class.
type SkyRatios struct {
	ratios map[skyKey][]float64
}

// PassSub is one scored light, as the expected score is fitted from.
type PassSub struct {
	Object   string
	Filter   string
	Key      string
	Score    float64
	DateObs  time.Time
	Exposure *float64
	MountRA  *float64
	MountDec *float64
}

// usable is false for a light the fit leaves out: a calibrated copy, no time, no pointing or a score that
// is not a number.
func (s PassSub) usable() bool {
	return !strings.Contains(path.Base(s.Key), "_cal.") && !s.DateObs.IsZero() && s.MountRA != nil && s.MountDec != nil &&
		!math.IsNaN(s.Score) && !math.IsInf(s.Score, 0)
}

func (s PassSub) mid() time.Time {
	if s.Exposure == nil || !(*s.Exposure > 0) {
		return s.DateObs
	}
	return s.DateObs.Add(time.Duration(*s.Exposure / 2 * float64(time.Second)))
}

// FitSky bins subs by the sky they were taken under, seen from site, as fractions of their object's best
// in in.
func (in PassInputs) FitSky(subs []PassSub, site sky.Site) SkyRatios {
	out := SkyRatios{ratios: map[skyKey][]float64{}}
	for _, s := range subs {
		if !s.usable() {
			continue
		}
		f := frameheader.NormalizeFilter(strings.TrimSpace(s.Filter))
		best := in.best([]string{s.Object}, f)
		if !(best > 0) {
			continue
		}
		r := math.Max(0, math.Min(1, s.Score)) / best
		r = math.Min(1, r)
		c := SkyAt(site, s.mid(), *s.MountRA, *s.MountDec).Cell()
		for _, k := range []skyKey{
			{"object:" + s.Object, f, c.Moon, c.Alt}, {"object:" + s.Object, f, c.Moon, anyAlt},
			{"", f, c.Moon, c.Alt}, {"", f, c.Moon, anyAlt}, {classScope(f), "", c.Moon, anyAlt},
		} {
			out.ratios[k] = append(out.ratios[k], r)
		}
	}
	for _, v := range out.ratios {
		slices.Sort(v)
	}
	return out
}

// Ratio is the expected score of a sub of filter f under cell c as a fraction of its target's best, and
// where it came from: the target's own subs under the same Moon and altitude, then under the same Moon,
// then the filter's subs of every target under the same Moon and altitude, then the same Moon, then every
// narrowband or broadband filter under the same Moon. The first with enough subs gives its quantile.
func (r SkyRatios) Ratio(objects []string, f string, c SkyCell) (float64, string, bool) {
	own := func(alt int) []float64 {
		var v []float64
		for _, o := range objects {
			v = append(v, r.ratios[skyKey{"object:" + o, f, c.Moon, alt}]...)
		}
		if len(objects) > 1 {
			slices.Sort(v)
		}
		return v
	}
	for _, l := range []struct {
		name string
		v    []float64
		need int
		q    float64
	}{
		{SourceTarget, own(c.Alt), ExpectedOwnSubs, ExpectedOwnQuantile},
		{SourceTargetMoon, own(anyAlt), ExpectedOwnSubs, ExpectedOwnQuantile},
		{SourceFilter, r.ratios[skyKey{"", f, c.Moon, c.Alt}], ExpectedPoolSubs, ExpectedPoolQuantile},
		{SourceFilterMoon, r.ratios[skyKey{"", f, c.Moon, anyAlt}], ExpectedPoolSubs, ExpectedPoolQuantile},
		{SourceClassMoon, r.ratios[skyKey{classScope(f), "", c.Moon, anyAlt}], ExpectedPoolSubs, ExpectedPoolQuantile},
	} {
		if len(l.v) >= l.need {
			return percentileSorted(l.v, l.q*100), l.name, true
		}
	}
	return 0, "", false
}

// RecentScore is one target's sub scored within ExpectedRecentWindow before now.
type RecentScore struct {
	At    time.Time
	Score float64
}

// recentRatio is the median of the newest ExpectedRecentMax recent scores of objects in f as a fraction of
// best, when there are at least ExpectedRecentSubs.
func (in PassInputs) recentRatio(objects []string, f string, best float64) (float64, bool) {
	var v []RecentScore
	for _, o := range objects {
		v = append(v, in.Recent[Key{Object: o, Filter: f}]...)
	}
	if len(v) < ExpectedRecentSubs {
		return 0, false
	}
	slices.SortStableFunc(v, func(a, b RecentScore) int { return a.At.Compare(b.At) })
	if len(v) > ExpectedRecentMax {
		v = v[len(v)-ExpectedRecentMax:]
	}
	s := make([]float64, len(v))
	for i, x := range v {
		s[i] = x.Score
	}
	return math.Min(1, median(s)/best), true
}

// ExpectedScore is the score a sub of filter taken now under sky s is expected to get on a target made of
// objects: its best (see best) times the ratio of its recent subs tonight when it has them, else the
// ratio Ratio gives for the sky's cell. Nil without a best, or without recent subs and a sky or enough
// history.
func (in PassInputs) ExpectedScore(objects []string, filter string, s *SkyState) *float64 {
	f := frameheader.NormalizeFilter(filter)
	best := in.best(objects, f)
	if !(best > 0) {
		return nil
	}
	ratio, ok := in.recentRatio(objects, f, best)
	if !ok {
		if s == nil {
			return nil
		}
		if ratio, _, ok = in.Sky.Ratio(objects, f, s.Cell()); !ok {
			return nil
		}
	}
	e := quantize(math.Min(1, ratio)*best, passScoreStep)
	return &e
}

// LoadRecent reads each object's subs scored in the ExpectedRecentWindow before now.
func LoadRecent(ctx context.Context, appDB *gorm.DB, now time.Time) (map[Key][]RecentScore, error) {
	var subs []PassSub
	if err := passSubs(appDB.WithContext(ctx), now.Add(-ExpectedRecentWindow), now).Scan(&subs).Error; err != nil {
		return nil, fmt.Errorf("load tonight's sub scores: %w", err)
	}
	out := map[Key][]RecentScore{}
	for _, s := range subs {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || strings.Contains(path.Base(s.Key), "_cal.") {
			continue
		}
		k := Key{Object: s.Object, Filter: frameheader.NormalizeFilter(strings.TrimSpace(s.Filter))}
		out[k] = append(out[k], RecentScore{At: s.DateObs, Score: math.Max(0, math.Min(1, s.Score))})
	}
	return out, nil
}

func passSubs(db *gorm.DB, from, to time.Time) *gorm.DB {
	return db.Table("stack_frames sf").
		Select("f.object AS object, f.filter AS filter, f.key AS key, sf.score AS score, f.date_obs AS date_obs, "+
			"f.exposure AS exposure, f.mount_ra AS mount_ra, f.mount_dec AS mount_dec").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("f.type = ? AND f.date_obs IS NOT NULL AND f.date_obs > ? AND f.date_obs <= ?", "LIGHT", from, to).
		Where("sf.status IN ?", passStatuses()).
		Order("f.date_obs")
}
