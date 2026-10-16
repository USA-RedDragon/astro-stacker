package coverage

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
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
	return fmt.Sprintf("Requested dark sets come first. Next come combos with lights from the last imaging night (the latest night with lights), "+
		"so the darks for the night that just ended are taken at its dawn. All other combos follow. Within each group, combos are ranked by score, highest first. "+
		"Each light adds its need weight times its age weight. "+
		"Need weight: %s for a light with no dark at all, %s for a light calibrated with a dark scaled from another setpoint or exposure, "+
		"%s for a light with an exact dark set short of a full set (%s) of clean darks. Age weight: 1 for a light from the night that ends today, halved for every %s days "+
		"since its night, so recent lights dominate. A light of an active target never weighs less than %s; a target is active when it has lights "+
		"from the last %d days. Ties go to more lights with no dark, then scaled, then short sets, then the latest night.",
		numText(DarkWeightBlocked), numText(DarkWeightScaled), numText(DarkWeightThin), calmatch.FullDarkRule(), numText(DarkHalfLifeDays),
		numText(DarkActiveFloor), DarkRecentDays)
}

type darkRank struct {
	today     time.Time
	lastNight time.Time
	active    map[string]bool
}

func targetKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func nightDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func newDarkRank(lights []darkLightGroup, now time.Time) darkRank {
	r := darkRank{today: nightDate(now.UTC()), active: map[string]bool{}}
	for _, l := range lights {
		if n := nightDate(l.Night); n.After(r.lastNight) {
			r.lastNight = n
		}
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

func (r darkRank) isLastNight(l darkLightGroup) bool {
	return !r.lastNight.IsZero() && nightDate(l.Night).Equal(r.lastNight)
}

func (r darkRank) lastNightText() string {
	if r.lastNight.IsZero() {
		return ""
	}
	return r.lastNight.Format(time.DateOnly)
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
	if r.isLastNight(l) {
		t.lastNight += l.N
	}
}

func roundScore(v float64) float64 {
	return math.Round(v*100) / 100
}

func scoreNote(c DarkCombo, lastNight string) string {
	note := fmt.Sprintf("Score %s = %s from lights with no dark + %s from lights on a scaled dark + %s from lights on a short set; "+
		"%d of its lights are from the last %d days, %d belong to active targets, and the latest night is %s.",
		numText(c.Score), numText(c.ScoreParts.Blocked), numText(c.ScoreParts.Scaled), numText(c.ScoreParts.Thin),
		c.LightsRecent, DarkRecentDays, c.LightsActive, c.LatestNight)
	if c.LightsLastNight > 0 {
		note += fmt.Sprintf(" %d of its lights are from the last imaging night, %s, so it ranks ahead of every combo without any.", c.LightsLastNight, lastNight)
	}
	return note
}
