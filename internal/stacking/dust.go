package stacking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// Dust check timing.
const (
	// DustInterval is how often settled flat sets and finished nights of
	// lights are looked for.
	DustInterval = 15 * time.Minute
	// DustLightsQuiet is how long a night's lights must go without a new
	// calibrated sub before they are checked.
	DustLightsQuiet = time.Hour
	// DustLightsKeep is how long calibrated sub copies are kept, and how
	// old a night may be for its lights to be kept and checked.
	DustLightsKeep = 14 * 24 * time.Hour
	// DustLightsRecent is how recent a light's night must be for its
	// calibrated copy to be kept, so calibrating old lights again doesn't
	// check old nights again.
	DustLightsRecent = 3 * 24 * time.Hour
	// DustLightsMax is the most subs one night's check reads.
	DustLightsMax = 150
	// DustAlertWindow is how long a change counts in the gauges after its
	// flat or night.
	DustAlertWindow = 36 * time.Hour
	// DustRetry is the wait before a flat set that could not be checked,
	// such as one whose master fails to build, is tried again.
	DustRetry = 6 * time.Hour
	// DustKeepBuilt is how old a flat set's night must be for the work
	// directory's copy of a master the dust check built for it to be
	// removed after: old sets' lights are stacked already.
	DustKeepBuilt = 7 * 24 * time.Hour
	dustCropHalf  = 700
	dustCacheSize = 16
)

const contentTypePNG = "image/png"

// dustState is the dust check's cache of decoded flat maps, by DustFlat ID.
type dustState struct {
	mu    sync.Mutex
	maps  map[int]dustcheck.Map
	order []int
	// failed holds when each set that could not be checked last failed,
	// so it is logged once and tried again only after DustRetry.
	failed map[string]time.Time
}

func (d *dustState) get(id int) (dustcheck.Map, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.maps[id]
	return m, ok
}

func (d *dustState) put(id int, m dustcheck.Map) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.maps == nil {
		d.maps = map[int]dustcheck.Map{}
	}
	if _, ok := d.maps[id]; !ok {
		d.order = append(d.order, id)
	}
	d.maps[id] = m
	for len(d.order) > dustCacheSize {
		delete(d.maps, d.order[0])
		d.order = d.order[1:]
	}
}

// fail records a set's failure and reports whether it is its first.
func (d *dustState) fail(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed == nil {
		d.failed = map[string]time.Time{}
	}
	_, seen := d.failed[key]
	d.failed[key] = now
	return !seen
}

// waiting reports whether a set failed less than DustRetry ago.
func (d *dustState) waiting(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.failed[key]
	return ok && now.Sub(at) < DustRetry
}

// runDust checks settled flat sets and finished nights of lights for dust
// changes every DustInterval.
func (p *Pipeline) runDust(ctx context.Context) {
	for !p.stopping(ctx) {
		err := p.CheckFlats(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("Flat dust check failed", "error", err)
		}
		lerr := p.checkLightNights(ctx, time.Now())
		if lerr != nil && ctx.Err() == nil {
			slog.Error("Lights dust check failed", "error", lerr)
		}
		if p.DustTask.Running() {
			if ctx.Err() != nil {
				err, lerr = nil, nil
			}
			p.DustTask.Finish(errors.Join(err, lerr))
		}
		p.DustTask.SetNext(time.Now().Add(DustInterval))
		p.dustMetrics(ctx, time.Now())
		if !p.pause(ctx, DustInterval) {
			return
		}
	}
}

// dustGroupKey identifies a flat set among the calibration sets.
func dustGroupKey(s calmatch.Set) string {
	num := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return strings.Join([]string{s.Night.Format(time.DateOnly), s.Object, s.Filter, num(s.Rotator),
		num(s.Exposure), num(s.Gain), num(s.Offset), num(s.SetTemp), num(s.BinX)}, "|")
}

type pendingFlat struct {
	set     calmatch.Set
	frames  []app.Frame
	takenAt time.Time
	rotator float64
	row     *app.DustFlat
}

