package quality

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"

	"gorm.io/gorm"
)

// Target Scheduler's grading status values.
const (
	GradingPending  = 0
	GradingAccepted = 1
	GradingRejected = 2
)

// SubScore is one acquired image's score and grading, keyed by file name.
type SubScore struct {
	File          string
	Filter        string
	Exposure      float64
	GradingStatus int
	// Score is the weight relative to the best tenth of subs for the same
	// filter and exposure across all targets, capped at 1, times the
	// square of Transparency. 0 when the metadata can't be scored.
	Score float64
	// Transparency is the light the sub recorded against the best subs of
	// its target, filter, exposure, gain and framing (see transparency.go);
	// 1 when it can't be told.
	Transparency float64
	// TargetBest is the best Score among its target's subs in the same
	// filter, so a target imaged only on poor nights can be judged against
	// what it has.
	TargetBest float64
	// HFR and Stars are NINA's star measurements, used to pick a sharp
	// registration reference.
	HFR          float64
	Stars        int
	Eccentricity float64
}

type group struct {
	filter   string
	exposure float64
}

// fieldGroup is the subs whose light above the sky compares: one target's
// field, framed the same way, through one filter, exposure and gain.
type fieldGroup struct {
	target  string
	g       group
	gain    float64
	framing int
}

// row is an acquired image with its parsed metadata.
type row struct {
	GradingStatus int
	Target        string
	meta          Metadata
}

// Measured is a sub without a Target Scheduler record, measured from its
// pixels (ADU median and half-flux radius, as NINA measures them).
type Measured struct {
	File     string
	Target   string
	Filter   string
	Exposure float64
	SkyADU   float64
	Offset   float64 // camera offset, 0 when unknown
	// Calibrated subs came calibrated (Telescope.live), their pedestal
	// already taken off.
	Calibrated bool
	HFR        float64
	Stars      int
}

// LoadScores reads every acquired image from the scheduler database and
// scores it the same way astro-processing does.
//
// measured subs, which Target Scheduler has no record of, are scored in the
// same groups from their own measurements.
//
// It reads and parses every image's metadata; a Scorer keeps the parsed
// metadata between calls.
func LoadScores(ctx context.Context, db *gorm.DB, pedestal float64, measured []Measured) (map[string]SubScore, error) {
	return new(Scorer).Load(ctx, db, pedestal, measured)
}

// Scorer scores subs as LoadScores does, keeping each acquired image's
// parsed metadata between calls. Every call still reads each image's grading
// status, target and a checksum of its metadata, so grading, renamed targets
// and new, deleted or rewritten images all count; only the metadata of new
// or changed images is read and parsed again. The zero value is ready to
// use, and it is safe for concurrent use.
type Scorer struct {
	mu     sync.Mutex
	images map[int]cachedImage // by acquiredimage Id
}

type cachedImage struct {
	sum  string
	meta Metadata
	ok   bool // parsed, with a file name
}

// imageState is what Load reads of every acquired image on every call.
type imageState struct {
	ID            int
	GradingStatus int
	Target        string
	Sum           string
}

// metadataChunk bounds how many ids go in one IN list.
const metadataChunk = 1000

// sumExpr is the SQL for a value that changes whenever an image's metadata
// does. In Postgres that is the row's xmin, the transaction that last wrote
// it, which costs nothing to read (md5 of every blob costs 30 ms). SQLite has
// no md5, so there the metadata is its own checksum: it is read on every
// call, but still parsed only when it changes.
func sumExpr(db *gorm.DB) string {
	switch db.Name() {
	case "postgres":
		return "a.xmin::text"
	case "mysql":
		return "md5(a.metadata)"
	default:
		return "a.metadata"
	}
}

// Load scores every acquired image in db, and measured subs, as LoadScores.
func (s *Scorer) Load(ctx context.Context, db *gorm.DB, pedestal float64, measured []Measured) (map[string]SubScore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db = db.WithContext(ctx)
	states, err := s.listImages(db)
	if err != nil {
		return nil, err
	}

	if s.images == nil {
		s.images = make(map[int]cachedImage, len(states))
	}
	sums := make(map[int]string, len(states))
	var stale []int
	for _, st := range states {
		sums[st.ID] = st.Sum
		if c, ok := s.images[st.ID]; !ok || c.sum != st.Sum {
			stale = append(stale, st.ID)
		}
	}
	for id := range s.images {
		if _, ok := sums[id]; !ok {
			delete(s.images, id)
		}
	}
	for i := 0; i < len(stale); i += metadataChunk {
		ids := stale[i:min(i+metadataChunk, len(stale))]
		var metas []struct {
			ID       int
			Metadata string
		}
		if err := db.Table("acquiredimage").Select(`"Id" as id, metadata`).
			Where(`"Id" IN ?`, ids).Scan(&metas).Error; err != nil {
			return nil, fmt.Errorf("load acquired image metadata: %w", err)
		}
		for _, r := range metas {
			m, err := ParseMetadata(r.Metadata)
			// Kept under the checksum read with the grading: if the metadata
			// changed in between, the next call reads it again.
			s.images[r.ID] = cachedImage{sum: sums[r.ID], meta: m, ok: err == nil && m.FileName != ""}
		}
	}

	rows := make([]row, 0, len(states))
	for _, st := range states {
		c, ok := s.images[st.ID]
		if !ok || !c.ok {
			// Deleted since it was listed, or not scorable.
			continue
		}
		rows = append(rows, row{GradingStatus: st.GradingStatus, Target: st.Target, meta: c.meta})
	}
	return score(rows, pedestal, measured), nil
}

