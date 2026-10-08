// Package indexer keeps the frames table in step with the object store: it
// lists the bucket, reads the header of every new or changed image with a
// ranged GET, and records what calibration matching needs.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func imageExtensions() map[string]bool {
	return map[string]bool{".xisf": true, ".fits": true, ".fit": true, ".fts": true}
}

type Indexer struct {
	client      *minio.Client
	bucket      string
	db          *gorm.DB
	concurrency int

	// Events, if set, hears when new frames are indexed.
	Events *events.Broker
}

func New(client *minio.Client, bucket string, db *gorm.DB, concurrency int) *Indexer {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Indexer{client: client, bucket: bucket, db: db, concurrency: concurrency}
}

// Run indexes the bucket every interval until ctx is cancelled. Between
// scans it measures lights' starlight a batch at a time: back to back while
// a backlog remains, so a backfill takes hours rather than days, but never
// holding up the next scan for longer than one batch, so new lights are
// indexed on time. With no backlog it waits out the interval.
func (ix *Indexer) Run(ctx context.Context, interval time.Duration) {
	runLoop(ctx, interval, ix.scan, ix.photometryPass)
}

// runLoop calls scan every interval, and pass between scans: again at once
// while it reports a backlog and the next scan isn't due, otherwise once
// before waiting for the next scan.
func runLoop(ctx context.Context, interval time.Duration, scan func(context.Context), pass func(context.Context) bool) {
	for {
		scan(ctx)
		next := time.Now().Add(interval)
		for {
			backlog := pass(ctx)
			if ctx.Err() != nil {
				return
			}
			wait := time.Until(next)
			if wait <= 0 {
				break
			}
			if backlog {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			break
		}
	}
}

// scan indexes the bucket, then reads what new lights need before they can
// be scored.
func (ix *Indexer) scan(ctx context.Context) {
	start := time.Now()
	stats, err := ix.IndexOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Indexing failed", "error", err)
	} else if err == nil {
		if stats.Indexed > 0 {
			ix.Events.Publish(events.Event{Type: events.TypeFrames})
		}
		slog.Info("Indexed bucket", "bucket", ix.bucket, "seen", stats.Seen, "indexed", stats.Indexed,
			"failed", stats.Failed, "removed", stats.Removed, "duration", time.Since(start).Round(time.Millisecond))
	}
	if n, err := ix.BackfillPointing(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Reading mount pointing failed", "error", err)
	} else if n > 0 {
		slog.Info("Read mount pointing", "lights", n)
	}
	if n, err := ix.MeasureUnrecorded(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Measuring lights failed", "error", err)
	} else if n > 0 {
		slog.Info("Measured lights without a scheduler record", "lights", n)
	}
}

// photometryPass measures one batch of lights' starlight and reports whether
// another batch should follow at once: it measured some and more are
// pending. A batch that measured nothing (downloads failing, say) waits for
// the next scan rather than spinning.
func (ix *Indexer) photometryPass(ctx context.Context) bool {
	start := time.Now()
	n, err := ix.MeasurePhotometry(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("Measuring lights' starlight failed", "error", err)
		}
		return false
	}
	if n == 0 {
		return false
	}
	var pending int64
	if err := photometryPending(ix.db.WithContext(ctx)).Count(&pending).Error; err != nil {
		slog.Error("Counting lights waiting for photometry failed", "error", err)
		return false
	}
	slog.Info("Measured lights' starlight", "lights", n, "pending", pending,
		"duration", time.Since(start).Round(time.Millisecond))
	return pending > 0
}

