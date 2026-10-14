package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/ninapath"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

func isMovingBody(stored map[string]bodies.Target, object string) bool {
	b, ok := stored[object]
	if !ok || strings.TrimSpace(b.Designation) == "" || bodies.IsEvent(b.Designation, b.Kind) {
		return false
	}
	k := strings.TrimSpace(b.Kind)
	return k == "" || k == bodies.KindComet || k == bodies.KindAsteroid
}

func nightKey(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(goals.NightLayout)
}

func (p *Pipeline) referenceNight(ctx context.Context, tr app.TargetReference) (string, error) {
	var f app.Frame
	err := p.db.WithContext(ctx).Select("id", "night").First(&f, tr.FrameID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return nightKey(f.Night), nil
}

func splitByNight(batch []candidate, refNight string) ([]candidate, map[string][]candidate) {
	var main []candidate
	others := map[string][]candidate{}
	for _, c := range batch {
		n := nightKey(c.frame.Night)
		if refNight == "" || n == "" || n == refNight {
			main = append(main, c)
			continue
		}
		others[n] = append(others[n], c)
	}
	return main, others
}

func nightRefKey(object, night string) string {
	return path.Join("stacks", ninapath.Folder(object), "nights", night, "reference.fit")
}

func (p *Pipeline) nightReferenceFile(object, night string) string {
	h := sha256.Sum256([]byte(object + "\x00" + night))
	return filepath.Join(p.workDir, "references", "night-"+hex.EncodeToString(h[:8])+".fit")
}

func (p *Pipeline) nightReference(ctx context.Context, stack *app.Stack, night string, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) (string, app.NightReference, error) {
	object := stack.Object
	local := p.nightReferenceFile(object, night)
	day, err := time.Parse(goals.NightLayout, night)
	if err != nil {
		return "", app.NightReference{}, err
	}
	var nr app.NightReference
	err = p.db.WithContext(ctx).Where("object = ? AND night = ?", object, day).First(&nr).Error
	if err == nil {
		if _, err := os.Stat(local); err != nil {
			if err := p.download(ctx, p.dest, nr.ObjectKey, local); err != nil {
				return "", nr, err
			}
		}
		p.solveNightReference(ctx, stack, &nr, local)
		return local, nr, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nr, err
	}
	var frames []app.Frame
	if err := p.db.WithContext(ctx).Where("type = ? AND object = ? AND index_error IS NULL AND filter <> '' AND night >= ? AND night < ?",
		"LIGHT", object, day, day.AddDate(0, 0, 1)).Find(&frames).Error; err != nil {
		return "", nr, fmt.Errorf("load the night's lights: %w", err)
	}
	var usable []candidate
	for _, f := range frames {
		if c, status := p.classify(f, scores, sets, positions); status == "" {
			usable = append(usable, c)
		}
	}
	best, ok := pickReference(mainFraming(referenceCandidates(usable)))
	if !ok {
		return "", nr, errors.New("no usable lights that night")
	}
	dir, err := os.MkdirTemp(p.workDir, "nightref-")
	if err != nil {
		return "", nr, err
	}
	defer os.RemoveAll(dir)
	cals, err := p.calibrate(ctx, dir, []candidate{best}, sets)
	if err != nil {
		return "", nr, err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", nr, err
	}
	if err := os.Rename(cals[0].path, local); err != nil {
		return "", nr, err
	}
	key := nightRefKey(object, night)
	if err := p.upload(ctx, local, key, "application/fits"); err != nil {
		return "", nr, err
	}
	nr = app.NightReference{Object: object, Night: day, FrameID: best.frame.ID, ObjectKey: key, CreatedAt: time.Now()}
	if err := p.db.WithContext(ctx).Create(&nr).Error; err != nil {
		return "", nr, err
	}
	slog.Info("Chose a night's registration reference for a moving body", "object", object, "night", night, "frame", best.frame.Key)
	p.solveNightReference(ctx, stack, &nr, local)
	return local, nr, nil
}

func nightSolveDue(nr app.NightReference, now time.Time) bool {
	if nr.WCS != nil {
		return false
	}
	return nr.SolveAttempts < maxSolveAttempts || nr.SolvedAt == nil || now.Sub(*nr.SolvedAt) >= nightSolveRetry
}

func (p *Pipeline) solveNightReference(ctx context.Context, stack *app.Stack, nr *app.NightReference, local string) {
	if !nightSolveDue(*nr, time.Now()) {
		return
	}
	var ref app.Frame
	if err := p.db.WithContext(ctx).First(&ref, nr.FrameID).Error; err != nil {
		msg := "the night reference's light is gone: " + err.Error()
		now := time.Now()
		nr.SolveAttempts++
		nr.SolveError, nr.SolvedAt = &msg, &now
		p.db.WithContext(ctx).Model(nr).Updates(map[string]any{colSolveAttempts: nr.SolveAttempts, "solve_error": msg, "solved_at": now})
		return
	}
	tr := app.TargetReference{Object: nr.Object, FrameID: nr.FrameID}
	_, nina := p.referenceCards(ctx, &tr)
	dir, err := os.MkdirTemp(p.workDir, "nightsolve-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "reference.fit")
	if err := os.Link(local, file); err != nil {
		return
	}
	cards, err := p.solveMaster(ctx, p.solveInputs(ctx, stack, ref.Key, nina), stack, dir, file)
	now := time.Now()
	updates := map[string]any{colSolveAttempts: nr.SolveAttempts + 1, "solved_at": now}
	nr.SolveAttempts++
	nr.SolvedAt = &now
	if err != nil {
		msg := err.Error()
		updates["solve_error"] = msg
		nr.SolveError = &msg
		slog.Warn("Could not plate solve a night's reference", "object", nr.Object, "night", nightKey(&nr.Night), "error", err)
	} else if b, err := json.Marshal(cards); err == nil {
		w := string(b)
		updates["wcs"], updates["solve_error"] = w, nil
		nr.WCS, nr.SolveError = &w, nil
	}
	if err := p.db.WithContext(ctx).Model(nr).Updates(updates).Error; err != nil {
		slog.Warn("Could not save a night reference's plate solution", "object", nr.Object, "error", err)
	}
}

func (p *Pipeline) stackNightBatch(ctx context.Context, stack *app.Stack, night string, batch []candidate, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) error {
	ref, _, err := p.nightReference(ctx, stack, night, sets, scores, positions)
	if err != nil {
		return fmt.Errorf("night reference for %s on %s: %w", stack.Object, night, err)
	}
	dir, err := os.MkdirTemp(p.workDir, "nightbatch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p.progress(stack.Object, stack.Filter, StageCalibrating, 0, len(batch))
	cals, err := p.calibrate(ctx, dir, batch, sets)
	if err != nil {
		return err
	}
	p.progress(stack.Object, stack.Filter, StageRegistering, 0, len(cals))
	registered, err := p.register(ctx, dir, ref, cals, false)
	if err != nil {
		return err
	}
	failed := 0
	for i, c := range cals {
		exp := *c.c.frame.Exposure
		sf := app.StackFrame{FrameID: c.c.frame.ID, StackID: &stack.ID, Status: app.StackStatusNight,
			Score: c.c.score.Score, Weight: c.c.score.Score * exp, Exposure: exp, DarkScale: c.darkScale,
			NoDark: c.c.cal.Dark.Set == nil && !precalibrated(c.c.frame), BiasMaster: optional(c.bias), DarkMaster: optional(c.dark),
			FlatMaster: optional(c.flat), FlatMatch: c.flatMatch(), MetadataSource: optional(c.c.source)}
		if registered[i] == "" {
			msg := "Siril could not register the sub to its night's reference"
			sf.StackID, sf.Status, sf.Weight, sf.Error = nil, app.StackStatusRegistration, 0, &msg
			if c.c.offBy > 0 {
				sf.Status, sf.Error = app.StackStatusOffTarget, offTargetError(c.c.offBy)
			}
			if err := p.record(ctx, sf); err != nil {
				return err
			}
			failed++
			continue
		}
		key := registeredKey(stack.Object, stack.Filter, c.c.frame.Key)
		local := strings.TrimSuffix(registered[i], filepath.Ext(registered[i])) + registeredExt
		if err := encodeRegisteredFile(registered[i], local); err != nil {
			return err
		}
		if err := p.upload(ctx, local, key, registeredContentType); err != nil {
			return err
		}
		sf.RegisteredKey = &key
		if err := p.record(ctx, sf); err != nil {
			return err
		}
	}
	slog.Info("Registered a moving body's subs to their night's reference", "object", stack.Object, "filter", stack.Filter, "night", night, "subs", len(cals), "failed", failed)
	if failed > 0 {
		return p.maybeReReferenceNight(ctx, stack.Object, night)
	}
	return nil
}

func (p *Pipeline) splitBodyBatch(ctx context.Context, stack *app.Stack, tr app.TargetReference, batch []candidate, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) ([]candidate, error) {
	refNight, err := p.referenceNight(ctx, tr)
	if err != nil {
		return nil, err
	}
	main, others := splitByNight(batch, refNight)
	for _, n := range sortedNights(others) {
		err := p.stackNightBatch(ctx, stack, n, others[n], sets, scores, positions)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		slog.Error("Stacking a moving body's night failed", "object", stack.Object, "filter", stack.Filter, "night", n, "error", err)
		msg := err.Error()
		for _, c := range others[n] {
			if err := p.record(ctx, app.StackFrame{FrameID: c.frame.ID, Status: app.StackStatusFailed,
				Score: c.score.Score, Exposure: val(c.frame.Exposure), Error: &msg}); err != nil {
				return nil, err
			}
		}
	}
	return main, nil
}

func sortedNights(m map[string][]candidate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (p *Pipeline) bodyReferenceNight(ctx context.Context, object string) (time.Time, bool, error) {
	if !isMovingBody(p.storedDesignations(ctx), object) {
		return time.Time{}, false, nil
	}
	var tr app.TargetReference
	err := p.db.WithContext(ctx).Where("object = ?", object).First(&tr).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	n, err := p.referenceNight(ctx, tr)
	if err != nil || n == "" {
		return time.Time{}, false, err
	}
	t, err := time.Parse(goals.NightLayout, n)
	return t, err == nil, err
}

func (p *Pipeline) nightReferenceRow(ctx context.Context, object string, night time.Time) (app.NightReference, bool, error) {
	var nr app.NightReference
	if has, err := tables.Has(ctx, p.db, &app.NightReference{}); err != nil || !has {
		return nr, false, err
	}
	err := p.db.WithContext(ctx).Where("object = ? AND night = ?", object, night).First(&nr).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nr, false, nil
	}
	return nr, err == nil, err
}

func wcsCards(s *string) ([]frameheader.Card, bool) {
	if s == nil {
		return nil, false
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*s), &cards); err != nil || len(cards) == 0 {
		return nil, false
	}
	return cards, true
}

func (p *Pipeline) nightWCS(ctx context.Context, object string, night time.Time) ([]frameheader.Card, bool) {
	nr, ok, err := p.nightReferenceRow(ctx, object, night)
	if err != nil || !ok {
		return nil, false
	}
	return wcsCards(nr.WCS)
}

func (p *Pipeline) nightField(ctx context.Context, stack *app.Stack, night time.Time) (bodyField, bool, error) {
	nr, ok, err := p.nightReferenceRow(ctx, stack.Object, night)
	if err != nil {
		return bodyField{}, false, err
	}
	if !ok {
		f, err := p.bodyField(ctx, stack)
		return f, false, err
	}
	cards, ok := wcsCards(nr.WCS)
	if !ok {
		return bodyField{}, true, errors.New("the night's registration reference has no plate solution yet")
	}
	kw := frameheader.Keywords{}
	for _, c := range cards {
		kw[c.Name] = c.Value
	}
	sky, err := wcsFromHeader(kw, stack.Width, stack.Height)
	if err != nil {
		return bodyField{}, true, err
	}
	var ref app.Frame
	if err := p.db.WithContext(ctx).First(&ref, nr.FrameID).Error; err != nil {
		return bodyField{}, true, fmt.Errorf("night reference frame: %w", err)
	}
	refKW, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: ref.Key, Size: ref.Size})
	if err != nil {
		return bodyField{}, true, fmt.Errorf("night reference header: %w", err)
	}
	f := bodyField{sky: sky, lat: refKW.Float("SITELAT"), lon: refKW.Float("SITELONG"), elev: refKW.Float("SITEELEV")}
	if math.IsNaN(f.lat) || math.IsNaN(f.lon) {
		return bodyField{}, true, errors.New("the night's reference sub has no site coordinates")
	}
	if math.IsNaN(f.elev) {
		f.elev = 0
	}
	return f, true, nil
}