// CheckFlats checks every settled flat set not checked yet, oldest first:
// it builds the set's master if there is none, maps its dust, compares it
// with the flat before it of its filter and records what changed.
func (p *Pipeline) CheckFlats(ctx context.Context) error {
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return err
	}
	var done []app.DustFlat
	if err := p.db.WithContext(ctx).Find(&done).Error; err != nil {
		return fmt.Errorf("load dust flats: %w", err)
	}
	have := make(map[string]*app.DustFlat, len(done))
	for i := range done {
		have[done[i].GroupKey] = &done[i]
	}
	now := time.Now()
	var todo []pendingFlat
	for _, s := range sets {
		if s.Type != frameTypeFlat || s.Master != "" || s.Count < calmatch.MinFrames || !p.settled(s, now) {
			continue
		}
		row := have[dustGroupKey(s)]
		if row != nil && row.Frames == s.Count && row.Version == dustcheck.Version || p.dust.waiting(dustGroupKey(s), now) {
			continue
		}
		frames, err := coverage.SetFrames(ctx, p.db, s)
		if err != nil {
			return err
		}
		if len(frames) < calmatch.MinFrames {
			continue
		}
		todo = append(todo, pendingFlat{set: s, frames: frames, takenAt: firstTaken(frames, s.Night),
			rotator: medianRotator(frames), row: row})
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].takenAt.Before(todo[j].takenAt) })
	nights := map[time.Time]bool{}
	if len(todo) > 0 {
		p.DustTask.Start("checking new flats for dust", int64(len(todo)), "flat sets")
	}
	for i, f := range todo {
		p.DustTask.SetProgress(int64(i), int64(len(todo)))
		p.DustTask.SetCurrent(fmt.Sprintf("checking %s flats of %s for dust", f.set.Filter, f.set.Night.Format("2 Jan 2006")))
		if p.stopping(ctx) {
			return ctx.Err()
		}
		if err := p.checkFlat(ctx, f, sets); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p.dust.fail(dustGroupKey(f.set), time.Now()) {
				slog.Warn("Could not check a flat set for dust", "night", f.set.Night.Format(time.DateOnly),
					"filter", f.set.Filter, "object", f.set.Object, "error", err)
			}
			continue
		}
		nights[f.set.Night] = true
	}
	var changed []time.Time
	if err := p.db.WithContext(ctx).Model(&app.DustEvent{}).Where("class IN ? AND night IS NOT NULL",
		[]string{dustcheck.ClassNew, dustcheck.ClassGone, dustcheck.ClassMoved}).Distinct("night").Pluck("night", &changed).Error; err != nil {
		return err
	}
	regroup := map[string]time.Time{}
	for _, n := range changed {
		regroup[n.Format(time.DateOnly)] = n
	}
	propose := map[string]bool{}
	for n := range nights {
		regroup[n.Format(time.DateOnly)] = n
		propose[n.Format(time.DateOnly)] = true
	}
	for day, n := range regroup {
		if err := p.groupDustNight(ctx, n, propose[day]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("Could not group a night's flat dust changes", "night", day, "error", err)
		}
	}
	return nil
}

func firstTaken(frames []app.Frame, night time.Time) time.Time {
	var first time.Time
	for _, f := range frames {
		if f.DateObs != nil && (first.IsZero() || f.DateObs.Before(first)) {
			first = *f.DateObs
		}
	}
	if first.IsZero() {
		return night
	}
	return first
}

