// Package bodies builds the nightly position tables of moving targets
// (comets) from JPL Horizons, publishes them to the plugin through
// ts_body_position, and lists the comets observable tonight.
package bodies

import (
	"context"
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	TargetTable   = "ts_target_body"
	PositionTable = "ts_body_position"
	KindComet     = "comet"
)

type Target struct {
	GUID        string `json:"target_guid"`
	Name        string `json:"name"`
	Designation string `json:"designation"`
	Kind        string `json:"kind"`
	Active      bool   `json:"active"`
}

// Targets lists the scheduler's targets that have a body designation. A
// scheduler without the ts_target_body table has none.
func Targets(ctx context.Context, sched *gorm.DB) ([]Target, error) {
	if sched == nil {
		return nil, nil
	}
	has, err := tables.Has(ctx, sched, TargetTable)
	if err != nil || !has {
		return nil, err
	}
	var rows []struct {
		GUID        string
		Designation string
		Kind        *string
		Name        *string
		Active      *bool
	}
	if err := sched.WithContext(ctx).Table(TargetTable + " b").
		Select(`b.target_guid AS guid, b.designation AS designation, b.kind AS kind, t.name AS name, ` +
			`(t."Id" IS NOT NULL AND coalesce(p.state, 1) IN (0, 1) AND coalesce(t.active, 1) = 1) AS active`).
		Joins(`LEFT JOIN target t ON t.guid = b.target_guid`).
		Joins(`LEFT JOIN project p ON t.projectid = p."Id"`).
		Order("b.target_guid").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", TargetTable, err)
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		d := strings.TrimSpace(r.Designation)
		if r.GUID == "" || d == "" {
			continue
		}
		t := Target{GUID: r.GUID, Designation: d, Kind: KindComet, Active: r.Active != nil && *r.Active}
		if r.Kind != nil && strings.TrimSpace(*r.Kind) != "" {
			t.Kind = strings.TrimSpace(*r.Kind)
		}
		if r.Name != nil {
			t.Name = *r.Name
		}
		out = append(out, t)
	}
	return out, nil
}

// DesignationsByName maps target names to their stored designations, for
// the stacker, which knows targets by name.
func DesignationsByName(ctx context.Context, sched *gorm.DB) (map[string]string, error) {
	targets, err := Targets(ctx, sched)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, t := range targets {
		if t.Name == "" {
			continue
		}
		if _, ok := out[t.Name]; !ok {
			out[t.Name] = t.Designation
		}
	}
	return out, nil
}
