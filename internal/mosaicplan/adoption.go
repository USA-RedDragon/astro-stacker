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
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
)

const (
	KindFrames   = "frames"
	LinkReview   = "review"
	framesMaxSep = 0.5
)

var (
	ErrNotFound    = errors.New("not found")
	ErrBadDecision = errors.New("decision must be accept or reject")
	ErrBadRequest  = errors.New("bad request")
)

type Service struct {
	App   *gorm.DB
	Sched *gorm.DB
	Rig   mosaics.Rig
	Site  SiteSource
}

func New(appDB, sched *gorm.DB) *Service {
	return &Service{App: appDB, Sched: sched, Rig: mosaics.DefaultRig()}
}

type Adoption struct {
	ID          int                    `json:"id"`
	Subject     string                 `json:"subject"`
	ProjectGUID string                 `json:"projectGuid,omitempty"`
	Project     string                 `json:"project"`
	Kind        string                 `json:"kind"`
	Confidence  string                 `json:"confidence"`
	Issue       string                 `json:"issue"`
	Suggestion  string                 `json:"suggestion"`
	Status      string                 `json:"status"`
	Clean       bool                   `json:"clean"`
	Action      string                 `json:"action,omitempty"`
	DecidedBy   string                 `json:"decidedBy,omitempty"`
	DecidedAt   *time.Time             `json:"decidedAt,omitempty"`
	Panels      []mosaics.AdoptedPanel `json:"panels,omitempty"`
	Frames      *FramesProposal        `json:"frames,omitempty"`
}

type FramesProposal = mosaicstore.FramesProposal

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
	var out []candidate
	for _, p := range mosaics.Propose(projects, s.Rig) {
		body, err := json.Marshal(p.Panels)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate{
			row: app.MosaicAdoption{Subject: "project:" + p.ProjectGUID, ProjectGUID: p.ProjectGUID, Project: p.Project, Kind: p.Kind,
				Confidence: p.Confidence, Issue: p.Issue, Suggestion: p.Suggestion, Proposal: string(body), Fingerprint: p.Fingerprint},
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
	for _, t := range targets {
		named[t.Name] = true
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
		fp := &FramesProposal{Object: r.Object, Count: r.N, Separation: math.Inf(1)}
		if r.MountRA != nil && r.MountDec != nil {
			for _, t := range targets {
				if t.GUID == "" {
					continue
				}
				if d := separation(*r.MountRA, *r.MountDec, t.RA, t.Dec); d < fp.Separation {
					fp.Separation, fp.TargetGUID, fp.Target, fp.ProjectGUID = d, t.GUID, t.Name, t.ProjectGUID
				}
			}
		}
		row := app.MosaicAdoption{Subject: "frames:" + r.Object, Project: r.Object, Kind: KindFrames}
		row.Issue = fmt.Sprintf("%d subs of %s have no Target Scheduler record.", r.N, r.Object)
		if r.N == 1 {
			row.Issue = fmt.Sprintf("1 sub of %s has no Target Scheduler record.", r.Object)
		}
		switch {
		case fp.TargetGUID != "" && fp.Separation <= framesMaxSep:
			row.Confidence = mosaics.ConfidenceMedium
			if fp.Separation <= 0.2 && sameName(r.Object, fp.Target) {
				row.Confidence = mosaics.ConfidenceHigh
			}
			row.Suggestion = fmt.Sprintf("Attach them to %s by object name and coordinates, %.2f° apart.", fp.Target, fp.Separation)
		default:
			fp.TargetGUID, fp.Target, fp.ProjectGUID = "", "", ""
			row.Confidence = mosaics.ConfidenceLow
			row.Suggestion = "No Target Scheduler target is within 0.5°. Keep them separate and count their hours on their own."
		}
		if math.IsInf(fp.Separation, 1) {
			fp.Separation = 0
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
				row = old
				if row.Status == app.AdoptionProposed {
					rep.Review++
					if row.Clean {
						rep.Clean++
					}
				}
				if c.frames != nil && row.Status == app.AdoptionAccepted && !dryRun {
					if err := mosaicstore.LinkFrames(tx, *c.frames, now); err != nil {
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

func toAdoption(r app.MosaicAdoption) Adoption {
	a := Adoption{ID: r.ID, Subject: r.Subject, ProjectGUID: r.ProjectGUID, Project: r.Project, Kind: r.Kind, Confidence: r.Confidence,
		Issue: r.Issue, Suggestion: r.Suggestion, Status: r.Status, Clean: r.Clean, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt}
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
	slices.SortFunc(rows, func(a, b app.MosaicAdoption) int {
		if order[a.Status] != order[b.Status] {
			return order[a.Status] - order[b.Status]
		}
		return strings.Compare(a.Subject, b.Subject)
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
	row, err := mosaicstore.SetStatus(ctx, s.App, id, "", status, who, s.Rig)
	if errors.Is(err, mosaicstore.ErrNotFound) {
		return Adoption{}, ErrNotFound
	}
	if err != nil {
		return Adoption{}, err
	}
	return toAdoption(row), nil
}
