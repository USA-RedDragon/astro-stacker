package vetting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StateNone is a source the job has not looked at.
const StateNone = "none"

// Status is a source's vetting as the panel reads it.
type Status struct {
	SourceID    int        `json:"source_id"`
	State       string     `json:"state"`
	Report      *Report    `json:"report"`
	Error       string     `json:"error"`
	RequestedAt *time.Time `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	// Stale says the report came from code a newer deploy replaced; the
	// next run vets the source again.
	Stale bool `json:"stale,omitempty"`
}

func statusOf(id int, row *app.DiscoveryVet, ann app.VetBuild, haveAnn bool) (Status, error) {
	if row == nil {
		return Status{SourceID: id, State: StateNone}, nil
	}
	st := Status{SourceID: id, State: row.State, Error: row.Error, RequestedAt: row.RequestedAt, FinishedAt: row.FinishedAt}
	st.Stale = row.Report != "" && staleRow(ann, haveAnn, *row, "")
	if row.Report != "" {
		var r Report
		if err := json.Unmarshal([]byte(row.Report), &r); err != nil {
			return st, fmt.Errorf("read the vetting report: %w", err)
		}
		st.Report = &r
	}
	return st, nil
}

// Load is source id's vetting.
func Load(ctx context.Context, db *gorm.DB, id int) (Status, error) {
	var row app.DiscoveryVet
	err := db.WithContext(ctx).Where("source_id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return statusOf(id, nil, app.VetBuild{}, false)
	}
	if err != nil {
		return Status{}, err
	}
	ann, haveAnn, err := announced(ctx, db)
	if err != nil {
		return Status{}, err
	}
	return statusOf(id, &row, ann, haveAnn)
}

// Request queues source id for a fresh run, keeping its last report until
// the new one is ready.
func Request(ctx context.Context, db *gorm.DB, id int, now time.Time) (Status, error) {
	db = db.WithContext(ctx)
	var src app.DiscoverySource
	if err := db.Select("id").First(&src, id).Error; err != nil {
		return Status{}, err
	}
	row := app.DiscoveryVet{SourceID: id, State: StateQueued, RequestedAt: &now}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"requested_at": now,
			"state":        gorm.Expr("CASE WHEN discovery_vets.state = ? THEN discovery_vets.state ELSE ? END", StateRunning, StateQueued),
			colUpdatedAt:   now,
		}),
	}).Create(&row).Error
	if err != nil {
		return Status{}, err
	}
	return Load(ctx, db, id)
}

// Due is a source the job should vet now, with the hash of what it saw
// and, when it was asked for by hand, when.
type Due struct {
	Summary discoverysource.Summary
	Hash    string
	AskedAt *time.Time
	// Prior is the source's last vet row, nil before its first.
	Prior *app.DiscoveryVet
}

// retryAfter is how long a failed run waits, and pendingAfter how long a
// run waiting on a survey's queue waits, before the job tries again.
const (
	retryAfter   = 6 * time.Hour
	pendingAfter = time.Hour
)

// refreshEvery is how long after its last run a source the user marked
// real is vetted again on its own, to take in the survey data added since:
// ATLAS adds epochs nightly, TESS a sector a month and ZTF a release every
// few months, so a week keeps the binary pages near current at the cost of
// one run per target a week. realPendingAfter is how long such a run
// waiting on a survey's queue waits before it tries again: a refresh is
// not urgent, and each run reads the target's own subs again.
const (
	refreshEvery     = 7 * 24 * time.Hour
	realPendingAfter = 6 * time.Hour
)

// RefreshEvery is how often a source the user marked real, or one under
// watch, is vetted again on its own.
const RefreshEvery = refreshEvery

// Pipeline versions of the sources with checks of their own: a source
// whose pipeline changes gets a new input hash and is vetted again.
const (
	moverPipeline     = "mover:9"
	transientPipeline = "transient:10"
	starPipeline      = "star:16"
)

func pipelineOf(s discoverysource.Summary) string {
	switch {
	case s.Source.Kind == kindMover:
		return moverPipeline
	case isTransient(s):
		return transientPipeline
	}
	return starPipeline
}

func hashOf(s discoverysource.Summary) string {
	parts := make([]string, 0, len(s.Members)+1)
	for _, m := range s.Members {
		parts = append(parts, fmt.Sprintf("%d:%s", m.ID, m.Class))
	}
	sort.Strings(parts)
	if p := pipelineOf(s); p != "" {
		parts = append(parts, p)
	}
	h := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(h[:8])
}

// Pick lists the sources due: new and not vetted by the user with no
// report, an older report version or pipeline, a report another build
// wrote after the newest build was announced, changed candidates or
// survey answers still to collect; those the user marked real with no
// report, an older report version or pipeline, changed candidates, survey
// answers still to collect or a report refreshEvery old; and any run
// requested by hand. ids narrows the pick to those sources, due or not.
func Pick(ctx context.Context, db *gorm.DB, now time.Time, objects []string, ids []int) ([]Due, error) {
	list, err := summaries(ctx, db, objects, ids)
	if err != nil {
		return nil, err
	}
	var rows []app.DiscoveryVet
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	ann, haveAnn, err := announced(ctx, db)
	if err != nil {
		return nil, err
	}
	vets := map[int]app.DiscoveryVet{}
	for _, r := range rows {
		vets[r.SourceID] = r
	}
	watches, err := discoverysource.WatchesOf(db.WithContext(ctx), nil)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Watch = watches[list[i].Source.ID]
	}
	forced := map[int]bool{}
	for _, id := range ids {
		forced[id] = true
	}
	var out []Due
	for _, s := range list {
		h := hashOf(s)
		v, seen := vets[s.Source.ID]
		if forced[s.Source.ID] || due(s, h, v, seen, now) || (seen && v.State == StateDone && staleRow(ann, haveAnn, v, pipelineOf(s)) && dueAgain(s)) {
			d := Due{Summary: s, Hash: h}
			if seen {
				d.Prior = &v
				if asked(v) {
					d.AskedAt = v.RequestedAt
				}
			}
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Summary.Source.Object != out[j].Summary.Source.Object {
			return out[i].Summary.Source.Object < out[j].Summary.Source.Object
		}
		return out[i].Summary.Source.ID < out[j].Summary.Source.ID
	})
	return out, nil
}

// asked says the source was asked for by hand since its last run started.
func asked(v app.DiscoveryVet) bool {
	return v.RequestedAt != nil && (v.StartedAt == nil || v.RequestedAt.After(*v.StartedAt))
}

// dueAgain says the job still vets the source on its own: new and not
// vetted by the user.
func dueAgain(s discoverysource.Summary) bool {
	return discoveries.IsNew(s.Class) && s.Source.Vetting == ""
}

// userReal says the user marked the source a real change of a star. The
// job vets such a source again on its own when its survey data could have
// grown, to keep its period and eclipse predictions current; the user's
// verdict is never changed. Moving objects have no survey light curves to
// grow.
func userReal(s discoverysource.Summary) bool {
	return s.Source.Vetting == app.VetReal && s.Source.Kind != kindMover
}

// refreshed says the job vets the source again weekly with fresh survey
// data: marked real by the user, or a change under watch for more fades.
func refreshed(s discoverysource.Summary) bool {
	return userReal(s) || (s.Watched() && s.Source.Kind != kindMover)
}

func due(s discoverysource.Summary, hash string, v app.DiscoveryVet, seen bool, now time.Time) bool {
	if seen && asked(v) {
		return true
	}
	byUser := refreshed(s)
	if !dueAgain(s) && !byUser {
		return false
	}
	if !seen {
		return true
	}
	switch {
	case v.State == StateRunning:
		return v.StartedAt == nil || now.Sub(*v.StartedAt) > 2*retryAfter
	case v.State == StateFailed:
		return v.FinishedAt == nil || now.Sub(*v.FinishedAt) > retryAfter
	case v.Version < Version, v.InputHash != hash:
		return true
	case v.Pending != "" && byUser:
		return v.FinishedAt == nil || now.Sub(*v.FinishedAt) > realPendingAfter
	case v.Pending != "":
		return v.FinishedAt == nil || now.Sub(*v.FinishedAt) > pendingAfter
	case byUser && v.State == StateDone:
		return v.FinishedAt == nil || now.Sub(*v.FinishedAt) >= refreshEvery
	}
	return v.State != StateDone
}

func summaries(ctx context.Context, db *gorm.DB, objects []string, ids []int) ([]discoverysource.Summary, error) {
	db = db.WithContext(ctx)
	q := db.Model(&app.DiscoverySource{})
	if len(objects) > 0 {
		q = q.Where("object IN ?", objects)
	}
	if len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	} else {
		askedIDs := db.Model(&app.DiscoveryVet{}).Select("source_id").
			Where("requested_at IS NOT NULL AND (started_at IS NULL OR requested_at > started_at)")
		q = q.Where("vetting = '' OR (vetting = ? AND kind <> ?) OR id IN (?)", app.VetReal, kindMover, askedIDs)
	}
	var sources []app.DiscoverySource
	if err := q.Find(&sources).Error; err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}
	sids := make([]int, len(sources))
	for i, s := range sources {
		sids[i] = s.ID
	}
	members := map[int][]app.DiscoveryCandidate{}
	for start := 0; start < len(sids); start += 500 {
		var cands []app.DiscoveryCandidate
		if err := db.Omit("light", "track").Where("source_id IN ?", sids[start:min(start+500, len(sids))]).Find(&cands).Error; err != nil {
			return nil, err
		}
		for _, c := range cands {
			members[*c.SourceID] = append(members[*c.SourceID], c)
		}
	}
	out := make([]discoverysource.Summary, 0, len(sources))
	for _, s := range sources {
		ms := members[s.ID]
		if len(ms) == 0 {
			continue
		}
		out = append(out, summaryOf(s, ms))
	}
	return out, nil
}

func summaryOf(s app.DiscoverySource, ms []app.DiscoveryCandidate) discoverysource.Summary {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if !a.Night.Equal(b.Night) {
			return a.Night.After(b.Night)
		}
		if a.Class != b.Class {
			return discoverysource.Better(a.Class, b.Class)
		}
		return a.SNR > b.SNR
	})
	out := discoverysource.Summary{Source: s, Members: ms, Class: ms[0].Class}
	nights := map[time.Time]bool{}
	for _, m := range ms {
		if discoverysource.Better(m.Class, out.Class) {
			out.Class = m.Class
		}
		d := m.Night.UTC().Truncate(24 * time.Hour)
		nights[d] = true
		if out.First.IsZero() || d.Before(out.First) {
			out.First = d
		}
		if d.After(out.Last) {
			out.Last = d
		}
	}
	for _, m := range ms {
		if m.Class == out.Class {
			out.Best = m
			break
		}
	}
	out.Seen = len(nights)
	return out
}

func markRunning(ctx context.Context, db *gorm.DB, ids []int, now time.Time) error {
	for _, id := range ids {
		row := app.DiscoveryVet{SourceID: id, State: StateRunning, StartedAt: &now}
		err := db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}},
			DoUpdates: clause.Assignments(map[string]any{"state": StateRunning, "started_at": now, colUpdatedAt: now}),
		}).Create(&row).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// stamp names the code that wrote a vetting: its pipeline and build.
type stamp struct {
	pipeline string
	build    string
}

// errNewerVet says the stored vetting came from a newer pipeline, which
// an older run does not overwrite.
var errNewerVet = errors.New("a newer pipeline already vetted the source")

func save(ctx context.Context, db *gorm.DB, id int, hash string, by stamp, r *Report, runErr error, now time.Time) error {
	var have app.DiscoveryVet
	err := db.WithContext(ctx).Select("pipeline").Where("source_id = ?", id).First(&have).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if newerPipeline(have.Pipeline, by.pipeline) {
		return errNewerVet
	}
	up := map[string]any{"finished_at": now, colUpdatedAt: now, "version": Version, "input_hash": hash,
		"pipeline": by.pipeline, "build": by.build}
	if runErr != nil {
		up["state"], up["error"] = StateFailed, runErr.Error()
	} else {
		r.Pipeline, r.Build = by.pipeline, by.build
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		up["state"], up["error"], up["report"] = StateDone, "", string(b)
		up["suggested"], up["confidence"] = r.Suggestion.State, r.Suggestion.Confidence
		up["pending"] = strings.Join(r.Pending, ",")
	}
	q := db.WithContext(ctx).Model(&app.DiscoveryVet{}).Where("source_id = ?", id)
	if have.Pipeline != "" {
		q = q.Where("pipeline = ?", have.Pipeline)
	}
	return q.Updates(up).Error
}
