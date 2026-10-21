package stacking

import (
	"context"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// photometryPad is how far either side of a dip watch's or eclipse
// request's window a light below the stacking cut is still registered for
// photometry, for the star's level around the window.
const photometryPad = 2 * time.Hour

// photometryWatch says which lights below the stacking cut are calibrated
// and registered all the same, for measuring stars rather than stacking: a
// dip watch or eclipse request measures every usable sub of its window, and
// the cut is for pictures. The windows are read once, on first use.
type photometryWatch struct {
	db      *gorm.DB
	ids     []int
	loaded  bool
	windows map[string][][2]time.Time
	done    map[int]bool
}

func (p *Pipeline) photometryWatch(frames []app.Frame) *photometryWatch {
	ids := make([]int, len(frames))
	for i, f := range frames {
		ids[i] = f.ID
	}
	return &photometryWatch{db: p.db, ids: ids}
}

// wants reports whether f, below the cut, is to be registered for
// photometry: it has what calibration needs, falls in a window of its
// target, and has no registered copy yet.
func (w *photometryWatch) wants(ctx context.Context, f app.Frame) bool {
	if f.Exposure == nil || *f.Exposure <= 0 || f.Night == nil || f.DateObs == nil {
		return false
	}
	if !w.loaded {
		w.loaded = true
		var err error
		if w.windows, w.done, err = loadPhotometryWindows(ctx, w.db, w.ids); err != nil {
			slog.Warn("Could not read the dip watches and eclipse requests; lights below the cut are not registered", "error", err)
		}
	}
	if w.done[f.ID] {
		return false
	}
	for _, win := range w.windows[f.Object] {
		if !f.DateObs.Before(win[0].Add(-photometryPad)) && !f.DateObs.After(win[1].Add(photometryPad)) {
			return true
		}
	}
	return false
}

func loadPhotometryWindows(ctx context.Context, db *gorm.DB, ids []int) (map[string][][2]time.Time, map[int]bool, error) {
	var rows []struct {
		Object     string
		Start, End time.Time
	}
	for _, table := range []any{&app.DipWatch{}, &app.EclipseRequest{}} {
		var part []struct {
			Object     string
			Start, End time.Time
		}
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(table); err != nil {
			return nil, nil, err
		}
		if err := db.WithContext(ctx).Table(stmt.Schema.Table + " w").
			Select("ds.object, w.start, w.\"end\"").
			Joins("JOIN discovery_sources ds ON ds.id = w.source_id").
			Scan(&part).Error; err != nil {
			return nil, nil, err
		}
		rows = append(rows, part...)
	}
	windows := map[string][][2]time.Time{}
	for _, r := range rows {
		windows[r.Object] = append(windows[r.Object], [2]time.Time{r.Start, r.End})
	}
	var done []int
	if err := db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("frame_id IN ? AND status = ? AND registered_key IS NOT NULL", ids, app.StackStatusLowScore).
		Pluck("frame_id", &done).Error; err != nil {
		return nil, nil, err
	}
	seen := make(map[int]bool, len(done))
	for _, id := range done {
		seen[id] = true
	}
	return windows, seen, nil
}

// keepPhotometry keeps the registered copy of a light below the cut when it
// is judged below the cut again.
func keepPhotometry(sf *app.StackFrame, existing app.StackFrame) {
	if sf.Status != app.StackStatusLowScore || existing.Status != app.StackStatusLowScore ||
		sf.RegisteredKey != nil || existing.RegisteredKey == nil {
		return
	}
	sf.StackID, sf.RegisteredKey, sf.Weight = existing.StackID, existing.RegisteredKey, existing.Weight
	sf.DarkScale, sf.NoDark = existing.DarkScale, existing.NoDark
	sf.BiasMaster, sf.DarkMaster, sf.FlatMaster, sf.FlatMatch = existing.BiasMaster, existing.DarkMaster, existing.FlatMaster, existing.FlatMatch
}

// recordPhotometry records the lights registered for photometry only:
// below the cut, out of the master, with the registered copy measurements
// read. It returns the others, which go on the master.
func (p *Pipeline) recordPhotometry(ctx context.Context, stack *app.Stack, subs []addedSub) ([]addedSub, error) {
	var stacked []addedSub
	for _, a := range subs {
		if !a.c.c.photometryOnly {
			stacked = append(stacked, a)
			continue
		}
		sf := p.leftOut(a.c.c, app.StackStatusLowScore)
		exp := *a.c.c.frame.Exposure
		key := a.key
		sf.StackID, sf.RegisteredKey, sf.Weight = &stack.ID, &key, a.c.c.score.Score*exp
		sf.DarkScale, sf.NoDark = a.c.darkScale, a.c.c.cal.Dark.Set == nil && !precalibrated(a.c.c.frame)
		sf.BiasMaster, sf.DarkMaster, sf.FlatMaster, sf.FlatMatch = optional(a.c.bias), optional(a.c.dark), optional(a.c.flat), a.c.flatMatch()
		if err := p.record(ctx, sf); err != nil {
			return nil, err
		}
	}
	return stacked, nil
}
