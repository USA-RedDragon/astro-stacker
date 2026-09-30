package quality_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"gorm.io/gorm"
)

// legacyLoadScores is LoadScores as it was before the Scorer: one query
// reading every image's metadata, parsed on every call. Scorer must match it.
func legacyLoadScores(ctx context.Context, db *gorm.DB, pedestal float64, measured []quality.Measured) map[string]quality.SubScore {
	type legacyRow struct {
		GradingStatus int
		Target        string
		Metadata      string
	}
	var rows []legacyRow
	if err := db.WithContext(ctx).Table("acquiredimage").
		Select(`acquiredimage."gradingStatus" as grading_status, target.name as target, acquiredimage.metadata`).
		Joins(`LEFT JOIN target ON target."Id" = acquiredimage."targetId"`).Scan(&rows).Error; err != nil {
		panic(err)
	}
	type group struct {
		filter   string
		exposure float64
	}
	type item struct {
		s      quality.SubScore
		raw    float64
		g      group
		target string
	}
	items := make([]item, 0, len(rows))
	byGroup := map[group][]float64{}
	for _, r := range rows {
		m, err := quality.ParseMetadata(r.Metadata)
		if err != nil || m.FileName == "" {
			continue
		}
		g := group{filter: m.FilterName, exposure: math.Round(float64(m.ExposureDuration))}
		raw := quality.RawWeight(quality.Sky(float64(m.ADUMedian), quality.PedestalAt(pedestal, float64(m.Offset))), float64(m.HFR))
		items = append(items, item{
			s: quality.SubScore{
				File: m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:], Filter: g.filter, Exposure: g.exposure,
				GradingStatus: r.GradingStatus, HFR: float64(m.HFR), Stars: int(m.DetectedStars), Eccentricity: float64(m.Eccentricity),
			},
			raw: raw, g: g, target: r.Target,
		})
		if r.GradingStatus != quality.GradingRejected {
			byGroup[g] = append(byGroup[g], raw)
		}
	}
	recorded := map[string]bool{}
	for _, it := range items {
		recorded[it.s.File] = true
	}
	for _, m := range measured {
		if recorded[m.File] {
			continue
		}
		g := group{filter: m.Filter, exposure: math.Round(m.Exposure)}
		ped := quality.PedestalAt(pedestal, m.Offset)
		if m.Calibrated {
			ped = 0
		}
		items = append(items, item{
			s:   quality.SubScore{File: m.File, Filter: g.filter, Exposure: g.exposure, GradingStatus: quality.GradingPending, HFR: m.HFR, Stars: m.Stars},
			raw: quality.RawWeight(quality.Sky(m.SkyADU, ped), m.HFR), g: g, target: m.Target,
		})
		byGroup[g] = append(byGroup[g], items[len(items)-1].raw)
	}
	refs := map[group]float64{}
	for g, ws := range byGroup {
		refs[g] = quality.Reference(ws)
	}
	type targetFilter struct{ target, filter string }
	best := map[targetFilter]float64{}
	for i := range items {
		it := &items[i]
		if it.s.GradingStatus != quality.GradingRejected {
			it.s.Score = quality.Score(it.raw, refs[it.g])
			k := targetFilter{it.target, it.s.Filter}
			best[k] = math.Max(best[k], it.s.Score)
		}
	}
	out := map[string]quality.SubScore{}
	for _, it := range items {
		it.s.TargetBest = best[targetFilter{it.target, it.s.Filter}]
		out[it.s.File] = it.s
	}
	return out
}

// schedulerFixture is a scheduler database with n acquired images over a few
// targets, filters and exposures, some graded, rejected, unmeasured,
// unparseable, at offset 240 or with no target.
func schedulerFixture(t testing.TB, n int) *gorm.DB {
	t.Helper()
	db := emptyScheduler(t)
	fillScheduler(t, db, n)
	return db
}

