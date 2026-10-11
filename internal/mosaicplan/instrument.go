package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const scaleTolerance = 0.15

type instrument struct {
	Camera    string
	Telescope string
	Scale     float64
}

type instrumentRow struct {
	Key         string
	Camera      string
	Telescope   *string
	PixelSize   *float64
	FocalLength *float64
	N           int
}

func (r instrumentRow) instrument() instrument {
	in := instrument{Camera: strings.TrimSpace(r.Camera)}
	if r.Telescope != nil {
		in.Telescope = strings.TrimSpace(*r.Telescope)
	}
	if r.PixelSize != nil && r.FocalLength != nil && *r.PixelSize > 0 && *r.FocalLength > 0 {
		in.Scale = 206.265 * *r.PixelSize / *r.FocalLength
	}
	return in
}

func (i instrument) known() bool {
	return i.Camera != "" || i.Telescope != "" || i.Scale > 0
}

func (i instrument) String() string {
	switch {
	case i.Camera != "" && i.Telescope != "":
		return i.Camera + " on " + i.Telescope
	case i.Camera != "":
		return i.Camera
	case i.Telescope != "":
		return i.Telescope
	case i.Scale > 0:
		return fmt.Sprintf("a %.2f″/px rig", i.Scale)
	}
	return "an unknown instrument"
}

func sameKit(a, b string) bool {
	norm := func(s string) string {
		var sb strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				sb.WriteRune(r)
			}
		}
		return sb.String()
	}
	x, y := norm(a), norm(b)
	return x == "" || y == "" || strings.Contains(x, y) || strings.Contains(y, x)
}

func differentInstrument(subs, own instrument) bool {
	if !sameKit(subs.Camera, own.Camera) || !sameKit(subs.Telescope, own.Telescope) {
		return true
	}
	return subs.Scale > 0 && own.Scale > 0 && math.Abs(subs.Scale/own.Scale-1) > scaleTolerance
}

func majority(rows []instrumentRow) map[string]instrument {
	best := map[string]int{}
	out := map[string]instrument{}
	for _, r := range rows {
		in := r.instrument()
		if !in.known() {
			continue
		}
		if n, ok := best[r.Key]; !ok || r.N > n {
			best[r.Key], out[r.Key] = r.N, in
		}
	}
	return out
}

const instrumentCols = "f.camera AS camera, f.telescope AS telescope, f.pixel_size AS pixel_size, f.focal_length AS focal_length, count(*) AS n"

func subsInstruments(ctx context.Context, db *gorm.DB) (map[string]instrument, error) {
	var rows []instrumentRow
	if err := db.WithContext(ctx).Table("frames f").
		Select("f.object AS key, "+instrumentCols).
		Joins("LEFT JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("(ft.id IS NULL OR ft.method = ?) AND f.type = ? AND f.index_error IS NULL AND f.object <> ''", LinkReview, "LIGHT").
		Group("f.object, f.camera, f.telescope, f.pixel_size, f.focal_length").Order("f.object, n DESC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load sub instruments: %w", err)
	}
	return majority(rows), nil
}

func targetInstruments(ctx context.Context, db *gorm.DB) (map[string]instrument, error) {
	var rows []instrumentRow
	if err := db.WithContext(ctx).Table("frames f").
		Select("ft.target_guid AS key, "+instrumentCols).
		Joins("JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("ft.method IN ? AND f.type = ?", []string{app.LinkHeader, app.LinkAcquiredImage}, "LIGHT").
		Group("ft.target_guid, f.camera, f.telescope, f.pixel_size, f.focal_length").Order("ft.target_guid, n DESC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load target instruments: %w", err)
	}
	return majority(rows), nil
}

func rigInstrument(info rigsource.Rig) instrument {
	var in instrument
	if info.Basis.Camera != nil {
		in.Camera = *info.Basis.Camera
	}
	if info.Basis.Telescope != nil {
		in.Telescope = *info.Basis.Telescope
	}
	if info.Scale != nil {
		in.Scale = *info.Scale
	}
	return in
}
