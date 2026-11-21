package goals

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	ColumnScoreCut      = "score_cut"
	ColumnExpectedScore = "expected_score"
	// PassMinTargets is how many targets with a settled best score a filter needs before a target that
	// has not settled its own is judged against theirs.
	PassMinTargets   = 3
	passScoreStep    = 0.01
	passCacheRefresh = time.Hour
)

// passStatuses are the verdicts whose score is the sub's own: the subs a target's best score is taken from.
func passStatuses() []string {
	return []string{app.StackStatusAdded, app.StackStatusNight, app.StackStatusLowScore, app.StackStatusMoon, app.StackStatusRecalibrate}
}

// PassInputs is what the plugin's guess of whether a sub would pass the stacker's score cut is made from.
type PassInputs struct {
	// Best is each object's best score in each filter and how many scored subs it has there.
	Best map[Key]BestScore
	// Reference is each filter's median best score over the objects that have settled theirs (MinSubs).
	Reference map[string]float64
	// Sky is the past subs' scores as fractions of their object's best, by the sky they were taken under;
	// empty without the site.
	Sky SkyRatios
	// Recent is each object's subs scored in the ExpectedRecentWindow before now.
	Recent map[Key][]RecentScore
}

type BestScore struct {
	Score float64
	Subs  int
}

func HasPassColumns(ctx context.Context, sched *gorm.DB) (bool, error) {
	for _, c := range []string{ColumnScoreCut, ColumnExpectedScore} {
		if has, err := tables.HasColumn(ctx, sched, ProgressTable, c); err != nil || !has {
			return false, err
		}
	}
	return true, nil
}

// LoadPassInputs reads the scored lights: every one for the best scores, ExpectedHistory back from now
// for the sky fit when site is known, and the recent ones.
func LoadPassInputs(ctx context.Context, appDB *gorm.DB, now time.Time, site *sky.Site) (PassInputs, error) {
	in := PassInputs{Best: map[Key]BestScore{}, Reference: map[string]float64{}}
	for _, t := range []any{&app.Frame{}, &app.StackFrame{}} {
		if has, err := tables.Has(ctx, appDB, t); err != nil || !has {
			return in, err
		}
	}
	var best []struct {
		Object string
		Filter string
		Best   float64
		N      int
	}
	if err := appDB.WithContext(ctx).Table("stack_frames sf").
		Select("f.object AS object, f.filter AS filter, max(sf.score) AS best, count(*) AS n").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("f.type = ? AND sf.status IN ? AND sf.score > 0", "LIGHT", passStatuses()).
		Group("f.object, f.filter").Scan(&best).Error; err != nil {
		return in, fmt.Errorf("load best sub scores: %w", err)
	}
	for _, b := range best {
		if math.IsNaN(b.Best) || math.IsInf(b.Best, 0) {
			continue
		}
		k := Key{Object: b.Object, Filter: frameheader.NormalizeFilter(b.Filter)}
		cur := in.Best[k]
		in.Best[k] = BestScore{Score: math.Max(cur.Score, b.Best), Subs: cur.Subs + b.N}
	}
	in.settle()
	if site != nil {
		var subs []PassSub
		if err := passSubs(appDB.WithContext(ctx), now.Add(-ExpectedHistory), now).Scan(&subs).Error; err != nil {
			return in, fmt.Errorf("load past sub scores: %w", err)
		}
		in.Sky = in.FitSky(subs, *site)
	}
	recent, err := LoadRecent(ctx, appDB, now)
	if err != nil {
		return in, err
	}
	in.Recent = recent
	return in, nil
}

// settle sets Reference from Best.
func (in *PassInputs) settle() {
	settled := map[string][]float64{}
	for k, b := range in.Best {
		if b.Subs >= MinSubs {
			settled[k.Filter] = append(settled[k.Filter], b.Score)
		}
	}
	for f, v := range settled {
		if len(v) >= PassMinTargets {
			in.Reference[f] = median(v)
		}
	}
}

// best is the best score a target made of objects is judged against in filter f: its own best, or the
// filter's Reference when that is higher and the target has fewer than MinSubs scored subs there, as its
// first subs pass against each other and fail when a good night raises the best. 0 when nothing is known.
func (in PassInputs) best(objects []string, f string) float64 {
	var own BestScore
	for _, o := range objects {
		b := in.Best[Key{Object: o, Filter: f}]
		own.Score, own.Subs = math.Max(own.Score, b.Score), own.Subs+b.Subs
	}
	best := own.Score
	if ref, ok := in.Reference[f]; ok && own.Subs < MinSubs {
		best = math.Max(best, ref)
	}
	return math.Min(best, 1)
}

// ScoreCut is the score a new sub of filter needs to stay out of low_score on a target made of objects:
// minScore times the target's best (see best). Nil when nothing is known.
func (in PassInputs) ScoreCut(objects []string, filter string, minScore float64) *float64 {
	best := in.best(objects, frameheader.NormalizeFilter(filter))
	if !(minScore > 0) || !(best > 0) {
		return nil
	}
	c := quantize(minScore*best, passScoreStep)
	return &c
}

// passInputs is the cached best scores and sky fit, at most passCacheRefresh old, with the recent subs
// read afresh.
func (p *Publisher) passInputs(ctx context.Context, now time.Time) (PassInputs, error) {
	p.mu.Lock()
	cached, at := p.pass, p.passAt
	p.mu.Unlock()
	if cached != nil && now.Sub(at) < passCacheRefresh && !now.Before(at) {
		in := *cached
		recent, err := LoadRecent(ctx, p.App, now)
		if err != nil {
			return in, err
		}
		in.Recent = recent
		return in, nil
	}
	in, err := LoadPassInputs(ctx, p.App, now, p.site(ctx))
	if err != nil {
		return in, err
	}
	p.mu.Lock()
	p.pass, p.passAt = &in, now
	p.mu.Unlock()
	return in, nil
}

// setPass publishes the score cut and the score a sub taken now is expected to get, under the sky at the
// target's position pos (RA, Dec in degrees) seen from site; without them only recent subs can give it.
func (r *progressRow) setPass(in PassInputs, objects []string, pos *[2]float64, site *sky.Site, now time.Time, minScore float64) {
	r.ScoreCut = in.ScoreCut(objects, r.Filter, minScore)
	var s *SkyState
	if pos != nil && site != nil {
		st := SkyAt(*site, now, pos[0], pos[1])
		s = &st
	}
	r.ExpectedScore = in.ExpectedScore(objects, r.Filter, s)
}

func (r progressRow) samePass(o progressRow) bool {
	return sameFloat(r.ScoreCut, o.ScoreCut) && sameFloat(r.ExpectedScore, o.ExpectedScore)
}