// BackfillPointing reads where the mount pointed for lights indexed before
// frames recorded it, a ranged read of each header.
func (ix *Indexer) BackfillPointing(ctx context.Context) (int, error) {
	var frames []app.Frame
	if err := pointingUnread(ix.db.WithContext(ctx)).Select("id", "key", "size").Find(&frames).Error; err != nil {
		return 0, err
	}
	work := make(chan app.Frame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range ix.concurrency {
		wg.Go(func() {
			for f := range work {
				kw, err := ReadHeader(ctx, ix.client, ix.bucket, minio.ObjectInfo{Key: f.Key, Size: f.Size})
				if err != nil {
					if ctx.Err() == nil {
						slog.Debug("Could not read header for pointing", "key", f.Key, "error", err)
					}
					continue
				}
				h := frameheader.FromKeywords(kw)
				if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).Updates(map[string]any{
					"mount_ra": ptr(h.RA), "mount_dec": ptr(h.Dec), "pointing_read": true, "pointing_wcs": true,
					"pointing_rev": PointingRevision,
				}).Error; err != nil {
					slog.Debug("Could not save pointing", "key", f.Key, "error", err)
					continue
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		})
	}
	for _, f := range frames {
		select {
		case work <- f:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return done, ctx.Err()
}

// PointingRevision counts the ways a light's pointing is found: 1, the
// plate solution's centre; 2, the target's position (OBJCTRA, OBJCTDEC).
// Lights still without one are read again when it goes up.
const PointingRevision = 2

// pointingUnread are the lights whose header hasn't been read for where
// they pointed: indexed before frames recorded it, or, without one, before
// the current PointingRevision. Frames indexed before the columns existed
// have them NULL.
func pointingUnread(db *gorm.DB) *gorm.DB {
	return db.Model(&app.Frame{}).Where("type = ? AND index_error IS NULL", "LIGHT").
		Where("pointing_read IS NULL OR pointing_read = ? OR (mount_ra IS NULL AND (pointing_rev IS NULL OR pointing_rev < ?))",
			false, PointingRevision)
}

// MeasureUnrecorded measures the sky and stars of lights the stacker found
// no Target Scheduler record for, so they can be scored from their pixels.
// Each is downloaded whole once.
func (ix *Indexer) MeasureUnrecorded(ctx context.Context) (int, error) {
	var frames []app.Frame
	if err := ix.db.WithContext(ctx).Select("id", "key").
		Where("type = ? AND index_error IS NULL AND measured_at IS NULL", "LIGHT").
		Where("id IN (?)", ix.db.Table("stack_frames").Select("frame_id").Where("status = ?", app.StackStatusNoMetadata)).
		Find(&frames).Error; err != nil {
		return 0, err
	}
	work := make(chan app.Frame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range ix.concurrency {
		wg.Go(func() {
			for f := range work {
				r, err := ix.measure(ctx, f.Key)
				if err != nil {
					if ctx.Err() == nil {
						slog.Debug("Could not measure light", "key", f.Key, "error", err)
					}
					continue
				}
				now := time.Now()
				if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).Updates(map[string]any{
					"sky_adu": r.SkyADU, "star_hfr": r.HFR, "star_count": r.Stars, "measured_at": now,
				}).Error; err != nil {
					slog.Debug("Could not save measurement", "key", f.Key, "error", err)
					continue
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		})
	}
	for _, f := range frames {
		select {
		case work <- f:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return done, ctx.Err()
}

func (ix *Indexer) measure(ctx context.Context, key string) (measure.Result, error) {
	b, err := ix.download(ctx, key)
	if err != nil {
		return measure.Result{}, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return measure.Result{}, err
	}
	return measure.Sub(im), nil
}

func (ix *Indexer) download(ctx context.Context, key string) ([]byte, error) {
	obj, err := ix.client.GetObject(ctx, ix.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

// photometryWorkers is how many lights MeasurePhotometry reads at once: each
// holds a whole light, about 150 MB decoded.
const photometryWorkers = 2

// photometryBatch is the most lights one pass measures, so a backfill of
// every light doesn't hold up indexing for hours.
const photometryBatch = 200

// photometryPending are the lights whose starlight hasn't been measured at
// the current measure.PhotometryRevision, leaving out those no master will
// take whatever their score (off target, duplicates, given up on).
func photometryPending(db *gorm.DB) *gorm.DB {
	return db.Model(&app.Frame{}).
		Where("type = ? AND index_error IS NULL", "LIGHT").
		Where("photometry_rev IS NULL OR photometry_rev < ?", measure.PhotometryRevision).
		Where("NOT EXISTS (SELECT 1 FROM stack_frames sf WHERE sf.frame_id = frames.id AND sf.status IN ?)",
			[]string{app.StackStatusOffTarget, app.StackStatusDuplicate, app.StackStatusDead})
}

// MeasurePhotometry measures the starlight of lights (measure.Photometry),
// newest first, for scoring their transparency; the stacker holds lights
// back until it has. Each is downloaded whole once. A light that can't be
// decoded is recorded as measured, with the error, so it isn't waited on.
func (ix *Indexer) MeasurePhotometry(ctx context.Context) (int, error) {
	var frames []app.Frame
	if err := photometryPending(ix.db.WithContext(ctx)).Select("id", "key").
		Order("date_obs DESC").Limit(photometryBatch).Find(&frames).Error; err != nil {
		return 0, err
	}
	work := make(chan app.Frame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range min(ix.concurrency, photometryWorkers) {
		wg.Go(func() {
			for f := range work {
				b, err := ix.download(ctx, f.Key)
				if err != nil {
					if ctx.Err() == nil {
						slog.Debug("Could not download light for photometry", "key", f.Key, "error", err)
					}
					continue // tried again next pass
				}
				cols := map[string]any{"photometry_rev": measure.PhotometryRevision}
				if im, err := imagedata.Decode(b); err != nil {
					msg := err.Error()
					cols["photometry"], cols["photometry_err"] = nil, msg
				} else {
					js, err := json.Marshal(measure.Measure(im))
					if err != nil {
						continue
					}
					cols["photometry"], cols["photometry_err"] = string(js), nil
				}
				if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).Updates(cols).Error; err != nil {
					slog.Debug("Could not save photometry", "key", f.Key, "error", err)
					continue
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		})
	}
	for _, f := range frames {
		select {
		case work <- f:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return done, ctx.Err()
}

type Stats struct {
	Seen, Indexed, Failed, Removed int
}

type known struct {
	Key  string
	ETag string
}

// IndexOnce makes one pass over the bucket.
func (ix *Indexer) IndexOnce(ctx context.Context) (Stats, error) {
	var stats Stats

	var rows []known
	if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Select("key", "e_tag").Scan(&rows).Error; err != nil {
		return stats, fmt.Errorf("load indexed frames: %w", err)
	}
	etags := make(map[string]string, len(rows))
	for _, r := range rows {
		etags[r.Key] = r.ETag
	}

	work := make(chan minio.ObjectInfo)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range ix.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for obj := range work {
				err := ix.indexObject(ctx, obj)
				mu.Lock()
				if err != nil {
					stats.Failed++
					slog.Warn("Could not index frame", "key", obj.Key, "error", err)
				} else {
					stats.Indexed++
					metrics.FramesIndexed.Inc()
				}
				mu.Unlock()
			}
		}()
	}

	present := make(map[string]bool, len(etags))
	extensions := imageExtensions()
	var listErr error
	for obj := range ix.client.ListObjects(ctx, ix.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			listErr = obj.Err
			break
		}
		if !extensions[strings.ToLower(path.Ext(obj.Key))] {
			continue
		}
		stats.Seen++
		present[obj.Key] = true
		if etags[obj.Key] == obj.ETag {
			continue
		}
		select {
		case work <- obj:
		case <-ctx.Done():
			listErr = ctx.Err()
		}
		if listErr != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	if listErr != nil {
		// A partial listing must not be mistaken for deletions.
		return stats, fmt.Errorf("list bucket: %w", listErr)
	}

	var gone []string
	for key := range etags {
		if !present[key] {
			gone = append(gone, key)
		}
	}
	if len(gone) > 0 {
		if err := ix.db.WithContext(ctx).Where("key IN ?", gone).Delete(&app.Frame{}).Error; err != nil {
			return stats, fmt.Errorf("remove deleted frames: %w", err)
		}
		stats.Removed = len(gone)
	}
	return stats, nil
}

func (ix *Indexer) indexObject(ctx context.Context, obj minio.ObjectInfo) error {
	frame := app.Frame{
		Key:          obj.Key,
		ETag:         obj.ETag,
		Size:         obj.Size,
		LastModified: obj.LastModified,
		IndexedAt:    time.Now(),
	}

	kw, err := ix.readHeader(ctx, obj)
	if err != nil {
		msg := err.Error()
		frame.IndexError = &msg
	} else {
		fillFrame(&frame, frameheader.FromKeywords(kw))
	}

	// Upsert on key so a changed object replaces its old row, including its
	// preview fields, which are left empty so the preview is rendered again.
	if dbErr := ix.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		UpdateAll: true,
	}).Create(&frame).Error; dbErr != nil {
		return fmt.Errorf("save frame: %w", dbErr)
	}
	return err
}

func (ix *Indexer) readHeader(ctx context.Context, obj minio.ObjectInfo) (frameheader.Keywords, error) {
	return ReadHeader(ctx, ix.client, ix.bucket, obj)
}

// ReadHeader reads an image's header keywords with ranged reads of the
// start of the object, growing the range until the header fits.
func ReadHeader(ctx context.Context, client *minio.Client, bucket string, obj minio.ObjectInfo) (frameheader.Keywords, error) {
	return readPrefixed(ctx, client, bucket, obj, frameheader.Parse)
}

// ReadCards reads an object's FITS keywords in order, with comments.
func ReadCards(ctx context.Context, client *minio.Client, bucket string, obj minio.ObjectInfo) ([]frameheader.Card, error) {
	return readPrefixed(ctx, client, bucket, obj, frameheader.ParseCards)
}

// readPrefixed parses an object's header from a ranged read, reading more
// when the header is longer than frameheader.PrefixSize.
func readPrefixed[T any](ctx context.Context, client *minio.Client, bucket string, obj minio.ObjectInfo, parse func([]byte) (T, error)) (T, error) {
	var zero T
	size := int64(frameheader.PrefixSize)
	for range 4 {
		prefix, err := readPrefix(ctx, client, bucket, obj.Key, min(size, obj.Size))
		if err != nil {
			return zero, err
		}
		v, err := parse(prefix)
		var more *frameheader.NeedMoreError
		if errors.As(err, &more) && int64(more.Total) <= obj.Size && int64(more.Total) > size {
			size = int64(more.Total)
			continue
		}
		return v, err
	}
	return zero, fmt.Errorf("header larger than %d bytes", size)
}

func readPrefix(ctx context.Context, client *minio.Client, bucket, key string, n int64) ([]byte, error) {
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(0, n-1); err != nil {
		return nil, err
	}
	r, err := client.GetObject(ctx, bucket, key, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, n))
}

func ptr(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

func fillFrame(dst *app.Frame, f frameheader.Frame) {
	dst.Type = f.Type
	dst.Object = f.Object
	dst.Filter = f.Filter
	dst.Exposure = ptr(f.Exposure)
	dst.Gain = ptr(f.Gain)
	dst.Offset = ptr(f.Offset)
	dst.SetTemp = ptr(f.SetTemp)
	dst.CCDTemp = ptr(f.CCDTemp)
	dst.BinX = ptr(f.BinX)
	dst.BinY = ptr(f.BinY)
	dst.Rotator = ptr(f.Rotator)
	dst.Camera = f.Camera
	dst.MountRA, dst.MountDec = ptr(f.RA), ptr(f.Dec)
	dst.PointingRead, dst.PointingWCS, dst.PointingRev = true, true, PointingRevision
	if f.HasDate {
		d := f.DateObs
		dst.DateObs = &d
	}
	if n, ok := f.Night(); ok {
		dst.Night = &n
	}
}
