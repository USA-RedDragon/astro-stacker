package goals

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

type Setting struct {
	Filter      string
	Kind        int
	SNRGoal     *float64
	DepthGoal   *float64
	PlateauStop *int
	Region      *string
}

func (s Setting) IsDepth() bool { return s.Kind == kindDepth }

type Settings map[string]map[string]Setting

func (s Settings) For(guid, filter string) (Setting, bool) {
	g, ok := s[guid][stackKey(filter)]
	return g, ok
}

func FilterKey(filter string) string { return stackKey(filter) }

func stackKey(filter string) string {
	return strings.ToUpper(strings.TrimSpace(filter))
}

func LoadSettings(ctx context.Context, sched *gorm.DB) (Settings, error) {
	rows, has, err := loadGoalRows(ctx, sched)
	if err != nil || !has {
		return nil, err
	}
	out := Settings{}
	for _, r := range rows {
		if out[r.TargetGUID] == nil {
			out[r.TargetGUID] = map[string]Setting{}
		}
		out[r.TargetGUID][stackKey(r.Filter)] = Setting{Filter: r.Filter, Kind: r.Kind, SNRGoal: r.SNRGoal, DepthGoal: r.DepthGoal, PlateauStop: r.PlateauStop, Region: r.Region}
	}
	return out, nil
}
