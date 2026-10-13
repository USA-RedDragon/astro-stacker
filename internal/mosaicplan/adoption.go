package mosaicplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KindFrames    = "frames"
	LinkReview    = "review"
	AutoDecider   = "astro-stacker"
	framesMaxSep  = 0.5
	framesHighSep = 0.2
)

var (
	ErrNotFound    = errors.New("not found")
	ErrBadDecision = errors.New("decision must be accept or reject")
	ErrBadRequest  = errors.New("bad request")
)

type Service struct {
	App     *gorm.DB
	Sched   *gorm.DB
	Rig     mosaics.Rig
	Measure func(ctx context.Context) rigsource.Rig
	Site    SiteSource
	Horizon HorizonSource
	// BodyNight places a moving-body target on a night; nil leaves every
	// target at its stored coordinates.
	BodyNight BodyNightFunc

	kickOnce sync.Once
	kick     *trigger.Debouncer
}

// Adoption kicks are debounced by AdoptionQuiet and AdoptionMaxWait, and
// passes during imaging are kept AdoptionMinGap apart.
const (
	AdoptionQuiet   = 2 * time.Minute
	AdoptionMaxWait = 10 * time.Minute
	AdoptionMinGap  = 15 * time.Minute
)

func (s *Service) debouncer() *trigger.Debouncer {
	s.kickOnce.Do(func() {
		s.kick = &trigger.Debouncer{Quiet: AdoptionQuiet, MaxWait: AdoptionMaxWait, MinGap: AdoptionMinGap}
	})
	return s.kick
}

// Kick asks for an adoption pass soon, as when projects or targets change
// or new lights are indexed.
func (s *Service) Kick() { s.debouncer().Kick() }

func New(appDB, sched *gorm.DB) *Service {
	return &Service{App: appDB, Sched: sched}
}

func (s *Service) rig(ctx context.Context) (mosaics.Rig, rigsource.Rig, error) {
	if s.Measure != nil {
		info := s.Measure(ctx)
		if r, ok := info.Mosaic(); ok {
			return r, info, nil
		}
		return mosaics.Rig{}, info, rigsource.ErrUnknown
	}
	if s.Rig.WidthDeg > 0 && s.Rig.HeightDeg > 0 {
		info := rigsource.Empty()
		info.Basis.Source = "static"
		w, h, sc := s.Rig.WidthDeg, s.Rig.HeightDeg, s.Rig.ScaleArcsec
		info.WidthDeg, info.HeightDeg, info.Scale = &w, &h, &sc
		return s.Rig, info, nil
	}
	return mosaics.Rig{}, rigsource.Empty(), rigsource.ErrUnknown
}

type Adoption struct {
	ID          int                    `json:"id"`
	Subject     string                 `json:"subject"`
	ProjectGUID string                 `json:"projectGuid,omitempty"`
	Project     string                 `json:"project"`
	Kind        string                 `json:"kind"`
	Confidence  string                 `json:"confidence"`
	Rule        string                 `json:"rule"`
	Issue       string                 `json:"issue"`
	Suggestion  string                 `json:"suggestion"`
	Status      string                 `json:"status"`
	Clean       bool                   `json:"clean"`
	Action      string                 `json:"action,omitempty"`
	DecidedBy   string                 `json:"decidedBy,omitempty"`
	DecidedAt   *time.Time             `json:"decidedAt,omitempty"`
	FoundAt     *time.Time             `json:"foundAt,omitempty"`
	WordedAt    *time.Time             `json:"wordedAt,omitempty"`
	Panels      []mosaics.AdoptedPanel `json:"panels,omitempty"`
	Frames      *FramesProposal        `json:"frames,omitempty"`
}

