package mosaicplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
)

const (
	KindFrames    = "frames"
	LinkReview    = "review"
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
}

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
	Coords      bool     `json:"coords"`
	NameMatch   bool     `json:"nameMatch"`
	ProjectName string   `json:"projectNamed,omitempty"`
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

func (s *Service) candidates(ctx context.Context) ([]candidate, error) {
	targets, err := tslink.Targets(ctx, s.Sched)
	if err != nil {
		return nil, err
	}
	var wizard []string
	if err := s.App.WithContext(ctx).Model(&app.MosaicPanel{}).Where("source = ?", app.MosaicSourceWizard).
		Distinct("project_guid").Pluck("project_guid", &wizard).Error; err != nil {
		return nil, err
	}
	planned := map[string]bool{}
	for _, g := range wizard {
		planned[g] = true
	}
	var projects []mosaics.TSProject
	for _, p := range tslink.GroupProjects(targets) {
		if p.GUID == "" || planned[p.GUID] {
			continue
		}
		tp := mosaics.TSProject{GUID: p.GUID, Name: p.Name, IsMosaic: p.IsMosaic}
		for _, t := range p.Targets {
			tp.Targets = append(tp.Targets, mosaics.TSTarget{GUID: t.GUID, Name: t.Name, RA: t.RA, Dec: t.Dec, Rotation: t.Rotation, Active: t.Active})
		}
		projects = append(projects, tp)
	}
	rig, _, err := s.rig(ctx)
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, p := range mosaics.Propose(projects, rig) {
		body, err := json.Marshal(p.Panels)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate{
			row: app.MosaicAdoption{Subject: "project:" + p.ProjectGUID, ProjectGUID: p.ProjectGUID, Project: p.Project, Kind: p.Kind,
				Confidence: p.Confidence, Rule: p.Rule, Issue: p.Issue, Suggestion: p.Suggestion, Proposal: string(body), Fingerprint: p.Fingerprint},
			auto: p.Auto, panels: p.Panels,
		})
	}
	frames, err := s.frameCandidates(ctx, targets)
	if err != nil {
		return nil, err
	}
	return append(out, frames...), nil
}

func (s *Service) frameCandidates(ctx context.Context, targets []tslink.Target) ([]candidate, error) {
	var rows []struct {
		Object   string
		N        int
		MountRA  *float64
		MountDec *float64
	}
	if err := s.App.WithContext(ctx).Table("frames f").
		Select("f.object, count(*) AS n, avg(f.mount_ra) AS mount_ra, avg(f.mount_dec) AS mount_dec").
		Joins("LEFT JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("ft.id IS NULL AND f.type = ? AND f.index_error IS NULL AND f.object <> ''", "LIGHT").
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
	if err := s.App.WithContext(ctx).Model(&app.FrameTarget{}).Distinct("object").Pluck("object", &linked).Error; err != nil {
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
		if row.Confidence == mosaics.ConfidenceLow {
			fp.TargetGUID, fp.Target, fp.ProjectGUID = "", "", ""
		}
		row.Fingerprint = fingerprint(r.Object, fp.TargetGUID)
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
		if d := separation(ra, dec, t.RA, t.Dec); fp.Separation == nil || d < *fp.Separation {
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
	cands, err := s.candidates(ctx)
	if err != nil {
		return rep, err
	}
	var existing []app.MosaicAdoption
	if err := s.App.WithContext(ctx).Find(&existing).Error; err != nil {
		return rep, err
	}
	bySubject := map[string]app.MosaicAdoption{}
	for _, e := range existing {
		bySubject[e.Subject] = e
	}
	now := time.Now()
	err = s.App.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, c := range cands {
			row := c.row
			old, seen := bySubject[row.Subject]
			action := ""
			switch {
			case seen && old.Fingerprint == row.Fingerprint:
				rep.Unchanged++
				fresh := row
				row = old
				if reworded(&row, fresh) && !dryRun {
					if err := tx.Model(&row).Select("confidence", "rule", "issue", "suggestion", "proposal", "updated_at").Updates(&row).Error; err != nil {
						return err
					}
				}
				if row.Status == app.AdoptionProposed {
					rep.Review++
					if row.Clean {
						rep.Clean++
					}
				}
				if c.frames != nil && row.Status == app.AdoptionAccepted && !dryRun {
					if err := mosaicstore.LinkFrames(tx, c.frames.store(), now); err != nil {
						return err
					}
				}
			default:
				if seen {
					rep.Changed++
					row.ID, row.CreatedAt = old.ID, old.CreatedAt
				} else {
					rep.New++
				}
				row.Status, row.DecidedBy, row.DecidedAt = app.AdoptionProposed, "", nil
				row.Clean = c.auto
				rep.Review++
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
			item := toAdoption(row)
			item.Action = action
			rep.Items = append(rep.Items, item)
		}
		return nil
	})
	return rep, err
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
