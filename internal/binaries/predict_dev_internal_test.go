//go:build binariesdev

package binaries

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
)

// TestStoredReportEclipses prints the eclipses stored vet reports predict
// from BIN_FROM (RFC 3339) for 30 days, in US Central: BIN_REPORTS holds
// the reports and BIN_POSITIONS a JSON map of source id to [ra, dec].
func TestStoredReportEclipses(t *testing.T) {
	t.Parallel()
	dir, posPath := os.Getenv("BIN_REPORTS"), os.Getenv("BIN_POSITIONS")
	from, err := time.Parse(time.RFC3339, os.Getenv("BIN_FROM"))
	if dir == "" || posPath == "" || err != nil {
		t.Skip("set BIN_REPORTS, BIN_POSITIONS and BIN_FROM")
	}
	raw, err := os.ReadFile(posPath)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string][2]float64{}
	if err := json.Unmarshal(raw, &pos); err != nil {
		t.Fatal(err)
	}
	loc := chicago(t)
	for id, p := range pos {
		b, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var r vetting.Report
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if !Periodic(&r) {
			continue
		}
		e, _ := adopt(&r)
		for _, x := range Predict(e, deeper(e), p[0], p[1], from, from.AddDate(0, 0, 30)) {
			day, clock, zone := Labels(x.Mid, loc)
			t.Logf("%s %s %s %s %-9s ±%.0f min, lasts %.1f h", id, day, clock, zone, x.Kind, x.Err.Minutes(), x.Duration.Hours())
		}
	}
}
