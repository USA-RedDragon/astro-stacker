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

const testPrefix = "gaia"

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
	o := starcat.MirrorOptions{Bucket: b, Prefix: testPrefix, BaseURL: srv.URL + "/", Workers: 2, Limit: 1, Verify: true}
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
	g := starcat.NewGaia(b, testPrefix)
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

func TestMirrorGaiaStoresAgainAFileWhosePieceIsDamaged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(11, 12))
	files := map[string][]byte{}
	var sums strings.Builder
	names := []string{"GaiaSource_000000-003111.csv.gz", "GaiaSource_003112-005263.csv.gz"}
	for i, f := range names {
		files[f] = ecsv(randomStars(500, rng, float64(100+10*i), 0, 1))
		h := md5.Sum(files[f])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), f)
	}
	files["_MD5SUM.txt"] = []byte(sums.String())
	var gets atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "_MD5SUM.txt" {
			gets.Add(1)
		}
		_, _ = w.Write(files[name])
	}))
	defer srv.Close()
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: testPrefix, BaseURL: srv.URL + "/", Workers: 1, Limit: 1, Verify: true}
	if err := starcat.MirrorGaia(ctx, o); err != nil {
		t.Fatal(err)
	}
	keys, _ := b.List(ctx, "gaia/order6/")
	data, _ := b.Get(ctx, keys[0])
	data[len(data)/2] ^= 1
	_ = b.Put(ctx, keys[0], data)
	o.Limit = 0
	if err := starcat.MirrorGaia(ctx, o); err == nil {
		t.Fatal("a damaged piece passed verification")
	}
	if err := starcat.MirrorGaia(ctx, o); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 3 {
		t.Fatalf("%d downloads, want 3: the damaged file once more", gets.Load())
	}
	if _, err := starcat.NewGaia(b, testPrefix).Cone(ctx, 100, 0, 2, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorGaiaRejectsABadChecksum(t *testing.T) {
	t.Parallel()
	body := ecsv(randomStars(10, rand.New(rand.NewPCG(9, 9)), 10, 10, 1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: testPrefix, BaseURL: srv.URL + "/"}
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

func TestMirrorVSXSwapsVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lines := []string{
		vsxLine(map[int]string{1: "       1", 10: "V1", 41: "0", 43: "010.00000", 53: "+41.00000", 63: "M", 96: " 10.000", 106: "V"}),
		vsxLine(map[int]string{1: "       2", 10: "V2", 41: "1", 43: "200.00000", 53: "-30.00000", 63: "EA", 159: "      1.250000000000"}),
	}
	dat := strings.Join(lines, "\n") + "\n"
	version := "2026-10-05"
	var dats atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "ReadMe":
			fmt.Fprintf(w, "vsx.dat        209 %d  Variable Star indeX, Version %s\n", len(lines), version)
		case "vsx.dat":
			dats.Add(1)
			_, _ = w.Write([]byte(dat))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	b := &starcat.MemBucket{}
	o := starcat.MirrorOptions{Bucket: b, Prefix: "vsx", BaseURL: srv.URL + "/"}
	for range 2 {
		if err := starcat.MirrorVSX(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if dats.Load() != 1 {
		t.Fatalf("vsx.dat fetched %d times for one version", dats.Load())
	}
	got, err := starcat.NewVSX(b, "vsx").Cone(ctx, 10, 41, 1, nil)
	if err != nil || len(got) != 1 || got[0].Name != "V1" || got[0].Max != 10 {
		t.Fatalf("%+v, %v", got, err)
	}
	version = "2026-11-02"
	if err := starcat.MirrorVSX(ctx, o); err != nil {
		t.Fatal(err)
	}
	keys, _ := b.List(ctx, "vsx/")
	for _, k := range keys {
		if strings.Contains(k, "2026-10-05") {
			t.Fatalf("old version left: %s", k)
		}
	}
	m, err := starcat.NewVSX(b, "vsx").Manifest(ctx)
	if err != nil || m.Version != version || m.Rows != 2 {
		t.Fatalf("manifest %+v, %v", m, err)
	}
}
