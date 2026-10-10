package mosaicstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KindFrames = "frames"
	LinkReview = "review"
)

var (
	ErrNotFound  = errors.New("adoption not found")
	ErrConflict  = errors.New("adoption decision changed since it was shown")
	ErrBadStatus = errors.New("status must be proposed, auto, accepted or rejected")
)

type FramesProposal struct {
	Object      string  `json:"object"`
	Count       int     `json:"count"`
	TargetGUID  string  `json:"targetGuid,omitempty"`
	Target      string  `json:"target,omitempty"`
	ProjectGUID string  `json:"projectGuid,omitempty"`
	Separation  float64 `json:"separationDeg"`
}

func ValidStatus(s string) bool {
	switch s {
	case app.AdoptionProposed, app.AdoptionAuto, app.AdoptionAccepted, app.AdoptionRejected:
		return true
	}
	return false
}

func adopts(status string) bool {
	return status == app.AdoptionAccepted || status == app.AdoptionAuto
}

func SetStatus(ctx context.Context, db *gorm.DB, id int, before, after, who string, rig mosaics.Rig) (app.MosaicAdoption, error) {
	if !ValidStatus(after) || (before != "" && !ValidStatus(before)) {
		return app.MosaicAdoption{}, ErrBadStatus
	}
	var row app.MosaicAdoption
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if before != "" && row.Status != before {
			return fmt.Errorf("%w: %s is %s, not %s", ErrConflict, row.Subject, row.Status, before)
		}
		now := time.Now()
		switch row.Kind {
		case KindFrames:
			var fp FramesProposal
			if err := json.Unmarshal([]byte(row.Proposal), &fp); err != nil {
				return err
			}
			if adopts(after) {
				if err := LinkFrames(tx, fp, now); err != nil {
					return err
				}
			} else if err := tx.Where("object = ? AND method = ?", fp.Object, LinkReview).Delete(&app.FrameTarget{}).Error; err != nil {
				return err
			}
		case mosaics.KindMosaic:
			var panels []mosaics.AdoptedPanel
			if err := json.Unmarshal([]byte(row.Proposal), &panels); err != nil {
				return err
			}
			if !adopts(after) {
				panels = nil
			}
			if err := WritePanels(tx, row.ProjectGUID, row.Project, panels, rig, now); err != nil {
				return err
			}
		default:
			if err := WritePanels(tx, row.ProjectGUID, row.Project, nil, rig, now); err != nil {
				return err
			}
		}
		row.Status, row.DecidedBy, row.DecidedAt = after, who, &now
		if after == app.AdoptionProposed {
			row.DecidedBy, row.DecidedAt = "", nil
		}
		return tx.Save(&row).Error
	})
	return row, err
}

func LinkFrames(tx *gorm.DB, fp FramesProposal, now time.Time) error {
	if fp.TargetGUID == "" {
		return nil
	}
	var ids []int
	if err := tx.Table("frames f").Select("f.id").
		Joins("LEFT JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("ft.id IS NULL AND f.type = ? AND f.object = ?", "LIGHT", fp.Object).Pluck("f.id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	links := make([]app.FrameTarget, 0, len(ids))
	for _, id := range ids {
		links = append(links, app.FrameTarget{FrameID: id, Object: fp.Object, TargetGUID: fp.TargetGUID, ProjectGUID: fp.ProjectGUID, Method: LinkReview, LinkedAt: now})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&links, 500).Error
}

func WritePanels(tx *gorm.DB, projectGUID, project string, panels []mosaics.AdoptedPanel, rig mosaics.Rig, now time.Time) error {
	if err := tx.Where("project_guid = ? AND source = ?", projectGUID, app.MosaicSourceAdopted).Delete(&app.MosaicPanel{}).Error; err != nil {
		return err
	}
	if len(panels) == 0 {
		return nil
	}
	rows := make([]app.MosaicPanel, 0, len(panels))
	for _, p := range panels {
		row, err := PanelRow(projectGUID, project, p, rig, app.MosaicSourceAdopted, now)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	return tx.Create(&rows).Error
}

func PanelRow(projectGUID, project string, p mosaics.AdoptedPanel, rig mosaics.Rig, source string, now time.Time) (app.MosaicPanel, error) {
	fp, err := json.Marshal(p.Footprint)
	if err != nil {
		return app.MosaicPanel{}, err
	}
	nb := p.Neighbours
	if nb == nil {
		nb = []string{}
	}
	nbs, err := json.Marshal(nb)
	if err != nil {
		return app.MosaicPanel{}, err
	}
	return app.MosaicPanel{ProjectGUID: projectGUID, TargetGUID: p.TargetGUID, Project: project, Target: p.Target, Panel: p.Panel,
		Row: p.Row, Col: p.Col, RA: p.Centre.RA, Dec: p.Centre.Dec, Rotation: p.RotationDeg, WidthDeg: rig.WidthDeg, HeightDeg: rig.HeightDeg,
		Footprint: string(fp), Neighbours: string(nbs), Source: source, CreatedAt: now, UpdatedAt: now}, nil
}
