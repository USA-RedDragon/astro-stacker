package stacking

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrimFileCacheDeletesOldestUnused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name string, age time.Duration) string {
		f := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, make([]byte, 1024), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(f, when, when); err != nil {
			t.Fatal(err)
		}
		return f
	}
	old := write("masters/old.fit", 10*time.Hour)
	ref := write("references/ref.fit", 5*time.Hour)
	recent := write("masters/recent.fit", 10*time.Minute)
	other := write("batch-1/sub.fit", 20*time.Hour)

	p := &Pipeline{workDir: dir, opts: PipelineOptions{FileCacheFree: 1 << 62}}
	p.trimFileCache()
	for _, f := range []string{old, ref} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s kept", f)
		}
	}
	for _, f := range []string{recent, other} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s deleted", f)
		}
	}

	keep := write("masters/keep.fit", 10*time.Hour)
	p.opts.FileCacheFree = 1
	p.trimFileCache()
	if _, err := os.Stat(keep); err != nil {
		t.Error("deleted with enough free space")
	}
}