type FramesProposal struct {
	Object      string   `json:"object"`
	Count       int      `json:"count"`
	TargetGUID  string   `json:"targetGuid,omitempty"`
	Target      string   `json:"target,omitempty"`
	ProjectGUID string   `json:"projectGuid,omitempty"`
	Separation  *float64 `json:"separationDeg"`
	Nearest     string   `json:"nearest,omitempty"`
	Within      int      `json:"within"`
	Instrument  string   `json:"instrument,omitempty"`
	TargetRig   string   `json:"targetRig,omitempty"`
	Coords      bool     `json:"coords"`
	NameMatch   bool     `json:"nameMatch"`
	ProjectName string   `json:"projectNamed,omitempty"`
	LinkedByTS  bool     `json:"linkedByTs,omitempty"`
}

func (fp FramesProposal) store() mosaicstore.FramesProposal {
	out := mosaicstore.FramesProposal{Object: fp.Object, Count: fp.Count, TargetGUID: fp.TargetGUID, Target: fp.Target, ProjectGUID: fp.ProjectGUID}
	if fp.Separation != nil {
		out.Separation = *fp.Separation
	}
	return out
}

type Report struct {
	DryRun    bool       `json:"dryRun"`
	Clean     int        `json:"clean"`
	New       int        `json:"new"`
	Review    int        `json:"review"`
	Auto      int        `json:"auto"`
	Unchanged int        `json:"unchanged"`
	Changed   int        `json:"changed"`
	Items     []Adoption `json:"items"`
}

type candidate struct {
	row    app.MosaicAdoption
	auto   bool
	panels []mosaics.AdoptedPanel
	frames *FramesProposal
}

type pool struct {
	cands    []candidate
	rig      mosaics.Rig
	projects map[string]bool
	planned  map[string]bool
}

func (s *Service) candidates(ctx context.Context) (pool, error) {
	targets, err := tslink.Targets(ctx, s.Sched)
	if err != nil {
		return pool{}, err
	}
	var wizard []string
	if err := s.App.WithContext(ctx).Model(&app.MosaicPanel{}).Where("source = ?", app.MosaicSourceWizard).
		Distinct("project_guid").Pluck("project_guid", &wizard).Error; err != nil {
		return pool{}, err
	}
	planned := map[string]bool{}
	for _, g := range wizard {
		planned[g] = true
	}
	inTS := map[string]bool{}
	var projects []mosaics.TSProject
	for _, p := range tslink.GroupProjects(targets) {
		inTS[p.GUID] = true
		if p.GUID == "" || planned[p.GUID] {
			continue
		}
		tp := mosaics.TSProject{GUID: p.GUID, Name: p.Name, IsMosaic: p.IsMosaic}
		for _, t := range p.Targets {
			tp.Targets = append(tp.Targets, mosaics.TSTarget{GUID: t.GUID, Name: t.Name, RA: t.RA, Dec: t.Dec, Rotation: t.Rotation, Active: t.Active})
		}
		projects = append(projects, tp)
	}
	rig, info, err := s.rig(ctx)
	if err != nil {
		return pool{}, err
	}
	var out []candidate
	for _, p := range mosaics.Propose(projects, rig) {
		body, err := json.Marshal(p.Panels)
		if err != nil {
			return pool{}, err
		}
		out = append(out, candidate{
			row: app.MosaicAdoption{Subject: "project:" + p.ProjectGUID, ProjectGUID: p.ProjectGUID, Project: p.Project, Kind: p.Kind,
				Confidence: p.Confidence, Rule: p.Rule, Issue: p.Issue, Suggestion: p.Suggestion, Proposal: string(body), Fingerprint: p.Fingerprint},
			auto: p.Auto, panels: p.Panels,
		})
	}
	frames, err := s.frameCandidates(ctx, targets, rigInstrument(info))
	if err != nil {
		return pool{}, err
	}
	return pool{cands: append(out, frames...), rig: rig, projects: inTS, planned: planned}, nil
}

