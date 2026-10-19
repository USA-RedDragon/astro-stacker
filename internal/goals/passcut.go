package goals

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	ColumnScoreCut      = "score_cut"
	ColumnExpectedScore = "expected_score"
	// PassHistory is how far back the subs that tell a filter's expected score at a night quality go.
	PassHistory = 120 * 24 * time.Hour
	// PassMinSamples is how many past subs taken at about the same night quality a filter needs for an
	// expected score.
	PassMinSamples = 20
	// PassQualityBand is the first and the step of the night quality bands, around the current one, that
	// past subs are taken from; PassQualityBands is how many steps are tried before there is no estimate.
	PassQualityBand  = 0.05
	PassQualityBands = 4
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
	// History is each filter's past subs: the night quality just before each and its score as a fraction
	// of its object's best (see Best), the ratio the stacker's cut tests.
	History map[string][]QualityRatio
}

type BestScore struct {
	Score float64
	Subs  int
}

type QualityRatio struct {
	Quality float64
	Ratio   float64
}

func HasPassColumns(ctx context.Context, sched *gorm.DB) (bool, error) {
	for _, c := range []string{ColumnScoreCut, ColumnExpectedScore} {
		if has, err := tables.HasColumn(ctx, sched, ProgressTable, c); err != nil || !has {
			return false, err
		}
	}
	return true, nil
}

// LoadPassInputs reads the scored lights: every one for the best scores, PassHistory back from now for
// the history.
func LoadPassInputs(ctx context.Context, appDB *gorm.DB, now time.Time) (PassInputs, error) {
	in := PassInputs{Best: map[Key]BestScore{}, Reference: map[string]float64{}, History: map[string][]QualityRatio{}}
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

	var subs []struct {
		Object  string
		Filter  string
		Score   float64
		DateObs time.Time
	}
	if err := appDB.WithContext(ctx).Table("stack_frames sf").
		Select("f.object AS object, f.filter AS filter, sf.score AS score, f.date_obs AS date_obs").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("f.type = ? AND f.date_obs IS NOT NULL AND f.date_obs > ? AND f.date_obs <= ?", "LIGHT", now.Add(-PassHistory-NightQualityWindow), now).
		Where("sf.status IN ?", []string{app.StackStatusAdded, app.StackStatusNight, app.StackStatusLowScore}).
		Order("f.date_obs").Scan(&subs).Error; err != nil {
		return in, fmt.Errorf("load past sub scores: %w", err)
	}
	all := make([]scoredSub, 0, len(subs))
	for _, s := range subs {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) {
			continue
		}
		all = append(all, scoredSub{Score: math.Max(0, math.Min(1, s.Score)), DateObs: s.DateObs})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].DateObs.Before(all[j].DateObs) })
	for _, s := range subs {
		if math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || !s.DateObs.After(now.Add(-PassHistory)) {
			continue
		}
		q := qualityBefore(all, s.DateObs)
		if q == nil {
			continue
		}
		f := frameheader.NormalizeFilter(s.Filter)
		best := in.best([]string{s.Object}, f)
		if !(best > 0) {
			continue
		}
		in.History[f] = append(in.History[f], QualityRatio{Quality: *q, Ratio: math.Max(0, math.Min(1, s.Score)) / best})
	}
	return in, nil
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

// qualityBefore is the night quality the stacker publishes just before t: medianQuality of the subs in
// the NightQualityWindow before it. all is sorted by time.
func qualityBefore(all []scoredSub, t time.Time) *float64 {
	lo := sort.Search(len(all), func(i int) bool { return all[i].DateObs.After(t.Add(-NightQualityWindow)) })
	hi := sort.Search(len(all), func(i int) bool { return !all[i].DateObs.Before(t) })
	q, _ := medianQuality(all[lo:hi])
	return q
}

// ExpectedScore is the score a sub of filter taken now is expected to get on a target made of objects: the
// target's best (see best) times the median ratio to their object's best of filter's past subs taken at
// about night quality q, within PassQualityBand of it, widened a band at a time up to PassQualityBands
// until PassMinSamples subs are in. Nil without a night quality, a best or enough subs.
func (in PassInputs) ExpectedScore(objects []string, filter string, q *float64) *float64 {
	f := frameheader.NormalizeFilter(filter)
	best := in.best(objects, f)
	if q == nil || !(best > 0) {
		return nil
	}
	hist := in.History[f]
	for b := 1; b <= PassQualityBands; b++ {
		band := float64(b)*PassQualityBand + 1e-9
		var v []float64
		for _, h := range hist {
			if math.Abs(h.Quality-*q) <= band {
				v = append(v, h.Ratio)
			}
		}
		if len(v) >= PassMinSamples {
			e := quantize(median(v)*best, passScoreStep)
			return &e
		}
	}
	return nil
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

func (p *Publisher) passInputs(ctx context.Context, now time.Time) (PassInputs, error) {
	p.mu.Lock()
	cached, at := p.pass, p.passAt
	p.mu.Unlock()
	if cached != nil && now.Sub(at) < passCacheRefresh && !now.Before(at) {
		return *cached, nil
	}
	in, err := LoadPassInputs(ctx, p.App, now)
	if err != nil {
		return in, err
	}
	p.mu.Lock()
	p.pass, p.passAt = &in, now
	p.mu.Unlock()
	return in, nil
}

// setPass publishes the score cut only: the expected score from absolute
// night quality held out broadband that passes when low targets drag the
// night down, so it stays unpublished until it is measured per target.
func (r *progressRow) setPass(in PassInputs, objects []string, _ *float64, minScore float64) {
	r.ScoreCut = in.ScoreCut(objects, r.Filter, minScore)
	r.ExpectedScore = nil
}

func (r progressRow) samePass(o progressRow) bool {
	return sameFloat(r.ScoreCut, o.ScoreCut) && sameFloat(r.ExpectedScore, o.ExpectedScore)
}
