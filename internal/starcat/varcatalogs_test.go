package starcat_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

const readMe = "ReadMe"

// varSource serves a catalogue's ReadMe and gzipped file like CDS.
type varSource struct {
	mu      sync.Mutex
	readme  string
	body    []byte
	fetches atomic.Int64
}

func newVarSource(t *testing.T, c *starcat.VarCatalog, lines []string, lrecl, records int) (*varSource, *httptest.Server) {
	t.Helper()
	s := &varSource{}
	s.set(c, lines, lrecl, records, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case readMe:
			fmt.Fprint(w, s.readme)
		case c.File + ".gz":
			s.fetches.Add(1)
			_, _ = w.Write(s.body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *varSource) set(c *starcat.VarCatalog, lines []string, lrecl, records int, note string) {
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	_ = w.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readme = fmt.Sprintf("File Summary:\n FileName  Lrecl  Records  Explanations\nReadMe  80  .  This file\n%s  %d  %d  Variables %s\n", c.File, lrecl, records, note)
	s.body = z.Bytes()
}

func readLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func near(a float64, b float32) bool { return math.Abs(float64(b)-a) < 1e-5 }

type varCase struct {
	c     *starcat.VarCatalog
	lrecl int
	check func(s starcat.VarStar) bool
}

func varCases() []varCase {
	return []varCase{
		{starcat.GaiaVariability(), 137, func(s starcat.VarStar) bool {
			return s.ID == 1000000465343848704 && s.Name == "" && s.Type == "AGN" && near(0.8193384, s.Score) &&
				s.RA == 104.81637830443292 && s.Dec == 55.96026415938674 && math.IsNaN(s.Period) && math.IsNaN(float64(s.Mag))
		}},
		{starcat.ATLASVariables(), 1911, func(s starcat.VarStar) bool {
			return s.ID == 91931086331406435 && s.Name == "J108.6331-13.3866" && s.Type == "dubious" && s.RA == 108.63314 && s.Dec == -13.38661 &&
				!math.IsNaN(float64(s.Mag)) && math.IsNaN(s.Period)
		}},
		{starcat.ZTFVariables(), 228, func(s starcat.VarStar) bool {
			return s.ID == 1 && s.Name == "ZTFJ000000.13+620605.8" && s.Type == "BYDra" && s.RA == 0.00056 && s.Dec == 62.10163 &&
				s.Period == 1.9449979 && near(16.571, s.Mag) && near(0.078, s.Amp) && math.IsNaN(float64(s.Score))
		}},
		{starcat.ASASSNVariables(), 799, func(s starcat.VarStar) bool {
			return s.ID == 1 && s.Name == "ASASSN-V J221121.85-290944.6" && s.Type == "EW" && s.RA == 332.84103 && s.Dec == -29.1624 &&
				s.Period == 0.368478 && near(13.56, s.Mag) && near(0.4, s.Amp) && near(1, s.Score)
		}},
	}
}

func TestVarCatalogsMirrorCDSRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range varCases() {
		t.Run(tc.c.Name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			lines := readLines(t, tc.c.File)
			src, srv := newVarSource(t, tc.c, lines, tc.lrecl, len(lines))
			b := &starcat.MemBucket{}
			o := starcat.MirrorOptions{Bucket: b, Prefix: tc.c.Prefix, BaseURL: srv.URL + "/"}
			for range 2 {
				if err := tc.c.Mirror(ctx, o); err != nil {
					t.Fatal(err)
				}
			}
			if src.fetches.Load() != 1 {
				t.Fatalf("the file was fetched %d times for one ReadMe", src.fetches.Load())
			}
			v := starcat.NewVariables(b, tc.c.Prefix)
			m, err := v.Manifest(ctx)
			if err != nil || m.Rows != int64(len(lines)) || m.Catalog != tc.c.Name || m.Verified.IsZero() || len(m.Inputs) != 1 || m.Inputs[0].SHA256 == "" {
				t.Fatalf("manifest %+v, %v", m, err)
			}
			first, err := v.Cone(ctx, 0, 0, 180, nil)
			if err != nil || len(first) != len(lines) {
				t.Fatalf("whole sky: %d rows of %d, %v", len(first), len(lines), err)
			}
			var row *starcat.VarStar
			for i := range first {
				if tc.check(first[i]) {
					row = &first[i]
				}
			}
			if row == nil {
				t.Fatalf("the first record is not read as expected: %+v", first)
			}
			got, err := v.Cone(ctx, row.RA+0.5/3600, row.Dec, 1.0/3600, nil)
			if err != nil || len(got) != 1 || got[0].ID != row.ID {
				t.Fatalf("1 arcsec cone: %+v, %v", got, err)
			}
			src.set(tc.c, lines, tc.lrecl, len(lines), "revised")
			if err := tc.c.Mirror(ctx, o); err != nil {
				t.Fatal(err)
			}
			m2, err := starcat.NewVariables(b, tc.c.Prefix).Manifest(ctx)
			if err != nil || m2.Version == m.Version {
				t.Fatalf("a changed ReadMe kept version %s: %v", m.Version, err)
			}
			keys, _ := b.List(ctx, tc.c.Prefix+"/")
			for _, k := range keys {
				if strings.Contains(k, m.Version) {
					t.Fatalf("old version left: %s", k)
				}
			}
		})
	}
}