func (s *Service) frameCandidates(ctx context.Context, targets []tslink.Target, rigKit instrument) ([]candidate, error) {
	var rows []struct {
		Object   string
		N        int
		MountRA  *float64
		MountDec *float64
	}
	if err := s.App.WithContext(ctx).Table("frames f").
		Select("f.object, count(*) AS n, avg(f.mount_ra) AS mount_ra, avg(f.mount_dec) AS mount_dec").
		Joins("LEFT JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("(ft.id IS NULL OR ft.method = ?) AND f.type = ? AND f.index_error IS NULL AND f.object <> ''", LinkReview, "LIGHT").
		Group("f.object").Order("f.object").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load unlinked lights: %w", err)
	}
	named := map[string]bool{}
	projects := map[string]bool{}
	for _, t := range targets {
		named[t.Name] = true
		projects[t.Project] = true
	}
	var linked []string
	if err := s.App.WithContext(ctx).Model(&app.FrameTarget{}).Where("method <> ?", LinkReview).Distinct("object").Pluck("object", &linked).Error; err != nil {
		return nil, err
	}
	subsKit, err := subsInstruments(ctx, s.App)
	if err != nil {
		return nil, err
	}
	ownKit, err := targetInstruments(ctx, s.App)
	if err != nil {
		return nil, err
	}
	for _, o := range linked {
		named[o] = true
	}
	var out []candidate
	for _, r := range rows {
		if named[r.Object] {
			continue
		}
		fp := &FramesProposal{Object: r.Object, Count: r.N, Coords: r.MountRA != nil && r.MountDec != nil}
		if projects[r.Object] {
			fp.ProjectName = r.Object
		}
		row := app.MosaicAdoption{Subject: "frames:" + r.Object, Project: r.Object, Kind: KindFrames}
		row.Issue = framesIssue(r.Object, r.N, fp.ProjectName != "")
		if fp.Coords {
			nearestFrames(fp, *r.MountRA, *r.MountDec, targets)
		}
		row.Confidence, row.Rule, row.Suggestion = framesVerdict(fp)
		key := fp.TargetGUID
		if row.Confidence == mosaics.ConfidenceLow {
			key = ""
		} else if own, ok := ownKit[fp.TargetGUID]; ok || rigKit.known() {
			if !ok {
				own = rigKit
			}
			if sub, ok := subsKit[r.Object]; ok && differentInstrument(sub, own) {
				fp.Instrument, fp.TargetRig = sub.String(), own.String()
				row.Confidence, row.Rule, row.Suggestion = instrumentVerdict(fp)
			}
		}
		if row.Confidence == mosaics.ConfidenceLow {
			fp.TargetGUID, fp.Target, fp.ProjectGUID = "", "", ""
		}
		row.Fingerprint = fingerprint(r.Object, key)
		body, err := json.Marshal(fp)
		if err != nil {
			return nil, err
		}
		row.Proposal = string(body)
		out = append(out, candidate{row: row, frames: fp})
	}
	return out, nil
}

func nearestFrames(fp *FramesProposal, ra, dec float64, targets []tslink.Target) {
	for _, t := range targets {
		if t.GUID == "" {
			continue
		}
		d := separation(ra, dec, t.RA, t.Dec)
		if d <= framesMaxSep {
			fp.Within++
		}
		if fp.Separation == nil || d < *fp.Separation {
			fp.Separation, fp.TargetGUID, fp.Target, fp.ProjectGUID, fp.Nearest = &d, t.GUID, t.Name, t.ProjectGUID, t.Name
			fp.NameMatch = sameName(fp.Object, t.Name) || sameName(fp.Object, t.Project)
		}
	}
}

func framesIssue(object string, n int, projectNamed bool) string {
	subs := fmt.Sprintf("%d subs are", n)
	if n == 1 {
		subs = "1 sub is"
	}
	if projectNamed {
		return fmt.Sprintf("%s named %q, which is a Target Scheduler project, but none of its targets has that name and the subs aren't linked to one.", subs, object)
	}
	return fmt.Sprintf("%s named %q, and no Target Scheduler target has that name or is linked to them.", subs, object)
}

