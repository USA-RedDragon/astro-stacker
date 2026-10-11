package schedcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

const fieldMinimumAltitude = "minimumaltitude"

var ErrProjectNotFound = errors.New("project not found in the scheduler database")

// ProjectAltitudes reads a project's current minimum and maximum altitude.
type ProjectAltitudes interface {
	ProjectAltitudes(ctx context.Context, projectID int64) (minimum, maximum float64, err error)
}

func checkAltitudes(minimum, maximum float64) error {
	if maximum > 0 && maximum <= minimum {
		return fmt.Errorf("%w: maximum altitude must be above the minimum altitude", ErrInvalid)
	}
	return nil
}

func altitudeChanges(changes []FieldChange) (minimum, maximum *float64) {
	read := func(v json.RawMessage) *float64 {
		var f float64
		if isNull(v) || json.Unmarshal(v, &f) != nil {
			return nil
		}
		return &f
	}
	for _, c := range changes {
		switch c.Field {
		case fieldMinimumAltitude:
			minimum = read(c.After)
		case FieldMaximumAltitude:
			maximum = read(c.After)
		}
	}
	return minimum, maximum
}

func projectAltitudeCross(changes []FieldChange) error {
	minimum, maximum := altitudeChanges(changes)
	if minimum == nil || maximum == nil {
		return nil
	}
	return checkAltitudes(*minimum, *maximum)
}

func (s *Service) checkProjectAltitudes(ctx context.Context, kind Kind, payload json.RawMessage) error {
	if kind != KindProjectEdit && kind != KindProjectBatchEdit {
		return nil
	}
	var b TemplateBatchPayload
	if kind == KindProjectEdit {
		b.Items = make([]EditPayload, 1)
		if err := json.Unmarshal(payload, &b.Items[0]); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	} else if err := json.Unmarshal(payload, &b); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	items := b.Items
	if len(items) == 0 {
		return nil
	}
	src, ok := s.Snapshot.(ProjectAltitudes)
	if !ok {
		return nil
	}
	for _, it := range items {
		minimum, maximum := altitudeChanges(it.Changes)
		if (minimum == nil) == (maximum == nil) {
			continue
		}
		curMin, curMax, err := src.ProjectAltitudes(ctx, it.ID)
		if errors.Is(err, ErrProjectNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s's altitudes: %w", it.Name, err)
		}
		if minimum == nil {
			minimum = &curMin
		}
		if maximum == nil {
			maximum = &curMax
		}
		if err := checkAltitudes(*minimum, *maximum); err != nil {
			return fmt.Errorf("%s: %w", it.Name, err)
		}
	}
	return nil
}

// ProjectAltitudesFrom reads a project's altitudes from the scheduler database.
func ProjectAltitudesFrom(ctx context.Context, sched *gorm.DB, projectID int64) (minimum, maximum float64, err error) {
	var row struct {
		Minimum *float64 `gorm:"column:minimumaltitude"`
		Maximum *float64 `gorm:"column:maximumAltitude"`
	}
	res := sched.WithContext(ctx).Table("project").Select(`minimumaltitude, "maximumAltitude"`).Where(`"Id" = ?`, projectID).Limit(1).Scan(&row)
	if res.Error != nil {
		return 0, 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, 0, ErrProjectNotFound
	}
	if row.Minimum != nil {
		minimum = *row.Minimum
	}
	if row.Maximum != nil {
		maximum = *row.Maximum
	}
	return minimum, maximum, nil
}
