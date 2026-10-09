package stacking

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

const (
	registeredPrefix      = "registered/"
	registeredExt         = ".xisf"
	legacyRegisteredExt   = ".fit"
	registeredContentType = "application/xisf"
)

func structuralKeywords() map[string]bool {
	return map[string]bool{
		"SIMPLE": true, "BITPIX": true, "NAXIS": true, "NAXIS1": true, "NAXIS2": true, "NAXIS3": true,
		"EXTEND": true, "BZERO": true, "BSCALE": true, "ROWORDER": true,
	}
}

func registeredKey(object, filter, frameKey string) string {
	return path.Join("registered", object, filter, strings.TrimSuffix(path.Base(frameKey), path.Ext(frameKey))+registeredExt)
}

func encodeRegistered(src []byte) ([]byte, *imagedata.Image, error) {
	im, err := imagedata.Decode(src)
	if err != nil {
		return nil, nil, err
	}
	all, err := frameheader.ParseCards(src)
	if err != nil {
		return nil, nil, err
	}
	structural := structuralKeywords()
	var cards []imagedata.Card
	for _, c := range all {
		if !structural[c.Name] {
			cards = append(cards, imageCard(c))
		}
	}
	props, keywords := pixInsightSolution(cards, im.H)
	props = append(props, imagedata.Pedestal16Properties()...)
	plane := im.Plane(0)
	codes := make([]uint16, len(plane))
	for i, v := range plane {
		codes[i] = imagedata.Quantize16(v)
	}
	var out bytes.Buffer
	if err := imagedata.WriteXISF16(&out, im.W, im.H, codes, keywords, props); err != nil {
		return nil, nil, err
	}
	return out.Bytes(), im, nil
}

func encodeRegisteredFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	enc, _, err := encodeRegistered(b)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path.Base(src), err)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := f.Write(enc); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type subStats struct {
	w, h, empty, clipped int
	mean, maxErr         float64
}

func compareEncoded(orig, enc *imagedata.Image) (subStats, error) {
	if orig.W != enc.W || orig.H != enc.H {
		return subStats{}, fmt.Errorf("decoded %dx%d, want %dx%d", enc.W, enc.H, orig.W, orig.H)
	}
	a, b := orig.Plane(0), enc.Plane(0)
	lo := float32(imagedata.Pedestal16Zero + imagedata.Pedestal16Step)
	hi := float32(imagedata.Pedestal16Zero + math.MaxUint16*imagedata.Pedestal16Step)
	s := subStats{w: enc.W, h: enc.H}
	var sumA, sumB float64
	for i, v := range a {
		q := b[i]
		if (v == 0) != (q == 0) {
			return s, fmt.Errorf("pixel %d is %v, decoded as %v", i, v, q)
		}
		if v == 0 {
			s.empty++
			continue
		}
		sumA += float64(v)
		sumB += float64(q)
		if v < lo || v > hi || math.IsNaN(float64(v)) {
			s.clipped++
			continue
		}
		s.maxErr = max(s.maxErr, math.Abs(float64(v)-float64(q)))
	}
	if n := len(a) - s.empty; n > 0 {
		s.mean = sumB / float64(n)
		if d := math.Abs(sumA-sumB) / float64(n); d > imagedata.Pedestal16Step {
			return s, fmt.Errorf("mean moved by %g", d)
		}
	}
	if s.maxErr > imagedata.Pedestal16Step/2+1e-7 {
		return s, fmt.Errorf("pixel moved by %g, more than half a step", s.maxErr)
	}
	return s, nil
}

type registeredObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type registeredStore interface {
	list(ctx context.Context, prefix string) ([]registeredObject, error)
	stat(ctx context.Context, key string) (registeredObject, error)
	get(ctx context.Context, key string) ([]byte, error)
	put(ctx context.Context, key string, b []byte, contentType string) error
	remove(ctx context.Context, key string) error
}

type minioStore struct {
	s3     *minio.Client
	bucket string
}

func (m minioStore) list(ctx context.Context, prefix string) ([]registeredObject, error) {
	var out []registeredObject
	for o := range m.s3.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, registeredObject{Key: o.Key, Size: o.Size, LastModified: o.LastModified})
	}
	return out, nil
}