func framesVerdict(fp *FramesProposal) (confidence, rule, suggestion string) {
	keep := "Keep them separate and count their hours on their own."
	switch {
	case !fp.Coords:
		return mosaics.ConfidenceLow, "Low: the subs have no mount coordinates, so they can't be matched to a target by position.",
			"The subs have no mount coordinates, so no target can be matched by position. " + keep
	case fp.Separation == nil:
		return mosaics.ConfidenceLow, "Low: Target Scheduler has no targets to compare against.", "Target Scheduler has no targets. " + keep
	case *fp.Separation > framesMaxSep:
		return mosaics.ConfidenceLow, fmt.Sprintf("Low: no target within %.1f° of the subs' mount position.", framesMaxSep),
			fmt.Sprintf("The nearest Target Scheduler target, %s, is %.2f° from the subs' mount position, beyond the %.1f° limit. %s", fp.Nearest, *fp.Separation, framesMaxSep, keep)
	}
	if fp.Within > 1 {
		return mosaics.ConfidenceMedium, fmt.Sprintf("Medium: %d targets are within %.1f° of the subs' mount position, so the nearest may not be the right one.", fp.Within, framesMaxSep),
			fmt.Sprintf("Attach them to %s, the nearest of %d targets within %.1f° (%.2f° away), or to whichever of them they were taken for.", fp.Target, fp.Within, framesMaxSep, *fp.Separation)
	}
	names := "the names don't match, so check it is the same object"
	if fp.NameMatch {
		names = "the names match"
	}
	suggestion = fmt.Sprintf("Attach them to %s: its coordinates are %.2f° from the subs' mount position, and %s.", fp.Target, *fp.Separation, names)
	switch {
	case *fp.Separation <= framesHighSep && fp.NameMatch:
		return mosaics.ConfidenceHigh, fmt.Sprintf("High: a target within %.1f° of the subs' mount position, and the names match.", framesHighSep), suggestion
	case fp.NameMatch:
		return mosaics.ConfidenceMedium, fmt.Sprintf("Medium: a target within %.1f° of the subs' mount position and the names match, but it is more than %.1f° away.", framesMaxSep, framesHighSep), suggestion
	}
	return mosaics.ConfidenceMedium, fmt.Sprintf("Medium: a target within %.1f° of the subs' mount position, but the names don't match.", framesMaxSep), suggestion
}

func instrumentVerdict(fp *FramesProposal) (confidence, rule, suggestion string) {
	return mosaics.ConfidenceLow,
		fmt.Sprintf("Low: the subs come from a different instrument (%s) than the rig that images %s (%s).", fp.Instrument, fp.Target, fp.TargetRig),
		fmt.Sprintf("%s is %.2f° from the subs' mount position, but the subs were taken with %s, not %s, the rig that images it. Keep them separate and count their hours on their own.",
			fp.Target, *fp.Separation, fp.Instrument, fp.TargetRig)
}

func sameName(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(mosaics.PanelName.ReplaceAllString(s, ""))
		return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)
	}
	return norm(a) != "" && (strings.Contains(norm(b), norm(a)) || strings.Contains(norm(a), norm(b)))
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

func separation(ra1, dec1, ra2, dec2 float64) float64 {
	const d = math.Pi / 180
	c := math.Sin(dec1*d)*math.Sin(dec2*d) + math.Cos(dec1*d)*math.Cos(dec2*d)*math.Cos((ra1-ra2)*d)
	return math.Acos(math.Max(-1, math.Min(1, c))) / d
}

