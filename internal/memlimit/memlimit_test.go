package memlimit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/memlimit"
)

func TestLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, c := range []struct {
		body string
		want int64
	}{
		{"8589934592\n", 8589934592},
		{"max\n", 0},
		{"9223372036854771712\n", 0},
		{"", 0},
	} {
		p := filepath.Join(dir, "memory.max")
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := memlimit.Limit(p); got != c.want {
			t.Errorf("%q: %d, want %d", c.body, got, c.want)
		}
	}
	if got := memlimit.Limit(filepath.Join(dir, "none")); got != 0 {
		t.Errorf("a missing file: %d", got)
	}
}
