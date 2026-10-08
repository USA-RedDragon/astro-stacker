package server

import (
	"context"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/publicframe"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// publicFrame is the stored public frame of object, or the newest of any
// object when it is empty. A frame whose light has left its master since
// (rescored, moon) is never served, even before the renderer replaces it.
func publicFrame(ctx context.Context, db *gorm.DB, object string) (app.PublicFrame, error) {
	q := db.WithContext(ctx).Table("public_frames pf").Select("pf.*").
		Joins("JOIN stack_frames sf ON sf.frame_id = pf.frame_id AND sf.status = ?", app.StackStatusAdded)
	if object != "" {
		q = q.Where("pf.object = ?", object)
	}
	var f app.PublicFrame
	err := q.Order("pf.date_obs DESC").Take(&f).Error
	return f, err
}

// publicSize reports whether a request's width and height, if given, are
// the size public frames are rendered at; they are never resized per
// request.
func publicSize(width, height string) bool {
	ok := func(v string, want int) bool {
		if v == "" {
			return true
		}
		n, err := strconv.Atoi(v)
		return err == nil && n == want
	}
	return ok(width, publicframe.DefaultOptions.Width) && ok(height, publicframe.DefaultOptions.Height)
}
