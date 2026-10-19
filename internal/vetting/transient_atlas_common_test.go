package vetting

import (
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// TestATLASCommonLight54893 is the ATLAS forced photometry at 54893 in
// the Crab Nebula, 6.6 px from a G 14.1 star, with the quality cuts the
// vet applies. One of the four frames of 2026-02-06 gave 176 microjansky
// at chi/N 17, which put the night at signal to noise 8, while the user's
// own frames showed nothing. 131 of the 928 ATLAS nights at the spot
// reach that level, so ATLAS must not confirm the new light.
func TestATLASCommonLight54893(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/atlas_54893.csv.zst")
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(string(text))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var rows []surveys.ATLASRow
	for _, r := range recs[1:] {
		num := func(s string) float64 {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		rows = append(rows, surveys.ATLASRow{MJD: num(r[0]), UJy: num(r[1]), DUJy: num(r[2]), Filter: r[3], Obs: r[4]})
	}
	fx := transientFacts{flagged: []time.Time{nightDate("2026-02-05")}, atlas: surveys.Fetch{State: surveys.Done}, rows: rows}
	c := atlasCheckTransient(fx)
	t.Logf("%s: %s", c.Status, c.Evidence)
	if c.Status == check.Pass || !strings.Contains(c.Evidence, "other ATLAS nights at this spot reach that too") {
		t.Errorf("ATLAS confirmed light it measures here on many nights: %s %s", c.Status, c.Evidence)
	}
}