func (p *Pipeline) starAligned(ctx context.Context, dir string, stack *app.Stack, box Rect, cropped, nightRef bool, n int, read func(int) ([]float32, int, int, error)) ([]float32, error) {
	if !nightRef {
		master, _, err := p.loadMaster(ctx, dir, stack)
		if err != nil {
			return nil, fmt.Errorf("star-aligned master: %w", err)
		}
		if cropped && master.W == stack.Width && master.H == stack.Height {
			return crop(master.Data, master.W, box), nil
		}
		return master.Data, nil
	}
	return meanOfSubs(n, box.W*box.H, read)
}

func meanOfSubs(n, size int, read func(int) ([]float32, int, int, error)) ([]float32, error) {
	sum := make([]float64, size)
	cnt := make([]int32, size)
	for i := range n {
		sub, _, _, err := read(i)
		if err != nil {
			return nil, err
		}
		if len(sub) != size {
			return nil, fmt.Errorf("sub %d has %d pixels, want %d", i, len(sub), size)
		}
		for j, v := range sub {
			if v != 0 {
				sum[j] += float64(v)
				cnt[j]++
			}
		}
	}
	out := make([]float32, size)
	for j := range out {
		if cnt[j] > 0 {
			out[j] = float32(sum[j] / float64(cnt[j]))
		}
	}
	return out, nil
}