func (m minioStore) stat(ctx context.Context, key string) (registeredObject, error) {
	o, err := m.s3.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return registeredObject{}, err
	}
	return registeredObject{Key: o.Key, Size: o.Size, LastModified: o.LastModified}, nil
}

func (m minioStore) get(ctx context.Context, key string) ([]byte, error) {
	o, err := m.s3.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (m minioStore) put(ctx context.Context, key string, b []byte, contentType string) error {
	_, err := m.s3.PutObject(ctx, m.bucket, key, bytes.NewReader(b), int64(len(b)), minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m minioStore) remove(ctx context.Context, key string) error {
	return m.s3.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

func (p *Pipeline) registeredStore() registeredStore {
	if p.objects != nil {
		return p.objects
	}
	return minioStore{s3: p.s3, bucket: p.dest}
}

type subRow struct {
	Status        string
	ProcessedAt   time.Time
	RegisteredKey *string
}

type gcVerdict int

const (
	gcKeep gcVerdict = iota
	gcYoung
	gcLeftRecently
	gcAmbiguous
	gcDelete
)

func registeredStem(key string) string {
	return strings.TrimSuffix(key, path.Ext(key))
}

func gcDecide(obj registeredObject, rows []subRow, referenced map[string]bool, listed map[string]registeredObject, now time.Time, grace time.Duration) gcVerdict {
	if referenced[obj.Key] {
		return gcKeep
	}
	if now.Sub(obj.LastModified) < grace {
		return gcYoung
	}
	for _, r := range rows {
		if r.Status != app.StackStatusAdded {
			if now.Sub(r.ProcessedAt) < grace {
				return gcLeftRecently
			}
			continue
		}
		if r.RegisteredKey == nil || registeredStem(*r.RegisteredKey) != registeredStem(obj.Key) {
			return gcAmbiguous
		}
		cur, ok := listed[*r.RegisteredKey]
		if !ok || now.Sub(cur.LastModified) < grace {
			return gcYoung
		}
	}
	return gcDelete
}

type gcResult struct {
	listed, referenced, deleted, young, left, ambiguous, failed int
	deletedBytes                                                int64
}

func (p *Pipeline) collectRegistered(ctx context.Context, store registeredStore, now time.Time) (gcResult, error) {
	grace := p.opts.RegisteredGrace
	var res gcResult
	objs, err := store.list(ctx, registeredPrefix)
	if err != nil {
		return res, fmt.Errorf("list registered subs: %w", err)
	}
	res.listed = len(objs)
	var keys []string
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("registered_key IS NOT NULL").
		Pluck("registered_key", &keys).Error; err != nil {
		return res, err
	}
	referenced := make(map[string]bool, len(keys))
	for _, k := range keys {
		referenced[k] = true
	}
	var rows []struct {
		Status        string
		ProcessedAt   time.Time
		RegisteredKey *string
		Key           string
	}
	if err := p.db.WithContext(ctx).Table("stack_frames AS sf").
		Select("sf.status, sf.processed_at, sf.registered_key, f.key").
		Joins("JOIN frames f ON f.id = sf.frame_id").Scan(&rows).Error; err != nil {
		return res, err
	}
	byBase := map[string][]subRow{}
	for _, r := range rows {
		base := strings.TrimSuffix(path.Base(r.Key), path.Ext(r.Key))
		byBase[base] = append(byBase[base], subRow{Status: r.Status, ProcessedAt: r.ProcessedAt, RegisteredKey: r.RegisteredKey})
	}
	listed := make(map[string]registeredObject, len(objs))
	for _, o := range objs {
		listed[o.Key] = o
	}
	for _, o := range objs {
		base := path.Base(registeredStem(o.Key))
		switch gcDecide(o, byBase[base], referenced, listed, now, grace) {
		case gcKeep:
			res.referenced++
			continue
		case gcYoung:
			res.young++
			continue
		case gcLeftRecently:
			res.left++
			continue
		case gcAmbiguous:
			res.ambiguous++
			continue
		case gcDelete:
		}
		if p.stopping(ctx) {
			break
		}
		if err := p.removeUnreferenced(ctx, store, o); err != nil {
			res.failed++
			slog.Warn("Could not delete an unreferenced registered sub", "key", o.Key, "error", err)
			continue
		}
		res.deleted++
		res.deletedBytes += o.Size
		metrics.RegisteredDeleted.Inc()
		metrics.RegisteredDeletedBytes.Add(float64(o.Size))
		if !p.pause(ctx, p.opts.RegisteredDeletePause) {
			break
		}
	}
	return res, nil
}

func (p *Pipeline) removeUnreferenced(ctx context.Context, store registeredStore, o registeredObject) error {
	var n int64
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("registered_key = ?", o.Key).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("referenced again")
	}
	cur, err := store.stat(ctx, o.Key)
	if err != nil {
		return err
	}
	if !cur.LastModified.Equal(o.LastModified) {
		return fmt.Errorf("rewritten since listed")
	}
	return store.remove(ctx, o.Key)
}

func (p *Pipeline) runRegisteredGC(ctx context.Context) {
	if p.opts.RegisteredGrace <= 0 {
		return
	}
	for !p.stopping(ctx) {
		start := time.Now()
		res, err := p.collectRegistered(ctx, p.registeredStore(), start)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("Collecting unreferenced registered subs failed", "error", err)
			}
		} else {
			slog.Info("Collected unreferenced registered subs", "listed", res.listed, "referenced", res.referenced,
				"deleted", res.deleted, "deleted_gib", fmt.Sprintf("%.1f", float64(res.deletedBytes)/(1<<30)),
				"young", res.young, "left_recently", res.left, "ambiguous", res.ambiguous, "failed", res.failed,
				"duration", time.Since(start).Round(time.Second))
		}
		if !p.pause(ctx, time.Hour) {
			return
		}
	}
}

