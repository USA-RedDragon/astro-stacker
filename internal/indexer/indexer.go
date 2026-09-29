// Package indexer keeps the frames table in step with the object store: it
// lists the bucket, reads the header of every new or changed image with a
// ranged GET, and records what calibration matching needs.
package indexer

import (
	"context"
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

var imageExtensions = map[string]bool{".xisf": true, ".fits": true, ".fit": true, ".fts": true}

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

// Run indexes the bucket every interval until ctx is cancelled.
func (ix *Indexer) Run(ctx context.Context, interval time.Duration) {
	for {
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
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// BackfillPointing reads where the mount pointed for lights indexed before
// frames recorded it, a ranged read of each header.
func (ix *Indexer) BackfillPointing(ctx context.Context) (int, error) {
	var frames []app.Frame
	if err := ix.db.WithContext(ctx).Select("id", "key", "size").
		// Frames indexed before the column existed have it NULL.
		Where("type = ? AND index_error IS NULL AND (pointing_read IS NULL OR pointing_read = ?)", "LIGHT", false).
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
				kw, err := ReadHeader(ctx, ix.client, ix.bucket, minio.ObjectInfo{Key: f.Key, Size: f.Size})
				if err != nil {
					if ctx.Err() == nil {
						slog.Debug("Could not read header for pointing", "key", f.Key, "error", err)
					}
					continue
				}
				h := frameheader.FromKeywords(kw)
				if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).Updates(map[string]any{
					"mount_ra": ptr(h.RA), "mount_dec": ptr(h.Dec), "pointing_read": true,
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
	obj, err := ix.client.GetObject(ctx, ix.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return measure.Result{}, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		return measure.Result{}, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return measure.Result{}, err
	}
	return measure.Sub(im), nil
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
	var listErr error
	for obj := range ix.client.ListObjects(ctx, ix.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			listErr = obj.Err
			break
		}
		if !imageExtensions[strings.ToLower(path.Ext(obj.Key))] {
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
	dst.PointingRead = true
	if f.HasDate {
		d := f.DateObs
		dst.DateObs = &d
	}
	if n, ok := f.Night(); ok {
		dst.Night = &n
	}
}
