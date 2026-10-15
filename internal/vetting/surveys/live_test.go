package surveys

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type dirStore struct{ root string }

func (d dirStore) path(key string) string { return filepath.Join(d.root, filepath.FromSlash(key)) }

func (d dirStore) Get(_ context.Context, key string) ([]byte, error) {
	b, err := os.ReadFile(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return b, err
}

func (d dirStore) Put(_ context.Context, key string, data []byte, _ string) error {
	p := d.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func (d dirStore) Sizes(_ context.Context, prefix string) (map[string]int64, error) {
	out := map[string]int64{}
	err := filepath.WalkDir(d.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(d.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			info, err := e.Info()
			if err != nil {
				return err
			}
			out[segment(prefix, key)] += info.Size()
		}
		return nil
	})
	return out, err
}

// TestLive pulls real survey data into SURVEYS_LIVE_DIR and logs sizes
// and timings. It talks to IRSA (and ATLAS when SURVEYS_ATLAS_TOKEN is
// set), so it only runs when asked.
func TestLive(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("SURVEYS_LIVE_DIR")
	if dir == "" {
		t.Skip("set SURVEYS_LIVE_DIR to pull real survey data")
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &Fetcher{
		Store: dirStore{root: filepath.Join(dir, "store")}, Prefix: DefaultPrefix,
		HTTP: &http.Client{Timeout: 30 * time.Minute}, WorkDir: work, ATLASToken: os.Getenv("SURVEYS_ATLAS_TOKEN"),
	}
	ctx := t.Context()
	fields := []Field{
		{Object: "Andromeda", RA: 10.68, Dec: 41.27, Radius: 0.9},
		{Object: "IC 1396 Panel 1", RA: 323.3, Dec: 58.6, Radius: 0.9},
		{Object: "North America Nebula Panel 2", RA: 314.7, Dec: 44.0, Radius: 0.9},
		{Object: "Cygnis Loop Panel 2", RA: 313.0, Dec: 29.77, Radius: 0.9},
	}
	all := fields
	if only := os.Getenv("SURVEYS_LIVE_FIELDS"); only != "" {
		var keep []Field
		for _, fl := range fields {
			if strings.Contains(only, FieldSlug(fl)) {
				keep = append(keep, fl)
			}
		}
		fields = keep
	}
	for _, fl := range fields {
		start := time.Now()
		if err := f.Prefetch(ctx, fl); err != nil {
			t.Fatalf("%s: %v", fl.Object, err)
		}
		m, err := f.ztfManifest(ctx, fl)
		if err != nil {
			t.Fatal(err)
		}
		var src, kept int64
		var objs, eps int
		for _, e := range m.Files {
			src += e.Source
			kept += e.Bytes
			objs += e.Objects
			eps += e.Epochs
		}
		t.Logf("%s: ZTF %s, %d files, %.2f GB at IRSA, %.3f GB kept, %d light curves, %d epochs, %s",
			fl.Object, m.Release, len(m.Files), float64(src)/1e9, float64(kept)/1e9, objs, eps, time.Since(start).Round(time.Second))
	}
	stars := []struct {
		field Field
		star  Target
	}{
		{all[1], ic1396Star()},
		{all[2], Target{GaiaID: 2161911134648201984, RA: 316.1618044989729, Dec: 43.32066480535969, G: 14.239096}},
		{all[0], Target{GaiaID: 381299950937609856, RA: 10.889334020094406, Dec: 41.66242846940393, PMRA: 2.1529643903735414, PMDec: -0.9269362575248431, G: 15.043443}},
	}
	for _, s := range stars {
		if !containsField(fields, s.field) {
			continue
		}
		for _, get := range []func(context.Context, Field, Target) Fetch{f.ZTF, f.NEOWISE, f.ATLAS} {
			start := time.Now()
			fe := get(ctx, s.field, s.star)
			var parts []string
			for _, x := range fe.Series {
				parts = append(parts, fmt.Sprintf("%s:%d", x.Band, len(x.Points)))
			}
			t.Logf("%d %s: %s %v (%s) in %s", s.star.GaiaID, fe.Survey, fe.State, parts, fe.Note, time.Since(start).Round(time.Millisecond))
		}
	}
	sizes, err := f.Sizes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for slug, s := range sizes {
		t.Logf("stored %s: %v", slug, s)
	}
}

func containsField(fs []Field, f Field) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}