type backfillRow struct {
	ID            int
	RegisteredKey string
	Object        string
}

type backfillOutcome int

const (
	backfillConverted backfillOutcome = iota
	backfillBusy
	backfillMoved
)

func (p *Pipeline) convertRegistered(ctx context.Context, store registeredStore, r backfillRow) (backfillOutcome, int64, int64, error) {
	if !p.hold(r.Object) {
		return backfillBusy, 0, 0, nil
	}
	defer p.release(r.Object)
	old, err := store.get(ctx, r.RegisteredKey)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("get: %w", err)
	}
	enc, orig, err := encodeRegistered(old)
	if err != nil {
		return 0, int64(len(old)), 0, fmt.Errorf("encode: %w", err)
	}
	dec, err := imagedata.Decode(enc)
	if err != nil {
		return 0, int64(len(old)), 0, fmt.Errorf("decode: %w", err)
	}
	if _, err := compareEncoded(orig, dec); err != nil {
		return 0, int64(len(old)), 0, fmt.Errorf("verify: %w", err)
	}
	newKey := registeredStem(r.RegisteredKey) + registeredExt
	if err := store.put(ctx, newKey, enc, registeredContentType); err != nil {
		return 0, int64(len(old)), 0, fmt.Errorf("put: %w", err)
	}
	st, err := store.stat(ctx, newKey)
	if err != nil {
		return 0, int64(len(old)), int64(len(enc)), fmt.Errorf("stat: %w", err)
	}
	if st.Size != int64(len(enc)) {
		_ = store.remove(ctx, newKey)
		return 0, int64(len(old)), int64(len(enc)), fmt.Errorf("stored copy is %d bytes, want %d", st.Size, len(enc))
	}
	res := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("id = ? AND status = ? AND registered_key = ?", r.ID, app.StackStatusAdded, r.RegisteredKey).
		UpdateColumn("registered_key", newKey)
	if res.Error != nil {
		return 0, int64(len(old)), int64(len(enc)), res.Error
	}
	if res.RowsAffected == 0 {
		var n int64
		if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("registered_key = ?", newKey).Count(&n).Error; err == nil && n == 0 {
			_ = store.remove(ctx, newKey)
		}
		return backfillMoved, int64(len(old)), int64(len(enc)), nil
	}
	if err := store.remove(ctx, r.RegisteredKey); err != nil {
		slog.Warn("Converted a registered sub but could not delete the old one; the collector will", "key", r.RegisteredKey, "error", err)
	}
	return backfillConverted, int64(len(old)), int64(len(enc)), nil
}