func stackedStatuses() []string {
	return []string{app.StackStatusAdded, app.StackStatusNight}
}

func (p *Pipeline) movingTarget(ctx context.Context, object string) (bool, error) {
	if p.sched == nil {
		return false, nil
	}
	stored, err := bodies.TargetsByName(ctx, p.sched)
	if err != nil {
		return false, fmt.Errorf("read the moving-body targets: %w", err)
	}
	return isMovingBody(stored, object), nil
}

func nightResetKey(object, night string) string {
	return object + "@night:" + night
}

func (p *Pipeline) maybeReReferenceNight(ctx context.Context, object, night string) error {
	day, err := time.Parse(goals.NightLayout, night)
	if err != nil {
		return err
	}
	var counts struct{ Failed, Added int64 }
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Joins("JOIN frames f ON f.id = stack_frames.frame_id").
		Where("f.object = ? AND f.night >= ? AND f.night < ?", object, day, day.AddDate(0, 0, 1)).
		Select("SUM(CASE WHEN stack_frames.status = ? THEN 1 ELSE 0 END) AS failed, "+
			"SUM(CASE WHEN stack_frames.status = ? THEN 1 ELSE 0 END) AS added",
			app.StackStatusRegistration, app.StackStatusNight).
		Scan(&counts).Error; err != nil {
		return err
	}
	if counts.Failed < reReferenceFailures || float64(counts.Failed) < reReferenceShare*float64(counts.Failed+counts.Added) {
		return nil
	}
	key := nightResetKey(object, night)
	var reset app.ReferenceReset
	if err := p.db.WithContext(ctx).Where("object = ?", key).First(&reset).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if reset.Count >= maxReReferences {
		return nil
	}
	slog.Warn("Replacing a night's registration reference; its subs register again", "object", object, "night", night,
		"failed", counts.Failed, "registered", counts.Added, "reset", reset.Count+1)
	reset.Object, reset.Count, reset.At = key, reset.Count+1, time.Now()
	var oldKeys []string
	if err := p.db.WithContext(ctx).Model(&app.NightReference{}).Where("object = ? AND night = ?", object, day).Pluck("object_key", &oldKeys).Error; err != nil {
		return err
	}
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("object = ? AND night = ?", object, day).Delete(&app.NightReference{}).Error; err != nil {
			return err
		}
		if err := tx.Where("frame_id IN (?)", tx.Model(&app.Frame{}).Select("id").
			Where("object = ? AND night >= ? AND night < ?", object, day, day.AddDate(0, 0, 1))).
			Where("status IN ?", []string{app.StackStatusNight, app.StackStatusRegistration}).
			Delete(&app.StackFrame{}).Error; err != nil {
			return err
		}
		return tx.Save(&reset).Error
	})
	if err != nil {
		return err
	}
	_ = os.Remove(p.nightReferenceFile(object, night))
	p.removeOldLayout(ctx, object, oldKeys)
	return nil
}

