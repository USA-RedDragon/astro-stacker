package starcat_test

import (
	"context"
	"math"
	"path"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// TestMaizVariabilityMirror mirrors records of J/A+A/677/A137: six read
// from the start of CDS's catalog.dat.gz, and discovery 34867's star, Gaia
// DR3 151460926477704192, written in the ReadMe's format from VizieR's
// values for it. Two rows are stored at a time, so partitions have pieces
// from several stores.
func TestMaizVariabilityMirror(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := starcat.WithFlushRows(starcat.MaizVariability(), 2)
	lines := readLines(t, "maiz-var.dat")
	src, srv := newVarSource(t, c, lines, 403, len(lines))
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: c.Prefix, BaseURL: srv.URL + "/"}
	for range 2 {
		if err := c.Mirror(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if src.fetches.Load() != 1 {
		t.Fatalf("the file was fetched %d times for one ReadMe", src.fetches.Load())
	}
	v := starcat.NewVariables(b, c.Prefix)
	m, err := v.Manifest(ctx)
	if err != nil || m.Rows != int64(len(lines)) || m.Verified.IsZero() || m.Order != 6 {
		t.Fatalf("manifest %+v, %v", m, err)
	}
	stores := map[string]bool{}
	for _, p := range m.Pieces {
		stores[path.Base(p.Key)] = true
	}
	if len(stores) != 4 {
		t.Fatalf("pieces from stores %v, want four", stores)
	}
	keys, _ := b.List(ctx, c.Prefix+"/")
	if len(keys) != len(m.Pieces)+2 {
		t.Fatalf("%d objects for %d pieces and two manifests", len(keys), len(m.Pieces))
	}
	got, err := v.Cone(ctx, 68.96090, 26.37117, 3.0/3600, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("3 arcsec cone at discovery 34867: %+v, %v", got, err)
	}
	s := got[0]
	if s.ID != 151460926477704192 || s.Type != "VVV" || !starcat.MaizVariesInG(&s) || math.Abs(s.RA-68.96098370080)*3.6e6 > 0.5 ||
		math.Abs(s.Dec-26.37141657410)*3.6e6 > 0.5 || math.Abs(float64(s.Mag)-15.85615) > 1e-3 || math.Abs(float64(s.Amp)-0.025381886) > 3e-5 ||
		math.Abs(float64(s.Score)/(25.381886/0.277829)-1) > 4e-3 || !math.IsNaN(s.Period) || s.Name != "" {
		t.Fatalf("discovery 34867's star: %+v", s)
	}
	all, err := v.Cone(ctx, 0, 0, 180, nil)
	if err != nil || len(all) != len(lines) {
		t.Fatalf("whole sky: %d rows, %v", len(all), err)
	}
	checkMaizFlags(t, all)
}

func checkMaizFlags(t *testing.T, all []starcat.VarStar) {
	t.Helper()
	flags := map[int64]string{}
	for i := range all {
		flags[all[i].ID] = all[i].Type
		if all[i].ID == 423599125335337984 && starcat.MaizVariesInG(&all[i]) {
			t.Fatalf("a star flagged NNN varies: %+v", all[i])
		}
	}
	if flags[424248627458872448] != "VVB" || flags[424246153558429952] != "MBN" || flags[423305074689649792] != "VMV" {
		t.Fatalf("flags %v", flags)
	}
}

func TestMaizVariabilityMirrorLeavesNothingFromABadFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := starcat.WithFlushRows(starcat.MaizVariability(), 2)
	lines := readLines(t, "maiz-var.dat")
	last := lines[len(lines)-1]
	lines[len(lines)-1] = last[:297] + "NXN" + last[300:]
	_, srv := newVarSource(t, c, lines, 403, len(lines))
	b := &starcat.MemBucket{}
	err := c.Mirror(ctx, starcat.MirrorOptions{Bucket: b, Prefix: c.Prefix, BaseURL: srv.URL + "/"})
	if err == nil || !strings.Contains(err.Error(), "line 7") {
		t.Fatalf("got %v, want line 7 refused", err)
	}
	if keys, _ := b.List(ctx, c.Prefix+"/"); len(keys) != 0 {
		t.Fatalf("stored %v from a bad file", keys)
	}
}