func (p *Pipeline) backfillBatch(ctx context.Context, after, limit int) ([]backfillRow, error) {
	var rows []backfillRow
	err := p.db.WithContext(ctx).Table("stack_frames AS sf").
		Select("sf.id, sf.registered_key, s.object").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("sf.status = ? AND sf.registered_key LIKE ? AND sf.id > ?", app.StackStatusAdded, registeredPrefix+"%"+legacyRegisteredExt, after).
		Order("sf.id").Limit(limit).Scan(&rows).Error
	return rows, err
}

func (p *Pipeline) legacyRemaining(ctx context.Context) (int64, error) {
	var n int64
	err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("status = ? AND registered_key LIKE ?", app.StackStatusAdded, registeredPrefix+"%"+legacyRegisteredExt).Count(&n).Error
	return n, err
}

func (p *Pipeline) runRegisteredBackfill(ctx context.Context) {
	rate := p.opts.RegisteredBackfillRate
	if rate <= 0 {
		return
	}
	interval := time.Duration(float64(time.Second) / rate)
	store := p.registeredStore()
	start := time.Now()
	total, err := p.legacyRemaining(ctx)
	if err != nil {
		slog.Error("Registered sub backfill could not count its work", "error", err)
		return
	}
	slog.Info("Converting registered subs to 16-bit XISF", "subs", total, "per_second", rate)
	metrics.RegisteredBackfillRemaining.Set(float64(total))
	var converted, busy, failed int
	var read, written int64
	tries := map[int]int{}
	gaveUp := 0
	after := 0
	for !p.stopping(ctx) {
		rows, err := p.backfillBatch(ctx, after, 100)
		if err != nil {
			slog.Error("Registered sub backfill query failed", "error", err)
			if !p.pause(ctx, time.Minute) {
				return
			}
			continue
		}
		if len(rows) == 0 {
			left, err := p.legacyRemaining(ctx)
			if err == nil && left <= int64(gaveUp) {
				metrics.RegisteredBackfillRemaining.Set(float64(left))
				slog.Info("Registered sub backfill done", "converted", converted, "failed", failed, "given_up", gaveUp,
					"read_gib", fmt.Sprintf("%.1f", float64(read)/(1<<30)), "written_gib", fmt.Sprintf("%.1f", float64(written)/(1<<30)),
					"duration", time.Since(start).Round(time.Second))
				return
			}
			after = 0
			if !p.pause(ctx, 5*time.Minute) {
				return
			}
			continue
		}
		for _, r := range rows {
			after = r.ID
			if p.stopping(ctx) {
				return
			}
			if tries[r.ID] >= 3 {
				continue
			}
			next := time.Now().Add(interval)
			outcome, in, out, err := p.convertRegistered(ctx, store, r)
			read += in
			written += out
			metrics.RegisteredBackfillBytes.WithLabelValues("read").Add(float64(in))
			metrics.RegisteredBackfillBytes.WithLabelValues("written").Add(float64(out))
			switch {
			case err != nil:
				failed++
				if tries[r.ID]++; tries[r.ID] == 3 {
					gaveUp++
				}
				metrics.RegisteredBackfill.WithLabelValues("failed").Inc()
				if ctx.Err() == nil {
					slog.Warn("Could not convert a registered sub", "key", r.RegisteredKey, "error", err)
				}
			case outcome == backfillBusy:
				busy++
				metrics.RegisteredBackfill.WithLabelValues("busy").Inc()
				continue
			case outcome == backfillMoved:
				metrics.RegisteredBackfill.WithLabelValues("moved").Inc()
			default:
				converted++
				metrics.RegisteredBackfill.WithLabelValues("converted").Inc()
			}
			if converted > 0 && converted%100 == 0 && outcome == backfillConverted && err == nil {
				left, _ := p.legacyRemaining(ctx)
				metrics.RegisteredBackfillRemaining.Set(float64(left))
				perSub := time.Since(start) / time.Duration(converted)
				slog.Info("Registered sub backfill progress", "converted", converted, "left", left, "busy_skips", busy, "failed", failed,
					"read_gib", fmt.Sprintf("%.1f", float64(read)/(1<<30)), "written_gib", fmt.Sprintf("%.1f", float64(written)/(1<<30)),
					"eta", (perSub * time.Duration(left)).Round(time.Minute))
			}
			if !p.pause(ctx, time.Until(next)) {
				return
			}
		}
	}
}