func (s *Service) RunAdoption(ctx context.Context, dryRun bool) (Report, error) {
	rep := Report{DryRun: dryRun}
	pl, err := s.candidates(ctx)
	if err != nil {
		return rep, err
	}
	if len(pl.projects) == 0 {
		slog.Warn("Target Scheduler returned no projects; adoption pass skipped")
		return rep, nil
	}
	cands, rig := pl.cands, pl.rig
	now := time.Now()
	err = s.App.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []app.MosaicAdoption
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Find(&existing).Error; err != nil {
			return err
		}
		bySubject := map[string]app.MosaicAdoption{}
		for _, e := range existing {
			bySubject[e.Subject] = e
		}
		for _, c := range cands {
			row := c.row
			old, seen := bySubject[row.Subject]
			action := ""
			switch {
			case seen && old.Fingerprint == row.Fingerprint:
				rep.Unchanged++
				kept, err := keep(tx, c, old, rig, now, dryRun)
				if err != nil {
					return err
				}
				row = kept
				if row.Status == app.AdoptionProposed && row.Clean {
					rep.Clean++
				}
			default:
				row.Status, row.DecidedBy, row.DecidedAt = app.AdoptionProposed, "", nil
				if seen {
					rep.Changed++
					row.ID, row.CreatedAt = old.ID, old.CreatedAt
					if old.Status == app.AdoptionAccepted || old.Status == app.AdoptionAuto {
						if err := withdraw(tx, old, &row, rig, now, dryRun); err != nil {
							return err
						}
					}
					if decidedByPerson(old) {
						row.DecidedBy, row.DecidedAt = old.DecidedBy, old.DecidedAt
					}
				} else {
					rep.New++
				}
				row.Clean = c.auto
				action = "review"
				if c.auto {
					rep.Clean++
				}
				if !dryRun {
					if err := tx.Save(&row).Error; err != nil {
						return err
					}
				}
			}
			decided, err := c.settle(tx, &row, rig, now, dryRun)
			if err != nil {
				return err
			}
			switch {
			case decided:
				action = "auto"
				rep.Auto++
			case row.Status == app.AdoptionProposed:
				rep.Review++
			}
			item := toAdoption(row)
			item.Action = action
			rep.Items = append(rep.Items, item)
		}
		live := map[string]bool{}
		for _, c := range cands {
			live[c.row.Subject] = true
		}
		for _, old := range existing {
			if live[old.Subject] || !moot(old, pl) {
				continue
			}
			row, ok, err := settleMoot(tx, old, pl, now, dryRun)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			rep.Auto++
			item := toAdoption(row)
			item.Action = "auto"
			rep.Items = append(rep.Items, item)
		}
		return nil
	})
	return rep, err
}

func moot(r app.MosaicAdoption, pl pool) bool {
	if r.Kind != KindFrames && !pl.projects[r.ProjectGUID] {
		return !autoByUs(r)
	}
	return r.Status == app.AdoptionProposed && r.DecidedAt == nil
}

func settleMoot(tx *gorm.DB, row app.MosaicAdoption, pl pool, now time.Time, dryRun bool) (app.MosaicAdoption, bool, error) {
	if row.Kind != KindFrames {
		switch {
		case !pl.projects[row.ProjectGUID]:
			row.Rule = "High: the project was removed from Target Scheduler, so nothing is left to adopt."
			row.Suggestion = "Nothing to do: the project was removed from Target Scheduler."
		case pl.planned[row.ProjectGUID]:
			row.Rule = "High: the project's mosaic was planned in Add target, so it needs no adoption."
			row.Suggestion = "Nothing to do: the mosaic was planned in Add target."
		default:
			return row, false, nil
		}
		return closeAuto(tx, row, now, dryRun)
	}
	var fp FramesProposal
	_ = json.Unmarshal([]byte(row.Proposal), &fp)
	var linked int64
	if err := tx.Model(&app.FrameTarget{}).Where("object = ? AND method <> ?", fp.Object, LinkReview).Count(&linked).Error; err != nil {
		return row, false, err
	}
	fp.TargetGUID, fp.Target, fp.ProjectGUID = "", "", ""
	if linked > 0 {
		fp.LinkedByTS = true
		row.Rule = fmt.Sprintf("High: Target Scheduler's own records now link %d subs named %q to a target, so nothing is left to decide.", linked, fp.Object)
		row.Suggestion = "Nothing to do: Target Scheduler's own records already link these subs to a target."
	} else {
		row.Rule = fmt.Sprintf("High: no unlinked subs named %q are left, so nothing is left to decide.", fp.Object)
		row.Suggestion = "Nothing to do: no unlinked subs of this name are left."
	}
	body, err := json.Marshal(fp)
	if err != nil {
		return row, false, err
	}
	row.Proposal = string(body)
	return closeAuto(tx, row, now, dryRun)
}