func medianRotator(frames []app.Frame) float64 {
	var v []float64
	for _, f := range frames {
		if f.Rotator != nil {
			v = append(v, *f.Rotator)
		}
	}
	if len(v) == 0 {
		return math.NaN()
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// masterPixels reads a set's master. A master already built is read from
// the work directory's copy, or else from a download that is removed after,
// so checking the whole flat history leaves no copies behind; one not built
// yet is built by masterFor and kept, as for lights.
func (p *Pipeline) masterPixels(ctx context.Context, set calmatch.Set, all []calmatch.Set) ([]float32, int, int, string, error) {
	file, key, built, err := p.dustMasterFile(ctx, set, all)
	if err != nil {
		return nil, 0, 0, "", err
	}
	if built && time.Since(set.Night) > DustKeepBuilt {
		defer p.dropLocalMaster(key)
	}
	if file == "" {
		var cm app.CalibrationMaster
		if err := p.db.WithContext(ctx).Where("set_key = ?", key).First(&cm).Error; err != nil {
			return nil, 0, 0, "", err
		}
		dir, err := os.MkdirTemp(p.workDir, "dust-")
		if err != nil {
			return nil, 0, 0, "", err
		}
		defer os.RemoveAll(dir)
		file = filepath.Join(dir, "master.fit")
		if err := p.download(ctx, p.dest, cm.ObjectKey, file); err != nil {
			return nil, 0, 0, "", err
		}
	}
	data, w, h, err := readSub(file)
	return data, w, h, key, err
}

// dropLocalMaster removes the work directory's copy of a master the dust
// check built for an old set; masterFor downloads it again if lights need it.
func (p *Pipeline) dropLocalMaster(key string) {
	defer p.lockKey(key)()
	if err := os.Remove(filepath.Join(p.workDir, "masters", key+".fit")); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Could not remove a master copy the dust check built", "key", key, "error", err)
	}
}

// dustMasterFile is the local master of a set and its key, or "" and the
// key when the master is built but has no local copy; built says the
// master was built just now.
func (p *Pipeline) dustMasterFile(ctx context.Context, set calmatch.Set, all []calmatch.Set) (string, string, bool, error) {
	if p.dustMasterFor != nil {
		file, key, err := p.dustMasterFor(ctx, set, all)
		return file, key, false, err
	}
	if set.Master != "" {
		file, key, err := p.masterFor(ctx, set, all)
		return file, key, false, err
	}
	frames, err := coverage.SetFrames(ctx, p.db, set)
	if err != nil {
		return "", "", false, err
	}
	key := setKey(set.Type, frames)
	local := filepath.Join(p.workDir, "masters", key+".fit")
	if _, err := os.Stat(local); err == nil {
		return local, key, false, nil
	}
	var n int64
	if err := p.db.WithContext(ctx).Model(&app.CalibrationMaster{}).Where("set_key = ?", key).Count(&n).Error; err != nil {
		return "", "", false, err
	}
	if n > 0 {
		return "", key, false, nil
	}
	file, key, err := p.masterFor(ctx, set, all)
	return file, key, err == nil, err
}

func dustMapKey(setKey string) string { return path.Join("dust", "flat", setKey+".map.zst") }

func (p *Pipeline) checkFlat(ctx context.Context, f pendingFlat, sets []calmatch.Set) error {
	data, w, h, setKey, err := p.masterPixels(ctx, f.set, sets)
	if err != nil {
		return err
	}
	raw := dustcheck.DustMap(data, w, h)
	enc, err := dustcheck.Encode(raw)
	if err != nil {
		return err
	}
	if err := p.putBytes(ctx, dustMapKey(setKey), enc, minio.PutObjectOptions{ContentType: contentTypeOctetStream}); err != nil {
		return err
	}
	s := dustcheck.Prepare(raw)
	catalog, err := json.Marshal(dustcheck.Catalog(s))
	if err != nil {
		return err
	}
	night := f.set.Night
	row := app.DustFlat{SetKey: setKey, Night: &night, Object: f.set.Object, Filter: f.set.Filter, TakenAt: f.takenAt,
		Frames: len(f.frames), GroupKey: dustGroupKey(f.set), MapKey: dustMapKey(setKey), Sigma: dustcheck.Sigma(s),
		Catalog: string(catalog), Width: raw.W, Height: raw.H, CheckedAt: time.Now()}
	if !math.IsNaN(f.rotator) {
		r := f.rotator
		row.Rotator = &r
	}
	var old app.DustFlat
	switch err := p.db.WithContext(ctx).Where("set_key = ? OR group_key = ?", setKey, row.GroupKey).First(&old).Error; {
	case err == nil:
		row.ID = old.ID
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}
	if err := p.db.WithContext(ctx).Save(&row).Error; err != nil {
		return fmt.Errorf("record dust flat: %w", err)
	}
	p.dust.put(row.ID, raw)
	return p.compareFlat(ctx, row, raw, s)
}

func flatOf(r app.DustFlat) dustcheck.Flat {
	rot := math.NaN()
	if r.Rotator != nil {
		rot = *r.Rotator
	}
	return dustcheck.Flat{ID: r.ID, Filter: r.Filter, Rotator: rot, TakenAt: r.TakenAt}
}

// dustMap returns a checked flat's raw dust map, from the cache or the
// bucket.
func (p *Pipeline) dustMap(ctx context.Context, r app.DustFlat) (dustcheck.Map, error) {
	if m, ok := p.dust.get(r.ID); ok {
		return m, nil
	}
	b, err := p.getBytes(ctx, r.MapKey)
	if err != nil {
		return dustcheck.Map{}, err
	}
	m, err := dustcheck.Decode(b)
	if err != nil {
		return dustcheck.Map{}, err
	}
	p.dust.put(r.ID, m)
	return m, nil
}

func (p *Pipeline) getBytes(ctx context.Context, key string) ([]byte, error) {
	obj, err := p.s3.GetObject(ctx, p.dest, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return b, nil
}

// compareFlat compares a checked flat with the one before it of its filter
// and records the differences in place of any recorded before.
func (p *Pipeline) compareFlat(ctx context.Context, row app.DustFlat, curRaw dustcheck.Map, s dustcheck.Smoothed) error {
	var hist []app.DustFlat
	if err := p.db.WithContext(ctx).Where("filter = ? AND taken_at < ? AND id <> ? AND width = ? AND height = ?",
		row.Filter, row.TakenAt, row.ID, row.Width, row.Height).
		Order("taken_at").Find(&hist).Error; err != nil {
		return err
	}
	flats := make([]dustcheck.Flat, len(hist))
	for i, h := range hist {
		flats[i] = flatOf(h)
	}
	cur := flatOf(row)
	cur.Map = s
	prevFlat, ok := dustcheck.Previous(cur, flats)
	if err := p.db.WithContext(ctx).Where("flat_id = ?", row.ID).Delete(&app.DustEvent{}).Error; err != nil {
		return err
	}
	if !ok {
		return p.db.WithContext(ctx).Model(&row).Updates(map[string]any{"prev_id": nil, "rot_match": false, "diff_sigma": 0,
			"version": dustcheck.Version}).Error
	}
	var prevRow app.DustFlat
	for _, h := range hist {
		if h.ID == prevFlat.ID {
			prevRow = h
		}
	}
	prevRaw, err := p.dustMap(ctx, prevRow)
	if err != nil {
		return err
	}
	prevFlat.Map = dustcheck.Prepare(prevRaw)
	if prevRaw.W != curRaw.W || prevRaw.H != curRaw.H {
		return fmt.Errorf("previous dust map is %dx%d, this one %dx%d", prevRaw.W, prevRaw.H, curRaw.W, curRaw.H)
	}
	evs := dustcheck.Compare(prevFlat, cur)
	now := time.Now()
	diffSigma := dustcheck.Sigma(dustcheck.Diff(prevFlat.Map, s))
	for i, e := range evs {
		ev := app.DustEvent{FlatID: row.ID, PrevID: prevRow.ID, Filter: row.Filter, TakenAt: row.TakenAt, Night: row.Night,
			Class: e.Class, X: e.X, Y: e.Y, Diameter: e.Diameter, Depth: e.Peak, Edge: e.Edge, Sharp: e.Sharp,
			PartnerX: e.PartnerX, PartnerY: e.PartnerY, RotMatch: e.RotMatch, DiffSigma: e.DiffSigma, DetectedAt: now}
		if dustcheck.Change(e.Class) {
			png, err := dustcheck.Crop(prevRaw, curRaw, e.X, e.Y, dustCropHalf)
			if err == nil {
				key := path.Join("dust", "crops", fmt.Sprintf("%s-%d.png", row.SetKey, i))
				if err = p.putBytes(ctx, key, png, minio.PutObjectOptions{ContentType: contentTypePNG}); err == nil {
					ev.CropKey = &key
				}
			}
			if err != nil {
				slog.Warn("Could not render a dust change crop", "filter", row.Filter, "error", err)
			}
		}
		if err := p.db.WithContext(ctx).Create(&ev).Error; err != nil {
			return fmt.Errorf("record dust event: %w", err)
		}
		if dustcheck.Change(e.Class) {
			slog.Info("Flat dust changed", "filter", row.Filter, "class", e.Class, "x", e.X, "y", e.Y,
				"diameter", e.Diameter, "depth", fmt.Sprintf("%+.2f%%", e.Peak), "since", prevRow.TakenAt.Format(time.DateOnly))
		}
	}
	return p.db.WithContext(ctx).Model(&row).Updates(map[string]any{
		"prev_id": prevRow.ID, "rot_match": dustcheck.AngleDiff(prevFlat.Rotator, cur.Rotator) <= dustcheck.RotationTolerance,
		"diff_sigma": diffSigma, "version": dustcheck.Version,
	}).Error
}

// groupDustNight works out, across filters, which of a night's flat
// changes are on the window and which on a filter; with propose, its window
// changes also propose dust epochs (proposeDustEpoch).
func (p *Pipeline) groupDustNight(ctx context.Context, night time.Time, propose bool) error {
	var flats []app.DustFlat
	if err := p.db.WithContext(ctx).Where("night = ?", night).Find(&flats).Error; err != nil {
		return err
	}
	checked := map[string]bool{}
	last := time.Time{}
	for _, f := range flats {
		if f.PrevID != nil {
			checked[f.Filter] = true
		}
		if f.TakenAt.After(last) {
			last = f.TakenAt
		}
	}
	var evs []app.DustEvent
	if err := p.db.WithContext(ctx).Where("night = ? AND class IN ?", night,
		[]string{dustcheck.ClassNew, dustcheck.ClassGone, dustcheck.ClassMoved}).Order("id").Find(&evs).Error; err != nil {
		return err
	}
	if len(evs) == 0 {
		return nil
	}
	others, err := p.latestCatalogs(ctx, last)
	if err != nil {
		return err
	}
	located := make([]dustcheck.Located, len(evs))
	byID := map[int]int{}
	for i, e := range evs {
		located[i] = dustcheck.Located{Filter: e.Filter, Event: dustcheck.Event{
			Blob:  dustcheck.Blob{X: e.X, Y: e.Y, Diameter: e.Diameter, Peak: e.Depth},
			Class: e.Class, PartnerX: e.PartnerX, PartnerY: e.PartnerY,
		}}
		byID[i] = e.ID
	}
	groups := dustcheck.GroupChanges(located, len(checked), others)
	for _, g := range groups {
		first := g.Members[0]
		gk := fmt.Sprintf("%s:%d:%d", night.Format(time.DateOnly), first.X, first.Y)
		filters := strings.Join(g.Filters(), ",")
		var members []app.DustEvent
		for _, m := range g.Members {
			for i, l := range located {
				if l.Filter != m.Filter || l.X != m.X || l.Y != m.Y {
					continue
				}
				members = append(members, evs[i])
				ids := []int{byID[i]}
				if l.Class == dustcheck.ClassMoved {
					for j, o := range located {
						if o.Filter == l.Filter && o.X == l.PartnerX && o.Y == l.PartnerY {
							ids = append(ids, byID[j])
						}
					}
				}
				if err := p.db.WithContext(ctx).Model(&app.DustEvent{}).Where("id IN ?", ids).
					Updates(map[string]any{"scope": g.Scope, "filters": filters, "group_key": gk}).Error; err != nil {
					return err
				}
			}
		}
		if propose && g.Scope == dustcheck.ScopeWindow {
			if err := p.proposeDustEpoch(ctx, night, g, members); err != nil {
				return err
			}
		}
	}
	return nil
}

// latestCatalogs is each filter's newest dust catalog taken by at.
func (p *Pipeline) latestCatalogs(ctx context.Context, at time.Time) (map[string][]dustcheck.Blob, error) {
	var flats []app.DustFlat
	if err := p.db.WithContext(ctx).Where("taken_at <= ?", at).Order("taken_at").Find(&flats).Error; err != nil {
		return nil, err
	}
	out := map[string][]dustcheck.Blob{}
	for _, f := range flats {
		var cat []dustcheck.Blob
		if f.Catalog != "" {
			if err := json.Unmarshal([]byte(f.Catalog), &cat); err != nil {
				return nil, fmt.Errorf("dust catalog of flat %d: %w", f.ID, err)
			}
		}
		out[f.Filter] = cat
	}
	return out, nil
}

// dustMetrics sets the dust gauges: the flat changes of the last
// DustAlertWindow, and the latest checked night's lights within it.
func (p *Pipeline) dustMetrics(ctx context.Context, now time.Time) {
	var evs []app.DustEvent
	if err := p.db.WithContext(ctx).Where("taken_at >= ? AND class IN ?", now.Add(-DustAlertWindow),
		[]string{dustcheck.ClassNew, dustcheck.ClassGone, dustcheck.ClassMoved}).Find(&evs).Error; err != nil {
		return
	}
	metrics.FlatDustEvents.Reset()
	for _, e := range evs {
		if e.Class == dustcheck.ClassMoved && e.Depth > 0 {
			continue
		}
		scope := e.Scope
		if scope == "" {
			scope = dustcheck.ScopeUnknown
		}
		metrics.FlatDustEvents.WithLabelValues(e.Filter, e.Class, scope).Inc()
	}
	var check app.DustLightCheck
	if err := p.db.WithContext(ctx).Where("night >= ? AND status = ?", now.Add(-DustAlertWindow).Truncate(24*time.Hour),
		dustcheck.LightsChecked).Order("night DESC").Limit(1).Find(&check).Error; err != nil {
		return
	}
	var discs []dustcheck.Blob
	if check.ID != 0 && check.Discs != "" {
		_ = json.Unmarshal([]byte(check.Discs), &discs)
	}
	metrics.LightsDustResidual.Set(check.Deepest)
	metrics.LightsDustMismatches.Set(float64(len(discs)))
}
