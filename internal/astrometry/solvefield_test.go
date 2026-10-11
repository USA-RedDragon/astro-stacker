package astrometry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The index set covers 11' to 6.8° quads: 5205/5206 LITE all sky (48
// healpixes each) and 4108-4115, about 1.1 GB.
func TestIndexFiles(t *testing.T) {
	t.Parallel()
	files, err := IndexFiles()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	var total int64
	counts := map[string]int{}
	for _, f := range files {
		if names[f.Name] {
			t.Errorf("%s listed twice", f.Name)
		}
		names[f.Name] = true
		total += f.Size
		if len(f.SHA256) != 64 || !strings.HasPrefix(f.URL, "https://") {
			t.Errorf("%s: sha256 %q url %q", f.Name, f.SHA256, f.URL)
		}
		counts[f.Name[:10]]++
	}
	if counts["index-5205"] != 48 || counts["index-5206"] != 48 {
		t.Errorf("5200 LITE healpixes: %v", counts)
	}
	for scale := 4108; scale <= 4115; scale++ {
		if !names[fmt.Sprintf("index-%d.fits", scale)] {
			t.Errorf("index-%d.fits missing", scale)
		}
	}
	if total != 1112166720 {
		t.Errorf("total %d bytes", total)
	}
	if id := IndexSetID(); len(id) != 12 {
		t.Errorf("index set id %q", id)
	}
}

// Good files are kept, a damaged one is downloaded again, and a download
// that doesn't match its checksum is not kept.
func TestEnsureFiles(t *testing.T) {
	t.Parallel()
	content := map[string][]byte{"/a.fits": []byte("index a"), "/b.fits": []byte("index b"), "/c.fits": []byte("tampered")}
	var served []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = append(served, r.URL.Path)
		_, _ = w.Write(content[r.URL.Path])
	}))
	defer srv.Close()
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	files := []IndexFile{
		{Name: "a.fits", URL: srv.URL + "/a.fits", Size: 7, SHA256: sum("index a")},
		{Name: "b.fits", URL: srv.URL + "/b.fits", Size: 7, SHA256: sum("index b")},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.fits"), []byte("index a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.fits"), []byte("index x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureFiles(context.Background(), srv.Client(), dir, files); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(served, []string{"/b.fits"}) {
		t.Errorf("downloaded %v, want only b", served)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.fits")); string(b) != "index b" {
		t.Errorf("b.fits holds %q", b)
	}
	bad := []IndexFile{{Name: "c.fits", URL: srv.URL + "/c.fits", Size: 8, SHA256: sum("original")}}
	if err := ensureFiles(context.Background(), srv.Client(), dir, bad); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("tampered download: %v", err)
	}
	for _, name := range []string{"c.fits", "c.fits.part"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", name, err)
		}
	}
}

func TestArgs(t *testing.T) {
	t.Parallel()
	s := &Solver{CPUSeconds: 90}
	args := strings.Join(s.Args("c.cfg", "out", "m.fit", Hint{RA: 179.4, Dec: 53.37, Radius: 5, ScaleLow: 0.462, ScaleHigh: 0.924, Width: 2048}), " ")
	for _, want := range []string{"--config c.cfg", "--dir out", "--fits-image", "--cpulimit 90", "--scale-units arcsecperpix --scale-low 0.4620 --scale-high 0.9240",
		"--ra 179.40000 --dec 53.37000 --radius 5.00", "--no-remove-lines", "--uniformize 0"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--downsample") || !strings.HasSuffix(args, " m.fit") {
		t.Errorf("args %q", args)
	}
	blind := strings.Join(s.Args("c.cfg", "out", "m.fit", Hint{RA: math.NaN(), Dec: math.NaN(), Width: 6248}), " ")
	if strings.Contains(blind, "--ra") || !strings.Contains(blind, "--scale-units degwidth --scale-low 0.15 --scale-high 10") || !strings.Contains(blind, "--downsample 2") {
		t.Errorf("blind args %q", blind)
	}
}

const testIndexDir = "/astrometry"

const wcsHeader = "SIMPLE  = T\nWCSAXES = 2\nCTYPE1  = 'RA---TAN-SIP'\nCTYPE2  = 'DEC--TAN-SIP'\nCRVAL1  = 179.382960506\n" +
	"CRVAL2  = 53.3176209036\nCRPIX1  = 1090.14\nCRPIX2  = 1331.68\nCD1_1   = -0.000183364001899\nCD1_2   = 5.26102171388E-06\n" +
	"CD2_1   = -5.64647517388E-06\nCD2_2   = -0.000183131573751\nA_ORDER = 2\nIMAGEW  = 2048\nEND\n"