func closeAuto(tx *gorm.DB, row app.MosaicAdoption, now time.Time, dryRun bool) (app.MosaicAdoption, bool, error) {
	at := now
	row.Confidence = mosaics.ConfidenceHigh
	row.Status, row.DecidedBy, row.DecidedAt = app.AdoptionAuto, AutoDecider, &at
	if dryRun {
		return row, true, nil
	}
	return row, true, tx.Model(&row).Select("confidence", "rule", "suggestion", "proposal", "status", "decided_by", "decided_at", "updated_at").Updates(&row).Error
}

func keep(tx *gorm.DB, c candidate, old app.MosaicAdoption, rig mosaics.Rig, now time.Time, dryRun bool) (app.MosaicAdoption, error) {
	row := old
	byPerson := old.Status == app.AdoptionAccepted || old.Status == app.AdoptionRejected
	if !byPerson && reworded(&row, c.row) && !dryRun {
		if err := tx.Model(&row).Select("confidence", "rule", "issue", "suggestion", "proposal", "updated_at").Updates(&row).Error; err != nil {
			return row, err
		}
	}
	if autoByUs(old) && (!c.decidable() || (old.Kind == KindFrames && proposedTarget(old) != proposedTarget(row))) {
		if err := withdraw(tx, old, &row, rig, now, dryRun); err != nil {
			return row, err
		}
		if !dryRun {
			if err := tx.Model(&row).Select("status", "decided_by", "decided_at", "updated_at").Updates(&row).Error; err != nil {
				return row, err
			}
		}
	}
	if c.frames != nil && (row.Status == app.AdoptionAccepted || row.Status == app.AdoptionAuto) && !dryRun {
		if err := mosaicstore.LinkFrames(tx, c.frames.store(), now); err != nil {
			return row, err
		}
	}
	return row, nil
}

func decidedByPerson(r app.MosaicAdoption) bool {
	return r.DecidedAt != nil && r.DecidedBy != AutoDecider
}

func autoByUs(r app.MosaicAdoption) bool {
	return r.Status == app.AdoptionAuto && r.DecidedBy == AutoDecider
}

func proposedTarget(r app.MosaicAdoption) string {
	var fp FramesProposal
	if json.Unmarshal([]byte(r.Proposal), &fp) != nil {
		return ""
	}
	return fp.TargetGUID
}

func withdraw(tx *gorm.DB, old app.MosaicAdoption, row *app.MosaicAdoption, rig mosaics.Rig, now time.Time, dryRun bool) error {
	if !dryRun {
		if err := mosaicstore.Apply(tx, old, app.AdoptionProposed, rig, now); err != nil {
			return err
		}
	}
	row.Status, row.DecidedBy, row.DecidedAt = app.AdoptionProposed, "", nil
	return nil
}

func (c candidate) settle(tx *gorm.DB, row *app.MosaicAdoption, rig mosaics.Rig, now time.Time, dryRun bool) (bool, error) {
	if row.Status != app.AdoptionProposed || row.DecidedAt != nil || !c.decidable() {
		return false, nil
	}
	at := now
	row.Status, row.DecidedBy, row.DecidedAt = app.AdoptionAuto, AutoDecider, &at
	if dryRun {
		return true, nil
	}
	if err := mosaicstore.Apply(tx, *row, app.AdoptionAuto, rig, now); err != nil {
		return false, err
	}
	return true, tx.Model(row).Select("status", "decided_by", "decided_at", "updated_at").Updates(row).Error
}

