package vetting

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"gorm.io/gorm"
)

// Bad nights: on some nights nearly every change a run finds goes the same
// way, because the frames are softer or sharper than the reference, not
// because the stars changed. 28575 and 28454 (Abell 85, 2026-01-17) were
// flagged only on a night when 85 of 96 O-III and 91 of 100 H-a changes were
// fades, and 30023 (Cygnus Loop Panel 2, 2026-08-12) on one when 235 of 264
// H-a changes were; the surveys saw all three steady for years. No source
// vetted real or known has every night so: the most one-sided runs under
// one are 16727's 2025-06-27 broadband, 78% brightenings, and 236's, 69%
// fades.
const (
	// skewRunMin is the fewest changes a run needs for its share of fades
	// to say anything about the night.
	skewRunMin = 50
	// skewShare is the share of a run's changes that must go the source's
	// way for the night to be a bad one.
	skewShare = 0.85
	// sayNoSurveySaw is the verdict suggest gives when the surveys saw the
	// star steady and nothing else settled it.
	sayNoSurveySaw = "Not sure: no survey saw it change"
	sayBadNight    = "Artifact: flagged only on bad nights"
)

// runSkew counts a run's changes and how many of them were fades.
type runSkew struct {
	N, Fainter int
}

// share is the share of the run's changes that went the way of sign.
func (r runSkew) share(sign int) float64 {
	if r.N == 0 {
		return 0
	}
	if sign < 0 {
		return float64(r.Fainter) / float64(r.N)
	}
	return float64(r.N-r.Fainter) / float64(r.N)
}

// runSkews counts the changes of each run the source's changes came from.
func runSkews(ctx context.Context, db *gorm.DB, members []app.DiscoveryCandidate) (map[int]runSkew, error) {
	ids := map[int]bool{}
	for _, m := range members {
		if m.Kind == app.DiscoveryKindChange && m.RunID > 0 {
			ids[m.RunID] = true
		}
	}
	if db == nil || len(ids) == 0 {
		return map[int]runSkew{}, nil
	}
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var rows []struct {
		RunID   int
		N       int
		Fainter int
	}
	err := db.WithContext(ctx).Model(&app.DiscoveryCandidate{}).
		Select("run_id, count(*) AS n, sum(CASE WHEN sign < 0 THEN 1 ELSE 0 END) AS fainter").
		Where("run_id IN ? AND kind = ?", list, app.DiscoveryKindChange).Group("run_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int]runSkew, len(rows))
	for _, r := range rows {
		out[r.RunID] = runSkew{N: r.N, Fainter: r.Fainter}
	}
	return out, nil
}

// badNights says how every night the source was flagged on was a bad
// night, with at least one run the source changed in where skewShare of
// skewRunMin or more changes went its way, or "" when one was not.
func badNights(members []app.DiscoveryCandidate, skews map[int]runSkew, runs map[int]app.DiscoveryRun) string {
	type worst struct {
		m     app.DiscoveryCandidate
		share float64
	}
	nights := map[time.Time]*worst{}
	for _, m := range members {
		if m.Kind != app.DiscoveryKindChange || m.Sign == 0 {
			continue
		}
		n := m.Night.UTC()
		w, ok := nights[n]
		if !ok {
			w = &worst{}
			nights[n] = w
		}
		r := skews[m.RunID]
		if r.N < skewRunMin {
			continue
		}
		if s := r.share(m.Sign); s >= skewShare && s > w.share {
			w.m, w.share = m, s
		}
	}
	if len(nights) == 0 {
		return ""
	}
	keys := make([]time.Time, 0, len(nights))
	for n, w := range nights {
		if w.share == 0 {
			return ""
		}
		keys = append(keys, n)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	parts := make([]string, 0, len(keys))
	for _, n := range keys {
		m := nights[n].m
		r := skews[m.RunID]
		way, k := "brightenings", r.N-r.Fainter
		if m.Sign < 0 {
			way, k = "fades", r.Fainter
		}
		p := fmt.Sprintf("on %s, %d of the %d changes found in %s were %s", dateWords(n), k, r.N, m.Group, way)
		if run, ok := runs[m.RunID]; ok && run.HFRNight > 0 && run.HFRRef > 0 {
			p += fmt.Sprintf(", with stars %.2f times as wide as in the reference", run.HFRNight/run.HFRRef)
		}
		parts = append(parts, p)
	}
	return sentenceStart(strings.Join(parts, "; ")) + ". So many changes going one way come from the night's frames, not the stars, and this spot was flagged on no other night."
}

// onBadNights turns a Not sure that only the detection speaks for into an
// artifact when every night it was flagged on was a bad night.
func onBadNights(s Suggestion, why string, groups []Group) Suggestion {
	if why == "" || s.State != app.VetUnsure || (s.Say != sayNotSure && s.Say != sayNoSurveySaw) {
		return s
	}
	conf := Low
	if s.Say == sayNoSurveySaw {
		conf = Medium
	}
	ztf := ""
	if s.Say == sayNoSurveySaw {
		ztf = evidence(groups, check.ZTF)
	}
	return Suggestion{State: app.VetArtifact, Say: sayBadNight, Confidence: conf, Why: joinSentences(why, ztf)}
}
