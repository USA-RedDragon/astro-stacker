package starcat_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

func ecsv(stars []starcat.GaiaStar) []byte {
	cols := ecsvColumns()
	var b strings.Builder
	names := make([]string, len(cols))
	for i, c := range cols {
		fmt.Fprintf(&b, "# - \n#   name: %s\n#   datatype: %s\n", c[0], c[1])
		names[i] = c[0]
	}
	b.WriteString(strings.Join(names, ",") + "\n")
	for _, s := range stars {
		fmt.Fprintf(&b, "%d,2016.0,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,\"NOT_AVAILABLE\"\n", s.SourceID, s.RA, s.RAError, s.Dec, s.DecError,
			s.Parallax, s.ParallaxError, s.PMRA, s.PMRAError, s.PMDec, s.PMDecError, s.RUWE, s.G, s.BP, s.RP)
	}
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write([]byte(b.String()))
	_ = w.Close()
	return z.Bytes()
}

func TestMirrorGaiaResumesAndWritesAManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(7, 8))
	files := map[string][]byte{}
	var all []starcat.GaiaStar
	var sums strings.Builder
	for i, f := range []string{"GaiaSource_000000-003111.csv.gz", "GaiaSource_003112-005263.csv.gz", "GaiaSource_005264-006601.csv.gz"} {
		stars := randomStars(2000, rng, float64(40+i), float64(i), 1)
		for j := range stars {
			stars[j].Variable = starcat.VariableNotAvailable
		}
		all = append(all, stars...)
		files[f] = ecsv(stars)
		h := md5.Sum(files[f])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), f)
	}
	sums.WriteString("2b916a83bda84c24f18fc947104d4d40  _MD5SUM.txt\n")
	files["_MD5SUM.txt"] = []byte(sums.String())
	var gets atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		b, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name != "_MD5SUM.txt" {
			gets.Add(1)
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: "gaia", BaseURL: srv.URL + "/", Workers: 2, Limit: 1, Verify: true}
	if err := starcat.MirrorGaia(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, "gaia/manifest.json.zst"); err == nil {
		t.Fatal("a manifest was written after one file of three")
	}
	o.Limit = 0
	if err := starcat.MirrorGaia(ctx, o); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 3 {
		t.Fatalf("%d downloads, want 3: a stored file was fetched again", gets.Load())
	}
	if err := starcat.MirrorGaia(ctx, o); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 3 {
		t.Fatalf("%d downloads after the mirror was complete, want 3", gets.Load())
	}
	g := starcat.NewGaia(b, "gaia")
	m, err := g.Manifest(ctx)
	if err != nil || m.Rows != int64(len(all)) || len(m.Inputs) != 3 || m.Verified.IsZero() {
		t.Fatalf("manifest %+v, %v", m, err)
	}
	got, err := g.Cone(ctx, 41, 1, 10, nil)
	if err != nil || len(got) != len(all) {
		t.Fatalf("%d stars, want %d, %v", len(got), len(all), err)
	}
	bySource(all)
	for i := range all {
		if !sameStar(got[i], all[i]) {
			t.Fatalf("row %d: %+v, want %+v", i, got[i], all[i])
		}
	}
}

func TestMirrorGaiaRejectsABadChecksum(t *testing.T) {
	t.Parallel()
	body := ecsv(randomStars(10, rand.New(rand.NewPCG(9, 9)), 10, 10, 1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: "gaia", BaseURL: srv.URL + "/"}
	err := starcat.StoreGaiaFile(context.Background(), o, "GaiaSource_000000-003111.csv.gz", "00000000000000000000000000000000")
	if err == nil || !strings.Contains(err.Error(), "MD5") {
		t.Fatalf("a file failing its MD5 gave %v", err)
	}
	if keys, _ := b.List(context.Background(), "gaia/"); len(keys) != 0 {
		t.Fatalf("stored %v from a file failing its MD5", keys)
	}
	h := md5.Sum(body)
	if err := starcat.StoreGaiaFile(context.Background(), o, "GaiaSource_000000-003111.csv.gz", hex.EncodeToString(h[:])); err != nil {
		t.Fatal(err)
	}
	if keys, _ := b.List(context.Background(), "gaia/done/"); len(keys) != 1 {
		t.Fatalf("done markers %v", keys)
	}
}
