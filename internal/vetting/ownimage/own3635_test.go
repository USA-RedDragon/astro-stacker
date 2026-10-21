package ownimage

import (
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/klauspost/compress/zstd"
)

// readOwnCase reads the subs and fits the vet measured at one spot: per
// sub its filter, night, start, exposure, the star's flux and error and
// each comparison star's flux (negative when its fit failed).
func readOwnCase(t *testing.T, path string) ([]Sub, []*rec) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	subs := make([]Sub, 0, len(rows)-1)
	recs := make([]*rec, 0, len(rows)-1)
	for _, r := range rows[1:] {
		id, _ := strconv.Atoi(r[0])
		night, err := time.Parse(time.DateOnly, r[2])
		if err != nil {
			t.Fatal(err)
		}
		start, err := time.Parse(time.RFC3339Nano, r[3])
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, Sub{FrameID: id, Filter: r[1], Night: night, Start: start, Exposure: num(r[4])})
		rc := &rec{flux: num(r[5]), ferr: num(r[6]), fluxOK: true}
		for _, c := range strings.Split(r[7], ";") {
			rc.comps = append(rc.comps, num(c))
		}
		recs = append(recs, rc)
	}
	return subs, recs
}

// TestOwnCurve3635 is source 3635 (Gaia DR3 4315093031714485376), an
// eclipsing binary flagged fainter on 2026-10-13 and brighter on
// 2026-10-10. Its target was shot in Luminance, Red and Green only on
// those two nights; the nights not flagged have Blue alone. The curve
// dropped every Luminance, Red and Green frame for want of a level on
// nights not flagged, so the flagged night kept one Blue frame and the
// own checks said it could not be measured. The night's 15 frames must
// be measured, and its Luminance frames show the fade into Min II.
func TestOwnCurve3635(t *testing.T) {
	t.Parallel()
	subs, recs := readOwnCase(t, "testdata/own3635.csv.zst")
	d10 := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	d13 := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	up, down := -0.0698, 0.1111
	sp := &spotPlan{
		spot: Spot{Nights: []Night{
			{Night: d10, Group: "broadband", Sign: 1, DeltaMag: &up},
			{Night: d13, Group: "broadband", Sign: -1, DeltaMag: &down},
		}},
		flagged:  map[string]bool{nightKey(d10): true, nightKey(d13): true},
		star:     &Star{},
		recs:     recs,
		comps:    make([]*starModel, len(recs[0].comps)),
		fadeSign: 1,
	}
	e := buildEnsemble(subs, sp)
	c := buildCurve(subs, sp, e)
	summarize(c, sp)
	byFilter := map[string][]Point{}
	for _, p := range c.Points {
		if p.Night == nightKey(d13) {
			byFilter[p.Filter] = append(byFilter[p.Filter], p)
		}
	}
	n := 0
	for _, ps := range byFilter {
		n += len(ps)
	}
	if n != 15 {
		t.Fatalf("measured %d frames of 2026-10-13, want 15", n)
	}
	lum := byFilter[broadbandClear]
	if len(lum) != 5 {
		t.Fatalf("%d Luminance frames on 2026-10-13, want 5", len(lum))
	}
	early, late := (lum[0].DMag+lum[1].DMag)/2, (lum[2].DMag+lum[3].DMag+lum[4].DMag)/3
	if late-early < 0.1 {
		t.Errorf("Luminance faded %.3f mag into Min II, want at least 0.1", late-early)
	}
	if lc := ownLC(sp, c); lc.Status == check.NotRun {
		t.Errorf("own light curve skipped: %s", lc.Evidence)
	}
	if ch := ownChange(sp, c); ch.Status == check.NotRun {
		t.Errorf("own change skipped: %s", ch.Evidence)
	}
}
