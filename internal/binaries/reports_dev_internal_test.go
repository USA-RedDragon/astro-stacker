//go:build binariesdev

package binaries

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
)

// TestStoredReportFlags prints the flags stored vet reports raise on
// their own, before any survey facts: BIN_REPORTS names a directory of
// report JSON files, one per source.
func TestStoredReportFlags(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("BIN_REPORTS")
	if dir == "" {
		t.Skip("set BIN_REPORTS to a directory of stored vet reports")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var r vetting.Report
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if !Periodic(&r) {
			t.Logf("%d: no periodic ephemeris", r.SourceID)
			continue
		}
		f := Work(&r, vetting.StarData{}, nil, time.Time{})
		flags := Flags(&r, f)
		ids := make([]string, 0, len(flags))
		for _, fl := range flags {
			ids = append(ids, fl.ID+": "+fl.Why)
		}
		ty, words := TypeOf(f.Ephemeris)
		t.Logf("%d P=%.6f doubled=%v type=%s (%s) flags=%q", r.SourceID, f.Ephemeris.Period, f.Doubled, ty, words, ids)
	}
}