// fakeSolveField writes a script that answers like solve-field: it records
// its arguments and the config, and writes body as <file>.wcs in --dir.
func fakeSolveField(t *testing.T, body string, sleep int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	header := ""
	for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		header += fmt.Sprintf("%-80s", line)
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" > %[1]q
out=; cfg=
while [ $# -gt 1 ]; do
  case "$1" in --dir) out=$2 ;; --config) cfg=$2 ;; esac
  shift
done
cat "$cfg" >> %[1]q
sleep %[2]d
if [ -n %[3]q ]; then printf '%%s' %[3]q > "$out/$(basename "$1" .fit).wcs"; fi
echo "Field 1: done"
`, record, sleep, header)
	cmd := filepath.Join(dir, "solve-field")
	if err := os.WriteFile(cmd, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmd, record
}

func TestSolve(t *testing.T) {
	t.Parallel()
	cmd, record := fakeSolveField(t, wcsHeader, 0)
	s := &Solver{Command: cmd, IndexDir: testIndexDir, CPUSeconds: 60, Timeout: time.Minute}
	if _, err := s.Solve(context.Background(), t.TempDir(), "master.fit", Hint{}); err == nil {
		t.Error("solved before the index files were ready")
	}
	s.MarkReady()
	if s.Fingerprint() != "astrometry.net:"+IndexSetID() {
		t.Errorf("fingerprint %q", s.Fingerprint())
	}
	work := t.TempDir()
	cards, err := s.Solve(context.Background(), work, "master.fit", Hint{RA: 179.4, Dec: 53.37, Radius: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cards {
		got[c.Name] = c.Value
	}
	if got["CRVAL1"] != "179.382960506" || got["CD2_2"] != "-0.000183131573751" || got["CTYPE1"] != "RA---TAN-SIP" {
		t.Errorf("cards %v", got)
	}
	rec, _ := os.ReadFile(record)
	if !strings.Contains(string(rec), "add_path "+testIndexDir+"\nautoindex") || !strings.Contains(string(rec), "--ra 179.40000") {
		t.Errorf("solve-field ran with %q", rec)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Errorf("left %d entries in the work dir", len(left))
	}
}

func TestSolveNoSolution(t *testing.T) {
	t.Parallel()
	cmd, _ := fakeSolveField(t, "", 0)
	s := &Solver{Command: cmd, IndexDir: testIndexDir, Timeout: time.Minute}
	s.MarkReady()
	if _, err := s.Solve(context.Background(), t.TempDir(), "master.fit", Hint{}); !errors.Is(err, ErrNoSolution) {
		t.Errorf("error %v, want no solution", err)
	}
}

// A solve that outlasts its time is stopped.
func TestSolveTimeout(t *testing.T) {
	t.Parallel()
	cmd, _ := fakeSolveField(t, wcsHeader, 30)
	s := &Solver{Command: cmd, IndexDir: testIndexDir, Timeout: 300 * time.Millisecond}
	s.MarkReady()
	start := time.Now()
	_, err := s.Solve(context.Background(), t.TempDir(), "master.fit", Hint{})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s", d)
	}
}

// With ASTROMETRY_TEST_INDEX_DIR and ASTROMETRY_TEST_IMAGE set (and
// solve-field on PATH), a real solve near ASTROMETRY_TEST_RADEC
// ("ra,dec").
func TestSolveReal(t *testing.T) {
	t.Parallel()
	dir, image := os.Getenv("ASTROMETRY_TEST_INDEX_DIR"), os.Getenv("ASTROMETRY_TEST_IMAGE")
	if dir == "" || image == "" {
		t.Skip("ASTROMETRY_TEST_INDEX_DIR and ASTROMETRY_TEST_IMAGE not set")
	}
	h := Hint{RA: math.NaN(), Dec: math.NaN(), Width: 4096}
	if _, err := fmt.Sscanf(os.Getenv("ASTROMETRY_TEST_RADEC"), "%g,%g", &h.RA, &h.Dec); err == nil {
		h.Radius = 5
	}
	s := &Solver{Command: "solve-field", IndexDir: dir, CPUSeconds: 120, Timeout: 5 * time.Minute}
	s.MarkReady()
	cards, err := s.Solve(context.Background(), t.TempDir(), image, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if strings.HasPrefix(c.Name, "CRVAL") || strings.HasPrefix(c.Name, "CD") {
			t.Log(c.Name, c.Value)
		}
	}
}
