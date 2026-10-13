package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/ninapath"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// keyChunk bounds how many keys go in one IN list.
const keyChunk = 1000

// IndexRecent indexes new and changed images in the folders of targets
// imaged lately, measures the new lights' starlight, and returns how many
// frames it indexed. It lists those few folders rather than the bucket, and
// never removes frames: the bucket scan does that.
func (ix *Indexer) IndexRecent(ctx context.Context) (int, error) {
	prefixes, err := ix.recentPrefixes(ctx)
	if err != nil {
		return 0, err
	}
	extensions := imageExtensions()
	var objs []minio.ObjectInfo
	for _, prefix := range prefixes {
		for obj := range ix.client.ListObjects(ctx, ix.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				return 0, fmt.Errorf("list %q: %w", prefix, obj.Err)
			}
			if extensions[strings.ToLower(path.Ext(obj.Key))] {
				objs = append(objs, obj)
			}
		}
	}
	changed, err := ix.changed(ctx, objs)
	if err != nil {
		return 0, err
	}
	var keys []string
	for _, obj := range changed {
		if err := ix.indexObject(ctx, obj); err != nil {
			if ctx.Err() != nil {
				return len(keys), ctx.Err()
			}
			slog.Warn("Could not index frame", "key", obj.Key, "error", err)
			continue
		}
		metrics.FramesIndexed.Inc()
		keys = append(keys, obj.Key)
	}
	if len(keys) == 0 {
		return 0, nil
	}
	ix.Events.Publish(events.Event{Type: events.TypeFrames})
	ix.Wake.Notify()
	n, err := ix.measurePhotometry(ctx, photometryPending(ix.db.WithContext(ctx)).Where("key IN ?", keys))
	if err != nil {
		return len(keys), fmt.Errorf("measure starlight: %w", err)
	}
	if n > 0 {
		ix.Wake.Notify()
	}
	return len(keys), nil
}

// changed returns the objects not indexed yet, or indexed with another ETag.
func (ix *Indexer) changed(ctx context.Context, objs []minio.ObjectInfo) ([]minio.ObjectInfo, error) {
	etags := make(map[string]string, len(objs))
	for chunk := range slices.Chunk(objs, keyChunk) {
		keys := make([]string, len(chunk))
		for i, o := range chunk {
			keys[i] = o.Key
		}
		var rows []known
		if err := ix.db.WithContext(ctx).Model(&app.Frame{}).Select("key", "e_tag").
			Where("key IN ?", keys).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load indexed frames: %w", err)
		}
		for _, r := range rows {
			etags[r.Key] = r.ETag
		}
	}
	var out []minio.ObjectInfo
	for _, o := range objs {
		if etag, ok := etags[o.Key]; !ok || etag != o.ETag {
			out = append(out, o)
		}
	}
	return out, nil
}

// recentPrefixes are the top folders of lights taken in the last
// RecentWindow, and of the targets RecentTargets names.
func (ix *Indexer) recentPrefixes(ctx context.Context) ([]string, error) {
	var keys []string
	if err := ix.db.WithContext(ctx).Model(&app.Frame{}).
		Where("type = ? AND date_obs > ?", "LIGHT", time.Now().Add(-RecentWindow)).
		Pluck("key", &keys).Error; err != nil {
		return nil, fmt.Errorf("load recent lights: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir+"/")
	}
	for _, k := range keys {
		if dir, _, ok := strings.Cut(k, "/"); ok {
			add(dir)
		}
	}
	if ix.RecentTargets != nil {
		names, err := ix.RecentTargets(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.Warn("Could not read recently imaged targets", "error", err)
		}
		for _, n := range names {
			add(n)
			add(ninapath.Folder(n))
		}
	}
	slices.Sort(out)
	return out, nil
}

const (
	ticksThreshold  = 600000000000000000
	ticksUnixOffset = 621355968000000000
	ticksPerSecond  = 10000000
)

// ScheduledTargets returns a RecentTargets that reads, from Target
// Scheduler's database, the targets it saved an image of in the last
// RecentWindow. Their first light of a night is indexed as soon as it is
// uploaded, before any light of theirs is in the frames table.
func ScheduledTargets(sched *gorm.DB) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		if !sched.Migrator().HasTable("acquiredimage") {
			return nil, nil
		}
		since := time.Now().Add(-RecentWindow).Unix()
		var names []string
		err := sched.WithContext(ctx).Table("acquiredimage a").
			Joins(`JOIN target t ON t."Id" = a."targetId"`).
			Where(`CAST(a.acquireddate AS BIGINT) >= ? AND (CAST(a.acquireddate AS BIGINT) < ? OR CAST(a.acquireddate AS BIGINT) >= ?)`,
				since, int64(ticksThreshold), since*ticksPerSecond+ticksUnixOffset).
			Distinct("t.name").Pluck("t.name", &names).Error
		return names, err
	}
}
