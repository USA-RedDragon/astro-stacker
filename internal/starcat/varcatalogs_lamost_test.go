package starcat_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/klauspost/compress/zstd"
)

func readZstdLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// TestAuditGapCatalogsMirror mirrors real records of the catalogues the
// 2026-10-24 audit found missing, read from CDS: 28305's star (Gaia DR3
// 171049722563343232) as a ROT star of 11.32 days in Qiao+ 2024, in Xu+
// 2022 and in Mowlavi+ 2021, whose galactic position must land on
// VizieR's own ICRS one, and 76400's star as TIC 470756157 in Kostov+ 2025
// table 2. Next to each are its neighbouring records in the file, one of
// them with Qiao's 1000-day limit as its period.
func TestAuditGapCatalogsMirror(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		c       *starcat.VarCatalog
		lrecl   int
		ra, dec float64
		check   func(s starcat.VarStar) bool
	}{
		{starcat.LAMOSTPeriodic(), 327, 62.060772, 34.860125, func(s starcat.VarStar) bool {
			return s.ID == 203509154 && s.Name == "ZTF 656211100005788" && s.Period == 11.319572 && s.Type == "ROT" && near(0.069672, s.Amp)
		}},
		{starcat.LAMOSTVariable(), 536, 62.060772, 34.860125, func(s starcat.VarStar) bool {
			return s.ID == 2 && near(0.047, s.Amp) && math.IsNaN(s.Period)
		}},
		{starcat.GaiaLargeAmplitude(), 268, 62.0607254466, 34.8601650295, func(s starcat.VarStar) bool {
			return s.ID == 171049722563343232 && s.Type == "LAV" && math.Abs(float64(s.Amp)-0.153395) < 1e-3 &&
				math.Abs(float64(s.Mag)-14.805149) < 1e-3
		}},
		{starcat.TESSEBCandidates(), 38, 357.901746, 62.436841, func(s starcat.VarStar) bool {
			return s.ID == 470756157 && s.Name == "TIC 470756157" && math.IsNaN(s.Period) && near(14.13, s.Mag)
		}},
	} {
		t.Run(tc.c.Name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			lines := readZstdLines(t, tc.c.Name+".dat.zst")
			var z bytes.Buffer
			w := gzip.NewWriter(&z)
			_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
			_ = w.Close()
			readme := fmt.Sprintf("File Summary:\n FileName  Lrecl  Records  Explanations\nReadMe  80  .  This file\n%s  %d  %d  Variables\n", tc.c.File, tc.lrecl, len(lines))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch strings.TrimPrefix(r.URL.Path, "/") {
				case readMe:
					fmt.Fprint(w, readme)
				case tc.c.File + ".gz":
					_, _ = w.Write(z.Bytes())
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			b := &starcat.MemBucket{}
			if err := tc.c.Mirror(ctx, starcat.MirrorOptions{Bucket: b, Prefix: tc.c.Prefix, BaseURL: srv.URL + "/"}); err != nil {
				t.Fatal(err)
			}
			v := starcat.NewVariables(b, tc.c.Prefix)
			all, err := v.Cone(ctx, 0, 0, 180, nil)
			if err != nil || len(all) != len(lines) {
				t.Fatalf("whole sky: %d rows, want %d: %v", len(all), len(lines), err)
			}
			for _, s := range all {
				if tc.c.Name == "lamost-per" && s.ID == 194709148 && !math.IsNaN(s.Period) {
					t.Errorf("1000-day limit read as a period: %+v", s)
				}
			}
			got, err := v.Cone(ctx, tc.ra, tc.dec, 0.05/3600, nil)
			if err != nil || len(got) != 1 || !tc.check(got[0]) {
				t.Fatalf("0.05 arcsec cone at the audit star: %+v, %v", got, err)
			}
		})
	}
}