// fillScheduler adds the images schedulerFixture has to db's empty tables.
func fillScheduler(t testing.TB, db *gorm.DB, n int) {
	t.Helper()
	for i, name := range []string{"M31", "Crescent", "Cygnus Loop Panel 1", "NGC 7000"} {
		if err := db.Exec(`INSERT INTO target ("Id", name) VALUES (?, ?)`, i+1, name).Error; err != nil {
			t.Fatal(err)
		}
	}
	filters := []string{"H-a", "O-III", "S-II", "Red", "Green", "Blue", "Lum"}
	exposures := []float64{60, 120, 300, 600}
	for i := range n {
		f := filters[i%len(filters)]
		exp := exposures[(i/7)%len(exposures)]
		hfr := fmt.Sprint(1.4 + float64(i%37)*0.05)
		if i%53 == 0 {
			hfr = `"NaN"`
		}
		offset := 50
		if i%41 == 0 {
			offset = 240
		}
		adu := 560 + (i*7919)%900
		if offset == 240 {
			adu += 1900
		}
		meta := fmt.Sprintf(`{"FileName":"A:\\NINA\\T\\LIGHT\\2026-09-%02d_%s_%.2fs_%05d.xisf","SessionId":%d,"FilterName":%q,`+
			`"ExposureStartTime":"2026-09-29T03:50:31Z","ExposureDuration":%v,"Offset":%d,"DetectedStars":%d,"HFR":%s,`+
			`"FWHM":"NaN","Eccentricity":%v,"ADUMedian":%d,"Airmass":1.02,`+
			// The rest of a real blob, which scoring ignores: ~1 KB in all.
			`"Gain":100,"Binning":"1x1","ReadoutMode":0,"ROI":100.0,"HFRStDev":0.060564293162900906,`+
			`"ADUStDev":253.42457485676186,"ADUMean":1703.7149106104757,"ADUMin":697,"ADUMax":65535,`+
			`"GuidingRMSScale":5.09296,"GuidingRMS":0.22016996372053474,"GuidingRMSArcSec":1.1213168184301345,`+
			`"GuidingRMSRA":0.16432550629134338,"GuidingRMSRAArcSec":0.8369032305215601,"GuidingRMSDEC":0.14653307103447757,`+
			`"GuidingRMSDECArcSec":0.7462870694557528,"FocuserPosition":26252,"FocuserTemp":28.059999465942383,`+
			`"RotatorPosition":180.9436798095703,"RotatorMechanicalPosition":142.39999389648438,"PierSide":"East",`+
			`"CameraTemp":5.0,"CameraTargetTemp":5.0}`,
			1+i%28, f, exp, i, i/50, f, exp, offset, 100+i%700, hfr, 0.3+float64(i%9)*0.02, adu)
		switch {
		case i%97 == 0:
			meta = `not json`
		case i%89 == 0:
			meta = `{"FilterName":"H-a","HFR":2}`
		}
		targetID := 1 + i%4
		if i%61 == 0 {
			targetID = 99 // no such target
		}
		if err := db.Exec(`INSERT INTO acquiredimage ("Id", "targetId", "gradingStatus", metadata) VALUES (?, ?, ?, ?)`,
			i+1, targetID, i%3, meta).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func sameScores(t *testing.T, step string, got, want map[string]quality.SubScore) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d scores, want %d", step, len(got), len(want))
	}
	eq := func(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }
	for f, w := range want {
		g, ok := got[f]
		if !ok {
			t.Errorf("%s: %s missing", step, f)
			continue
		}
		if g.File != w.File || g.Filter != w.Filter || g.GradingStatus != w.GradingStatus || g.Stars != w.Stars ||
			!eq(g.Exposure, w.Exposure) || !eq(g.Score, w.Score) || !eq(g.TargetBest, w.TargetBest) ||
			!eq(g.HFR, w.HFR) || !eq(g.Eccentricity, w.Eccentricity) {
			t.Errorf("%s: %s = %+v, want %+v", step, f, g, w)
		}
	}
}

// A Scorer gives the scores the one-query LoadScores gave, as the scheduler
// database changes under it, while reading only changed images' metadata.
func TestScorerMatchesLegacyAsImagesChange(t *testing.T) {
	t.Parallel()
	db := schedulerFixture(t, 1500)
	var metadataReads atomic.Int64
	if err := db.Callback().Row().After("gorm:row").Register("count_metadata", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), `"Id" IN`) {
			metadataReads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	measured := []quality.Measured{
		{File: "tl_1.fits", Target: "Crescent", Filter: "H-a", Exposure: 600, SkyADU: 177, HFR: 2.5, Calibrated: true},
		{File: "own_1.fits", Target: "M31", Filter: "Red", Exposure: 120, SkyADU: 700, Offset: 50, HFR: 1.9, Stars: 300},
	}
	var s quality.Scorer
	check := func(step string, wantReads int64) {
		t.Helper()
		metadataReads.Store(0)
		got, err := s.Load(t.Context(), db, 506, measured)
		if err != nil {
			t.Fatal(err)
		}
		sameScores(t, step, got, legacyLoadScores(t.Context(), db, 506, measured))
		if n := metadataReads.Load(); n != wantReads {
			t.Errorf("%s: %d metadata reads, want %d", step, n, wantReads)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}

	check("first load", 2) // 1500 images in chunks of 1000
	check("unchanged", 0)

	exec(`UPDATE acquiredimage SET "gradingStatus" = 2 WHERE "Id" IN (2, 3, 4, 5, 6, 7, 8)`)
	exec(`UPDATE acquiredimage SET "gradingStatus" = 1 WHERE "Id" IN (9, 10, 11)`)
	check("regraded", 0)

	exec(`UPDATE target SET name = 'Andromeda' WHERE "Id" = 1`)
	check("target renamed", 0)

	exec(`INSERT INTO acquiredimage ("Id", "targetId", "gradingStatus", metadata) VALUES (5000, 2, 0,
		'{"FileName":"C:\\new\\sharp.xisf","FilterName":"H-a","ExposureDuration":600,"HFR":0.9,"ADUMedian":640,"DetectedStars":900}')`)
	check("image added", 1)

	exec(`DELETE FROM acquiredimage WHERE "Id" IN (12, 5000)`)
	check("images deleted", 0)

	exec(`UPDATE acquiredimage SET metadata = replace(metadata, '"HFR":', '"HFR":3') WHERE "Id" = 20`)
	exec(`UPDATE acquiredimage SET metadata = 'not json' WHERE "Id" = 21`)
	exec(`UPDATE acquiredimage SET metadata = (SELECT metadata FROM acquiredimage WHERE "Id" = 30) WHERE "Id" = 97`)
	check("metadata rewritten", 1)

	measured = measured[:1]
	check("measured changed", 0)
}

func BenchmarkLoadScores(b *testing.B) {
	db := schedulerFixture(b, 16000)
	b.Run("legacy", func(b *testing.B) {
		for b.Loop() {
			legacyLoadScores(b.Context(), db, 506, nil)
		}
	})
	b.Run("scorer", func(b *testing.B) {
		var s quality.Scorer
		for b.Loop() {
			if _, err := s.Load(b.Context(), db, 506, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}
