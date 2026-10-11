package coverage

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	DarkRecentDays   = 30
	DarkHalfLifeDays = 30.0
	DarkActiveFloor  = 0.1

	DarkWeightBlocked = 2.0
	DarkWeightScaled  = 1.0
	DarkWeightThin    = 0.5
)

type ScoreParts struct {
	Blocked float64 `json:"blocked"`
	Scaled  float64 `json:"scaled"`
	Thin    float64 `json:"thin"`
}

func (p ScoreParts) total() float64 { return p.Blocked + p.Scaled + p.Thin }

func (p ScoreParts) rounded() ScoreParts {
	return ScoreParts{Blocked: roundScore(p.Blocked), Scaled: roundScore(p.Scaled), Thin: roundScore(p.Thin)}
}

func RankingBasis() string {
	return fmt.Sprintf("Combos are ranked by score, highest first. Each light adds its need weight times its age weight. "+
		"Need weight: %s for a light with no dark at all, %s for a light calibrated with a dark scaled from another setpoint or exposure, "+
		"%s for a light with an exact dark set of fewer than %d clean darks. Age weight: 1 for a light from the night that ends today, halved for every %s days "+
		"since its night, so recent lights dominate. A light of an active target never weighs less than %s; a target is active when its project is Active and the "+
		"target enabled in Target Scheduler, or when it has lights from the last %d days. Requested dark sets come first; ties go to more "+
		"lights with no dark, then scaled, then short sets, then the latest night.",
		numText(DarkWeightBlocked), numText(DarkWeightScaled), numText(DarkWeightThin), DarkFramesTarget, numText(DarkHalfLifeDays),
		numText(DarkActiveFloor), DarkRecentDays)
}

type darkRank struct {
	today  time.Time
	active map[string]bool
}

func targetKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func nightDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func newDarkRank(lights []darkLightGroup, active map[string]bool, now time.Time) darkRank {
	r := darkRank{today: nightDate(now.UTC()), active: map[string]bool{}}
	for k := range active {
		r.active[targetKey(k)] = true
	}
	for _, l := range lights {
		if l.Object != "" && r.ageDays(l.Night) <= DarkRecentDays {
			r.active[targetKey(l.Object)] = true
		}
	}
	return r
}

func (r darkRank) ageDays(night time.Time) int {
	return max(0, int(math.Round(r.today.Sub(nightDate(night)).Hours()/24))-1)
}

func (r darkRank) isActive(l darkLightGroup) bool {
	return l.Object != "" && r.active[targetKey(l.Object)]
}

func (r darkRank) weight(l darkLightGroup) float64 {
	w := math.Pow(0.5, float64(r.ageDays(l.Night))/DarkHalfLifeDays)
	if r.isActive(l) {
		w = math.Max(w, DarkActiveFloor)
	}
	return w
}

func (r darkRank) add(t *tempTally, l darkLightGroup, q calmatch.Quality) {
	w := float64(l.N) * r.weight(l)
	switch q {
	case calmatch.Missing:
		t.score.Blocked += w * DarkWeightBlocked
	case calmatch.Fallback:
		t.score.Scaled += w * DarkWeightScaled
	case calmatch.Exact:
		t.score.Thin += w * DarkWeightThin
	}
	if r.ageDays(l.Night) <= DarkRecentDays {
		t.recent += l.N
	}
	if r.isActive(l) {
		t.active += l.N
	}
}

func roundScore(v float64) float64 {
	return math.Round(v*100) / 100
}

func scoreNote(c DarkCombo) string {
	return fmt.Sprintf("Score %s = %s from lights with no dark + %s from lights on a scaled dark + %s from lights on a short set; "+
		"%d of its lights are from the last %d days, %d belong to active targets, and the latest night is %s.",
		numText(c.Score), numText(c.ScoreParts.Blocked), numText(c.ScoreParts.Scaled), numText(c.ScoreParts.Thin),
		c.LightsRecent, DarkRecentDays, c.LightsActive, c.LatestNight)
}

func ActiveTargets(ctx context.Context, sched *gorm.DB) (map[string]bool, error) {
	out := map[string]bool{}
	if sched == nil {
		return out, nil
	}
	for _, t := range []string{"target", "project"} {
		has, err := tables.Has(ctx, sched, t)
		if err != nil {
			return nil, fmt.Errorf("check the %s table for active targets: %w", t, err)
		}
		if !has {
			return out, nil
		}
	}
	var names []string
	if err := sched.WithContext(ctx).Table("target t").
		Joins(`JOIN project p ON p."Id" = t.projectid`).
		Where(`p.state = 1 AND coalesce(t.active, 1) = 1`).
		Pluck("t.name", &names).Error; err != nil {
		return nil, fmt.Errorf("load active targets: %w", err)
	}
	for _, n := range names {
		out[targetKey(n)] = true
	}
	return out, nil
}