const nightSolveRetry = 24 * time.Hour

func (p *Pipeline) solvePendingNightReferences(ctx context.Context, stack *app.Stack) {
	if has, err := tables.Has(ctx, p.db, &app.NightReference{}); err != nil || !has {
		return
	}
	var refs []app.NightReference
	if err := p.db.WithContext(ctx).Where("object = ? AND wcs IS NULL", stack.Object).Find(&refs).Error; err != nil {
		return
	}
	now := time.Now()
	for i := range refs {
		nr := &refs[i]
		if p.stopping(ctx) || !nightSolveDue(*nr, now) || !p.holdWait(ctx, stack.Object, bodyHoldWait) {
			continue
		}
		if err := p.db.WithContext(ctx).Where("wcs IS NULL").First(nr, nr.ID).Error; err != nil {
			p.release(stack.Object)
			continue
		}
		local := p.nightReferenceFile(nr.Object, nightKey(&nr.Night))
		if _, err := os.Stat(local); err != nil {
			err = os.MkdirAll(filepath.Dir(local), 0o700)
			if err == nil {
				err = p.download(ctx, p.dest, nr.ObjectKey, local)
			}
			if err != nil {
				p.release(stack.Object)
				continue
			}
		}
		p.solveNightReference(ctx, stack, nr, local)
		p.release(stack.Object)
	}
}

func countSubs(nights map[string][]nightSub) int {
	n := 0
	for _, rows := range nights {
		n += len(rows)
	}
	return n
}

func (p *Pipeline) nightsWithReference(ctx context.Context, object string) (map[string]bool, error) {
	out := map[string]bool{}
	if has, err := tables.Has(ctx, p.db, &app.NightReference{}); err != nil || !has {
		return out, err
	}
	var nights []time.Time
	if err := p.db.WithContext(ctx).Model(&app.NightReference{}).Where("object = ?", object).Pluck("night", &nights).Error; err != nil {
		return nil, err
	}
	for i := range nights {
		out[nightKey(&nights[i])] = true
	}
	return out, nil
}
