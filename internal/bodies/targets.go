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
	KindAsteroid  = "asteroid"
	// KindEvent targets follow a sky event's frame centre, which the event
	// finder publishes under a designation starting with EventPrefix; they
	// have no Horizons body.
	KindEvent   = "event"
	EventPrefix = "event:"
)

// IsEvent reports whether a designation or kind names an event centre.
func IsEvent(designation, kind string) bool {
	return kind == KindEvent || strings.HasPrefix(strings.ToLower(strings.TrimSpace(designation)), EventPrefix)
}

type Target struct {
	GUID        string `json:"target_guid"`
	Name        string `json:"name"`
	Designation string `json:"designation"`
	Kind        string `json:"kind"`
	Active      bool   `json:"active"`
	// RotationDeg is the position angle, degrees east of north, of the
	// frame's long side, pointing to its positive end; OffsetFrac is where
	// the body sits from the frame centre along that side, as a fraction of
	// the long side, positive toward the end at RotationDeg. Both are nil
	// when the target has no framing or the table has no framing columns.
	RotationDeg *float64 `json:"rotation_deg"`
	OffsetFrac  *float64 `json:"offset_frac"`
}

const (
	ColRotation = "rotation_deg"
	ColOffset   = "offset_frac"
)

// HasFraming reports whether the scheduler's ts_target_body has the framing
// columns.
func HasFraming(ctx context.Context, sched *gorm.DB) (bool, error) {
	for _, c := range []string{ColRotation, ColOffset} {
		ok, err := tables.HasColumn(ctx, sched, TargetTable, c)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
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
	framing, err := HasFraming(ctx, sched)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		GUID        string
		Designation string
		Kind        *string
		Name        *string
		Active      *bool
		RotationDeg *float64
		OffsetFrac  *float64
	}
	cols := `b.target_guid AS guid, b.designation AS designation, b.kind AS kind, t.name AS name, ` +
		`(t."Id" IS NOT NULL AND coalesce(p.state, 1) IN (0, 1) AND coalesce(t.active, 1) = 1) AS active`
	if framing {
		cols += `, b.rotation_deg AS rotation_deg, b.offset_frac AS offset_frac`
	}
	if err := sched.WithContext(ctx).Table(TargetTable + " b").
		Select(cols).
		Joins(`LEFT JOIN target t ON t.guid = b.target_guid`).
		Joins(`LEFT JOIN project p ON t.projectid = p."Id"`).
		Order("b.target_guid").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load %s: %w", TargetTable, err)
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		d := strings.TrimSpace(r.Designation)
		if r.GUID == "" || d == "" || IsEvent(d, deref(r.Kind)) {
			continue
		}
		t := Target{GUID: r.GUID, Designation: d, Kind: KindComet, Active: r.Active != nil && *r.Active}
		if r.RotationDeg != nil && finite(*r.RotationDeg) && r.OffsetFrac != nil && finite(*r.OffsetFrac) {
			t.RotationDeg, t.OffsetFrac = r.RotationDeg, r.OffsetFrac
		}
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

// TargetByGUID is the body target with the GUID, and whether there is one.
func TargetByGUID(ctx context.Context, sched *gorm.DB, guid string) (Target, bool, error) {
	targets, err := Targets(ctx, sched)
	if err != nil {
		return Target{}, false, err
	}
	for _, t := range targets {
		if strings.EqualFold(t.GUID, strings.TrimSpace(guid)) {
			return t, true, nil
		}
	}
	return Target{}, false, nil
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
