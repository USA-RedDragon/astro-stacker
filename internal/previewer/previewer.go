// Package previewer renders an auto-stretched JPEG for every light in the
// frames table and stores it in the processed bucket, newest nights first.
package previewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// batchSize bounds how many frames are claimed per pass, so newly indexed
// subs don't wait behind the whole backlog.
const batchSize = 64

type Previewer struct {
	client      *minio.Client
	source      string
	dest        string
	db          *gorm.DB
	concurrency int
	opts        preview.Options
}

func New(client *minio.Client, source, dest string, db *gorm.DB, concurrency int, opts preview.Options) *Previewer {
	return &Previewer{client: client, source: source, dest: dest, db: db, concurrency: max(1, concurrency), opts: opts}
}

// Key returns where a frame's preview is stored.
func Key(frameKey string) string {
	return "previews/" + strings.TrimSuffix(frameKey, path.Ext(frameKey)) + ".jpg"
}

// Run renders previews until ctx is cancelled, sleeping interval when idle.
func (p *Previewer) Run(ctx context.Context, interval time.Duration) {
	for {
		n, err := p.RenderBatch(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Rendering previews failed", "error", err)
		}
		if n > 0 && err == nil {
			// More may be waiting; go straight to the next batch.
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// RenderBatch renders previews for up to batchSize lights that lack one and
// returns how many it attempted.
func (p *Previewer) RenderBatch(ctx context.Context) (int, error) {
	var frames []app.Frame
	if err := p.db.WithContext(ctx).
		Where("type = ? AND index_error IS NULL AND preview_key IS NULL AND preview_error IS NULL", "LIGHT").
		Order("date_obs DESC").Limit(batchSize).Find(&frames).Error; err != nil {
		return 0, fmt.Errorf("load frames: %w", err)
	}
	if len(frames) == 0 {
		return 0, nil
	}

	start := time.Now()
	work := make(chan app.Frame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for range p.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				if err := p.renderOne(ctx, f); err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					slog.Warn("Could not render preview", "key", f.Key, "error", err)
				}
			}
		}()
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
	slog.Info("Rendered previews", "frames", len(frames), "failed", failed, "duration", time.Since(start).Round(time.Millisecond))
	return len(frames), ctx.Err()
}

func (p *Previewer) renderOne(ctx context.Context, f app.Frame) error {
	jpg, err := p.render(ctx, f.Key)
	now := time.Now()
	updates := map[string]any{"preview_at": now}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := err.Error()
		updates["preview_error"] = msg
	} else {
		key := Key(f.Key)
		if _, putErr := p.client.PutObject(ctx, p.dest, key, bytes.NewReader(jpg), int64(len(jpg)),
			minio.PutObjectOptions{ContentType: "image/jpeg", CacheControl: "private, max-age=86400"}); putErr != nil {
			// Upload failures are transient; leave the row untouched to retry.
			return fmt.Errorf("upload: %w", putErr)
		}
		updates["preview_key"] = key
	}
	// Only update the row if the frame hasn't changed meanwhile.
	if dbErr := p.db.WithContext(ctx).Model(&app.Frame{}).
		Where("id = ? AND e_tag = ?", f.ID, f.ETag).Updates(updates).Error; dbErr != nil {
		return fmt.Errorf("save preview: %w", dbErr)
	}
	return err
}

func (p *Previewer) render(ctx context.Context, key string) ([]byte, error) {
	obj, err := p.client.GetObject(ctx, p.source, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, err
	}
	return preview.Render(im, p.opts)
}

// Signer presigns preview URLs for browsers.
type Signer struct {
	client *minio.Client
	bucket string
	ttl    time.Duration
}

func NewSigner(publicClient *minio.Client, bucket string, ttl time.Duration) *Signer {
	return &Signer{client: publicClient, bucket: bucket, ttl: ttl}
}

// URL presigns a GET for a preview key. It makes no network call when the
// client has a region set.
func (s *Signer) URL(ctx context.Context, previewKey string) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, previewKey, s.ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