// listImages reads every acquired image's grading, target and checksum, in
// Id order. It scans the rows itself: gorm's reflection took most of a
// call's time.
func (s *Scorer) listImages(db *gorm.DB) ([]imageState, error) {
	rows, err := db.Table("acquiredimage a").
		Select(`a."Id", a."gradingStatus", t.name, ` + sumExpr(db)).
		Joins(`LEFT JOIN target t ON t."Id" = a."targetId"`).
		Order(`a."Id"`).Rows()
	if err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}
	defer rows.Close()
	states := make([]imageState, 0, len(s.images))
	for rows.Next() {
		var st imageState
		var target sql.NullString
		if err := rows.Scan(&st.ID, &st.GradingStatus, &target, &st.Sum); err != nil {
			return nil, fmt.Errorf("load acquired images: %w", err)
		}
		st.Target = target.String
		states = append(states, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}
	return states, nil
}

// score scores acquired images, in Id order, and measured subs.
func score(rows []row, pedestal float64, measured []Measured) map[string]SubScore {
	type item struct {
		s      SubScore
		raw    float64
		g      group
		target string
		// excess is the light above the sky (Excess), NaN for subs
		// measured by the stacker, and field the group it compares in.
		excess float64
		field  fieldGroup
	}
	items := make([]item, 0, len(rows)+len(measured))
	byGroup := map[group][]float64{}
	byField := map[fieldGroup][]float64{}
	for _, r := range rows {
		m := r.meta
		g := group{filter: m.FilterName, exposure: math.Round(float64(m.ExposureDuration))}
		raw := RawWeight(Sky(float64(m.ADUMedian), PedestalAt(pedestal, float64(m.Offset))), float64(m.HFR))
		field := fieldGroup{target: r.Target, g: g, gain: float64(m.Gain), framing: Framing(float64(m.RotatorPosition))}
		it := item{
			s: SubScore{
				File:          m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:],
				Filter:        g.filter,
				Exposure:      g.exposure,
				GradingStatus: r.GradingStatus,
				HFR:           float64(m.HFR),
				Stars:         int(m.DetectedStars),
				Eccentricity:  float64(m.Eccentricity),
			},
			raw:    raw,
			g:      g,
			target: r.Target,
			excess: Excess(float64(m.ADUMean), float64(m.ADUMedian)),
			field:  field,
		}
		items = append(items, it)
		if r.GradingStatus != GradingRejected {
			byGroup[g] = append(byGroup[g], raw)
			byField[field] = append(byField[field], it.excess)
		}
	}

	recorded := make(map[string]bool, len(items))
	for _, it := range items {
		recorded[it.s.File] = true
	}
	for _, m := range measured {
		if recorded[m.File] {
			continue
		}
		g := group{filter: m.Filter, exposure: math.Round(m.Exposure)}
		ped := PedestalAt(pedestal, m.Offset)
		if m.Calibrated {
			ped = 0
		}
		raw := RawWeight(Sky(m.SkyADU, ped), m.HFR)
		items = append(items, item{
			s:      SubScore{File: m.File, Filter: g.filter, Exposure: g.exposure, GradingStatus: GradingPending, HFR: m.HFR, Stars: m.Stars},
			raw:    raw,
			g:      g,
			target: m.Target,
			excess: math.NaN(),
		})
		byGroup[g] = append(byGroup[g], raw)
	}

	refs := make(map[group]float64, len(byGroup))
	for g, ws := range byGroup {
		refs[g] = Reference(ws)
	}
	fieldRefs := make(map[fieldGroup]float64, len(byField))
	for f, es := range byField {
		fieldRefs[f] = TransparencyReference(es)
	}
	type targetFilter struct {
		target string
		filter string
	}
	best := map[targetFilter]float64{}
	for i := range items {
		it := &items[i]
		ref := math.NaN()
		if !math.IsNaN(it.excess) {
			ref = fieldRefs[it.field]
		}
		it.s.Transparency = Transparency(it.excess, ref)
		if it.s.GradingStatus != GradingRejected {
			t := it.s.Transparency
			it.s.Score = Score(it.raw, refs[it.g]) * t * t
			k := targetFilter{it.target, it.s.Filter}
			best[k] = math.Max(best[k], it.s.Score)
		}
	}
	out := make(map[string]SubScore, len(items))
	for _, it := range items {
		it.s.TargetBest = best[targetFilter{it.target, it.s.Filter}]
		out[it.s.File] = it.s
	}
	return out
}