func (c candidate) decidable() bool {
	if c.frames == nil {
		return c.row.Kind == mosaics.KindMosaic && c.auto
	}
	if c.frames.TargetGUID == "" {
		return true
	}
	return c.frames.NameMatch && c.frames.Within == 1 && c.frames.Separation != nil && *c.frames.Separation <= framesMaxSep
}

func reworded(row *app.MosaicAdoption, fresh app.MosaicAdoption) bool {
	same := row.Confidence == fresh.Confidence && row.Rule == fresh.Rule && row.Issue == fresh.Issue && row.Suggestion == fresh.Suggestion
	if row.Kind == KindFrames {
		same = same && row.Proposal == fresh.Proposal
	}
	if same {
		return false
	}
	row.Confidence, row.Rule, row.Issue, row.Suggestion = fresh.Confidence, fresh.Rule, fresh.Issue, fresh.Suggestion
	if row.Kind == KindFrames {
		row.Proposal = fresh.Proposal
	}
	return true
}

func toAdoption(r app.MosaicAdoption) Adoption {
	a := Adoption{ID: r.ID, Subject: r.Subject, ProjectGUID: r.ProjectGUID, Project: r.Project, Kind: r.Kind, Confidence: r.Confidence, Rule: r.Rule,
		Issue: r.Issue, Suggestion: r.Suggestion, Status: r.Status, Clean: r.Clean, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt}
	if !r.CreatedAt.IsZero() {
		found, worded := r.CreatedAt, r.UpdatedAt
		a.FoundAt, a.WordedAt = &found, &worded
	}
	if r.Kind == KindFrames {
		var fp FramesProposal
		if json.Unmarshal([]byte(r.Proposal), &fp) == nil {
			a.Frames = &fp
		}
	} else {
		_ = json.Unmarshal([]byte(r.Proposal), &a.Panels)
	}
	return a
}

func (s *Service) Adoptions(ctx context.Context) ([]Adoption, error) {
	var rows []app.MosaicAdoption
	if err := s.App.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	order := map[string]int{app.AdoptionProposed: 0, app.AdoptionAccepted: 1, app.AdoptionRejected: 1, app.AdoptionAuto: 1}
	kinds := map[string]int{mosaics.KindMosaic: 0, mosaics.KindNotMosaic: 1, KindFrames: 2}
	slices.SortFunc(rows, func(a, b app.MosaicAdoption) int {
		switch {
		case order[a.Status] != order[b.Status]:
			return order[a.Status] - order[b.Status]
		case a.Clean != b.Clean:
			if a.Clean {
				return 1
			}
			return -1
		case kinds[a.Kind] != kinds[b.Kind]:
			return kinds[a.Kind] - kinds[b.Kind]
		}
		return strings.Compare(a.Project, b.Project)
	})
	out := make([]Adoption, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAdoption(r))
	}
	return out, nil
}

func (s *Service) Decide(ctx context.Context, id int, decision, who string) (Adoption, error) {
	var status string
	switch decision {
	case "accept":
		status = app.AdoptionAccepted
	case "reject":
		status = app.AdoptionRejected
	default:
		return Adoption{}, ErrBadDecision
	}
	rig, _, err := s.rig(ctx)
	if err != nil {
		return Adoption{}, err
	}
	row, err := mosaicstore.SetStatus(ctx, s.App, id, "", status, who, rig)
	if errors.Is(err, mosaicstore.ErrNotFound) {
		return Adoption{}, ErrNotFound
	}
	if err != nil {
		return Adoption{}, err
	}
	return toAdoption(row), nil
}
