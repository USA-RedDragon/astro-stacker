package discoverysource

import (
	"context"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

var syncMu sync.Mutex //nolint:gochecknoglobals

const updateChunk = 500

// Sync gives every candidate without a source the source it belongs to,
// starting a new source when none matches and joining the sources a
// candidate links. The newest vetting wins: a candidate vetted after its
// source's vetting last changed passes its vetting to the source; otherwise
// the candidate takes the source's. Sync is not cut short when ctx is
// cancelled, so a closed page never rolls back a half-done object.
func Sync(ctx context.Context, db *gorm.DB) error {
	syncMu.Lock()
	defer syncMu.Unlock()
	db = db.WithContext(context.WithoutCancel(ctx))
	var objects []string
	if err := db.Model(&app.DiscoveryCandidate{}).Where("source_id IS NULL").Distinct("object").Pluck("object", &objects).Error; err != nil {
		return err
	}
	for _, o := range objects {
		if err := db.Transaction(func(tx *gorm.DB) error { return syncObject(tx, o) }); err != nil {
			return err
		}
	}
	return db.Transaction(pruneEmpty)
}

// pruneEmpty removes the sources a rerun left with no candidates, and
// their vets, unless the user vetted them or something else keeps them:
// a rerun replaces its run's candidates, and a source whose candidates it
// did not find again is never listed or vetted again. On 2026-10-19, 863
// of 1831 mover sources and 23124 of 66788 change sources were such, some
// still carrying the unsure or artifact vet of their old candidates.
func pruneEmpty(tx *gorm.DB) error {
	q := tx.Model(&app.DiscoverySource{}).Select("id").Where("vetting = ''").
		Where("NOT EXISTS (?)", tx.Model(&app.DiscoveryCandidate{}).Select("1").Where("discovery_candidates.source_id = discovery_sources.id"))
	for _, m := range []any{&app.BinaryBook{}, &app.BinaryFacts{}, &app.EclipseRequest{}, &app.EclipseTiming{}, &app.ShareImage{}, &app.DiscoveryWatchNight{}, &app.DipWatch{}, &app.DipperEphemeris{}, &app.DipResult{}, &app.RunNight{}} {
		if tx.Migrator().HasTable(m) {
			q = q.Where("id NOT IN (?)", tx.Model(m).Select("source_id"))
		}
	}
	var ids []int
	if err := q.Pluck("id", &ids).Error; err != nil {
		return err
	}
	for chunk := range slices.Chunk(ids, updateChunk) {
		if err := tx.Where("source_id IN ?", chunk).Delete(&app.DiscoveryVet{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&app.DiscoverySource{}, chunk).Error; err != nil {
			return err
		}
	}
	return nil
}

// node is a source while an object is synced; a new one has no ID until
// it is stored.
type node struct {
	src     app.DiscoverySource
	members []app.DiscoveryCandidate
	fresh   []int
	joined  *node
	vetted  bool
	created bool
}

func (n *node) root() *node {
	for n.joined != nil {
		n = n.joined
	}
	return n
}

// bandArcsec is the height of the declination bands the changes are kept
// in, so a candidate is only compared with the sources in its own and the
// neighboring bands.
const bandArcsec = GaiaStarArcsec

func band(dec float64) int { return int(math.Floor(dec * 3600 / bandArcsec)) }

type index struct {
	bands  map[int][]*node
	movers []*node
}

func (ix *index) add(n *node, dec float64) {
	if n.src.Kind != app.DiscoveryKindChange {
		if !slices.Contains(ix.movers, n) {
			ix.movers = append(ix.movers, n)
		}
		return
	}
	b := band(dec)
	if !slices.Contains(ix.bands[b], n) {
		ix.bands[b] = append(ix.bands[b], n)
	}
}

func roots(list []*node, out []*node) []*node {
	for _, n := range list {
		if r := n.root(); !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func (ix *index) near(c app.DiscoveryCandidate) []*node {
	if c.Kind != app.DiscoveryKindChange {
		return roots(ix.movers, nil)
	}
	var out []*node
	b := band(c.Dec)
	for k := b - 1; k <= b+1; k++ {
		out = roots(ix.bands[k], out)
	}
	return out
}

func syncObject(tx *gorm.DB, object string) error {
	var fresh []app.DiscoveryCandidate
	if err := tx.Omit("light").Where("object = ? AND source_id IS NULL", object).Order("night, id").Find(&fresh).Error; err != nil {
		return err
	}
	var sources []app.DiscoverySource
	if err := tx.Where("object = ?", object).Order("id").Find(&sources).Error; err != nil {
		return err
	}
	var held []app.DiscoveryCandidate
	if err := tx.Select("id", "object", "kind", "night", "ra", "dec", "gaia_g", "source_id").
		Where("object = ? AND kind = ? AND source_id IS NOT NULL", object, app.DiscoveryKindChange).Find(&held).Error; err != nil {
		return err
	}
	ix := &index{bands: map[int][]*node{}}
	byID := map[int]*node{}
	all := make([]*node, 0, len(sources)+len(fresh))
	for _, s := range sources {
		n := &node{src: s}
		byID[s.ID] = n
		all = append(all, n)
		ix.add(n, s.Dec)
	}
	for _, m := range held {
		if n := byID[*m.SourceID]; n != nil {
			n.members = append(n.members, m)
			ix.add(n, m.Dec)
		}
	}
	for _, c := range fresh {
		var hits []*node
		for _, n := range ix.near(c) {
			if Matches(n.src, n.members, c) {
				hits = append(hits, n)
			}
		}
		var n *node
		if len(hits) == 0 {
			n = startNode(c)
			all = append(all, n)
		} else {
			sort.SliceStable(hits, func(i, j int) bool { return older(hits[i], hits[j]) })
			n = hits[0]
			for _, o := range hits[1:] {
				join(n, o)
			}
		}
		adopt(n, c)
		n.fresh = append(n.fresh, c.ID)
		if c.Kind == app.DiscoveryKindChange {
			n.members = append(n.members, c)
		}
		ix.add(n, c.Dec)
	}
	return store(tx, all)
}

// older orders nodes so the one stored first leads a join.
func older(a, b *node) bool {
	if a.created != b.created {
		return !a.created
	}
	return !a.created && a.src.ID < b.src.ID
}

func startNode(c app.DiscoveryCandidate) *node {
	s := app.DiscoverySource{Object: c.Object, Kind: c.Kind, RA: c.RA, Dec: c.Dec, GaiaG: c.GaiaG, Night: c.Night}
	if c.Kind == app.DiscoveryKindMover {
		a := alertOf(c)
		s.At, s.RateArcmin, s.PA = a.At, a.RateArcmin, a.PA
	}
	return &node{src: s, created: true}
}

func later(a, b *time.Time) bool {
	return a != nil && (b == nil || a.After(*b))
}

// join folds b into a; the newer vetting of the two stays, and every
// candidate of the joined source takes it.
func join(a, b *node) {
	if later(b.src.VetChangedAt, a.src.VetChangedAt) {
		a.src.Vetting, a.src.VetNote, a.src.VettedAt, a.src.VetChangedAt = b.src.Vetting, b.src.VetNote, b.src.VettedAt, b.src.VetChangedAt
	}
	a.members = append(a.members, b.members...)
	a.fresh = append(a.fresh, b.fresh...)
	b.members, b.fresh = nil, nil
	b.joined = a
	a.vetted = true
}

// adopt passes a candidate's vetting to its source when it is newer than
// the source's last change.
func adopt(n *node, c app.DiscoveryCandidate) {
	if c.Vetting == "" || !later(c.VettedAt, n.src.VetChangedAt) {
		return
	}
	n.src.Vetting, n.src.VetNote, n.src.VettedAt, n.src.VetChangedAt = c.Vetting, c.VetNote, c.VettedAt, c.VettedAt
	n.vetted = true
}

func store(tx *gorm.DB, all []*node) error {
	if err := create(tx, all); err != nil {
		return err
	}
	for _, n := range all {
		if n.joined == nil || n.created {
			continue
		}
		if err := tx.Model(&app.DiscoveryCandidate{}).Where("source_id = ?", n.src.ID).Update("source_id", n.root().src.ID).Error; err != nil {
			return err
		}
		if err := tx.Delete(&app.DiscoverySource{}, n.src.ID).Error; err != nil {
			return err
		}
	}
	assign := map[int]int{}
	for _, n := range all {
		if n.joined != nil {
			continue
		}
		for _, id := range n.fresh {
			assign[id] = n.src.ID
		}
	}
	if err := setSources(tx, assign); err != nil {
		return err
	}
	for _, n := range all {
		if n.joined != nil || (len(n.fresh) == 0 && !n.vetted) {
			continue
		}
		if n.vetted && !n.created {
			if err := tx.Model(&app.DiscoverySource{}).Where("id = ?", n.src.ID).Updates(vetColumns(n.src)).Error; err != nil {
				return err
			}
		}
		q := tx.Model(&app.DiscoveryCandidate{}).Where("source_id = ?", n.src.ID)
		if !n.vetted {
			q = q.Where("id IN ? AND (vetting <> ? OR vet_note <> ?)", n.fresh, n.src.Vetting, n.src.VetNote)
		}
		if err := q.Updates(VetFields(n.src.Vetting, n.src.VetNote, n.src.VettedAt)).Error; err != nil {
			return err
		}
	}
	return nil
}

func create(tx *gorm.DB, all []*node) error {
	var made []*node
	for _, n := range all {
		if n.joined == nil && n.created {
			made = append(made, n)
		}
	}
	for chunk := range slices.Chunk(made, updateChunk) {
		rows := make([]app.DiscoverySource, len(chunk))
		for i, n := range chunk {
			rows[i] = n.src
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		for i, n := range chunk {
			n.src.ID = rows[i].ID
		}
	}
	return nil
}

// setSources stores each candidate's source, many rows a statement.
func setSources(tx *gorm.DB, assign map[int]int) error {
	ids := make([]int, 0, len(assign))
	for id := range assign {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for chunk := range slices.Chunk(ids, updateChunk) {
		var b strings.Builder
		b.WriteString("CASE id")
		for _, id := range chunk {
			b.WriteString(" WHEN " + strconv.Itoa(id) + " THEN " + strconv.Itoa(assign[id]))
		}
		b.WriteString(" END")
		if err := tx.Model(&app.DiscoveryCandidate{}).Where("id IN ?", chunk).Update("source_id", gorm.Expr(b.String())).Error; err != nil {
			return err
		}
	}
	return nil
}

func vetColumns(s app.DiscoverySource) map[string]any {
	m := VetFields(s.Vetting, s.VetNote, s.VettedAt)
	m["vet_changed_at"] = s.VetChangedAt
	return m
}

// VetFields are the column updates that set a vetting.
func VetFields(state, note string, at *time.Time) map[string]any {
	return map[string]any{"vetting": state, "vet_note": note, "vetted_at": at}
}

// Vet sets a source's vetting and the same on each of its candidates. An
// empty state clears it. The change is newer than any candidate's earlier
// vetting, so a rerun's copies cannot bring an old one back.
func Vet(ctx context.Context, db *gorm.DB, id int, state, note string, at *time.Time) (app.DiscoverySource, error) {
	syncMu.Lock()
	defer syncMu.Unlock()
	var s app.DiscoverySource
	err := db.WithContext(context.WithoutCancel(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&s, id).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		s.Vetting, s.VetNote, s.VettedAt, s.VetChangedAt = state, note, at, &now
		if err := tx.Model(&app.DiscoverySource{}).Where("id = ?", id).Updates(vetColumns(s)).Error; err != nil {
			return err
		}
		return tx.Model(&app.DiscoveryCandidate{}).Where("source_id = ?", id).Updates(VetFields(state, note, at)).Error
	})
	return s, err
}
