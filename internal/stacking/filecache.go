package stacking

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// fileCacheInUse is how recently a cached file was used for it to be kept.
const fileCacheInUse = 2 * time.Hour

func touchCached(file string) {
	now := time.Now()
	_ = os.Chtimes(file, now, now)
}

func (p *Pipeline) runFileCacheTrim(ctx context.Context) {
	for {
		p.trimFileCache()
		if !p.pause(ctx, 5*time.Minute) {
			return
		}
	}
}

// trimFileCache deletes the least recently used cached masters and
// references until the work volume has FileCacheFree bytes free.
func (p *Pipeline) trimFileCache() {
	free, err := freeSpace(p.workDir)
	if err != nil || free >= p.opts.FileCacheFree {
		return
	}
	type cached struct {
		path string
		size int64
		used time.Time
	}
	var files []cached
	for _, d := range []string{"masters", "references"} {
		_ = filepath.WalkDir(filepath.Join(p.workDir, d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return fs.SkipDir
			}
			if e.IsDir() {
				return nil
			}
			if info, err := e.Info(); err == nil {
				files = append(files, cached{path, info.Size(), info.ModTime()})
			}
			return nil
		})
	}
	slices.SortFunc(files, func(a, b cached) int { return a.used.Compare(b.used) })
	cutoff := time.Now().Add(-fileCacheInUse)
	var n int
	var freed int64
	for _, f := range files {
		if free+freed >= p.opts.FileCacheFree || f.used.After(cutoff) {
			break
		}
		if os.Remove(f.path) == nil {
			n++
			freed += f.size
		}
	}
	if n > 0 {
		slog.Info("Deleted cached masters and references to free the work volume", "files", n, "bytes", freed)
	}
}