func TestVarCatalogMirrorRejectsAFileUnlikeItsReadMe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := starcat.ZTFVariables()
	lines := readLines(t, c.File)
	for _, tc := range []struct {
		name           string
		lrecl, records int
		body           func([]byte) []byte
		want           string
		fetches        int64
	}{
		{"record count", 228, len(lines) + 1, nil, "the ReadMe lists", 1},
		{"record length", 200, len(lines), nil, "record length 200 in the ReadMe, expected 228", 0},
		{"truncated gzip", 228, len(lines), func(b []byte) []byte { return b[:len(b)-6] }, "deadline exceeded", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src, srv := newVarSource(t, c, lines, tc.lrecl, tc.records)
			if tc.body != nil {
				src.body = tc.body(src.body)
			}
			b := &starcat.MemBucket{}
			o := starcat.MirrorOptions{Bucket: b, Prefix: c.Prefix, BaseURL: srv.URL + "/"}
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			err := c.Mirror(ctx, o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
			if src.fetches.Load() != tc.fetches {
				t.Fatalf("the file was fetched %d times", src.fetches.Load())
			}
			if keys, _ := b.List(ctx, c.Prefix+"/"); len(keys) != 0 {
				t.Fatalf("stored %v from a bad file", keys)
			}
			if _, err := starcat.NewVariables(b, c.Prefix).Cone(ctx, 0, 62, 1, nil); !errors.Is(err, starcat.ErrUnavailable) {
				t.Fatalf("cone without a mirror: %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestVariablesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	nan := math.NaN()
	stars := []starcat.VarStar{
		{ID: 3, Name: "A", RA: 10, Dec: 41, Type: "EW", Period: 0.3, Mag: 12.5, Amp: 0.4, Score: 0.9},
		{ID: 1, Name: "", RA: 10.001, Dec: 41.001, Type: "LPV", Period: nan, Mag: float32(nan), Amp: float32(nan), Score: float32(nan)},
		{ID: 2, Name: "far", RA: 200, Dec: -30, Type: "M"},
	}
	b := &starcat.MemBucket{}
	if err := starcat.WriteVariables(ctx, b, "v", starcat.ASASSNVariables(), stars); err != nil {
		t.Fatal(err)
	}
	got, err := starcat.NewVariables(b, "v").Cone(ctx, 10, 41, 0.1, func(s *starcat.VarStar) bool { return s.Type != "M" })
	if err != nil || len(got) != 2 || got[0].ID != 1 || got[1] != stars[0] || !math.IsNaN(got[0].Period) || !math.IsNaN(float64(got[0].Score)) {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestVarCatalogMirrorRetriesADownloadCutMidRecord(t *testing.T) {
	t.Parallel()
	c := starcat.ATLASVariables()
	lines := readLines(t, c.File)
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write([]byte(lines[0] + "\n" + lines[1][:900]))
	_ = w.Flush()
	part := append([]byte(nil), z.Bytes()...)
	_, _ = w.Write([]byte(lines[1][900:] + "\n" + lines[2] + "\n"))
	_ = w.Close()
	var fetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case readMe:
			fmt.Fprintf(w, "%s  1911  3  ATLAS\n", c.File)
		case c.File + ".gz":
			fetches.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(z.Len()))
			_, _ = w.Write(part)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Mirror(ctx, starcat.MirrorOptions{Bucket: &starcat.MemBucket{}, Prefix: c.Prefix, BaseURL: srv.URL + "/"})
	if err == nil || strings.Contains(err.Error(), "line 2") || fetches.Load() != 1 {
		t.Fatalf("a download cut mid-record gave %v after %d fetches, want a retry", err, fetches.Load())
	}
}

// cutSource serves the ATLAS test file, cutting the whole download off
// half way; a Range request is answered with etag's file.
func cutSource(t *testing.T, etag string) (c *starcat.VarCatalog, lines []string, body []byte, whole, ranged *atomic.Int64, url string) {
	t.Helper()
	c = starcat.ATLASVariables()
	lines = readLines(t, c.File)
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	_ = w.Close()
	body = z.Bytes()
	whole, ranged = &atomic.Int64{}, &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case readMe:
			fmt.Fprintf(w, "%s  1911  %d  ATLAS\n", c.File, len(lines))
		case c.File + ".gz":
			w.Header().Set("ETag", `"v1"`)
			if r.Header.Get("Range") != "" {
				ranged.Add(1)
				w.Header().Set("ETag", etag)
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
				return
			}
			whole.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:len(body)/2])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, buf, err := hj.Hijack(); err == nil {
					_ = buf.Flush()
					_ = conn.Close()
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return c, lines, body, whole, ranged, srv.URL + "/"
}

func TestVarCatalogMirrorResumesABrokenDownload(t *testing.T) {
	t.Parallel()
	c, lines, body, whole, ranged, url := cutSource(t, `"v1"`)
	b := &starcat.MemBucket{}
	if err := c.Mirror(context.Background(), starcat.MirrorOptions{Bucket: b, Prefix: c.Prefix, BaseURL: url, ResumeWait: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if whole.Load() != 1 || ranged.Load() != 1 {
		t.Fatalf("%d whole downloads and %d resumed, want 1 and 1", whole.Load(), ranged.Load())
	}
	m, err := starcat.NewVariables(b, c.Prefix).Manifest(context.Background())
	if err != nil || m.Rows != int64(len(lines)) || m.Inputs[0].Bytes != int64(len(body)) {
		t.Fatalf("manifest %+v, %v", m, err)
	}
}

func TestVarCatalogMirrorDoesNotResumeAChangedFile(t *testing.T) {
	t.Parallel()
	c, _, _, whole, ranged, url := cutSource(t, `"v2"`)
	b := &starcat.MemBucket{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.Mirror(ctx, starcat.MirrorOptions{Bucket: b, Prefix: c.Prefix, BaseURL: url, ResumeWait: time.Millisecond})
	if err == nil || whole.Load() != 1 || ranged.Load() == 0 {
		t.Fatalf("a changed file gave %v after %d whole downloads and %d resumed", err, whole.Load(), ranged.Load())
	}
	if _, err := starcat.NewVariables(b, c.Prefix).Manifest(context.Background()); err == nil {
		t.Fatal("a mirror was written from a changed file")
	}
}
