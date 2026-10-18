package starcat_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// TestMoreVarCatalogsMirror mirrors real records of the catalogues the
// 2026-10-17 audit found missing, read from CDS: each file as CDS serves
// it (gzipped or plain), with the audit's stars where a catalogue lists
// one: 9124 as TIC 418699631, 4272 as a ZTF suspected variable, 33898 and
// 33899 in the TESS rotation periods, and M31 V0119 and V1111 (235 and
// 255) at their B1950 positions moved to J2000.
func TestMoreVarCatalogsMirror(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		c      *starcat.VarCatalog
		lrecl  int
		plain  bool
		stored int
		ra     float64
		dec    float64
		check  func(s starcat.VarStar) bool
	}{
		{starcat.TESSEBNew(), 250, false, 4, 0.63277266, 63.13044805, func(s starcat.VarStar) bool {
			return s.ID == 418699631 && s.Name == "TIC 418699631" && s.Period == 1.984438 && s.Type == "EB"
		}},
		{starcat.TESSEBKnown(), 147, true, 3, 219.043018, -24.33205152, func(s starcat.VarStar) bool {
			return s.ID == 10507 && s.Period == 1.939769 && s.Type == "EB"
		}},
		{starcat.TESSEBPrsa(), 625, false, 3, 175.108298759065, -6.10253019588716, func(s starcat.VarStar) bool {
			return s.ID == 1 && s.Name == "TIC 185259483" && math.Abs(s.Period-0.3105640362990626) < 1e-12
		}},
		{starcat.ZTFSuspected(), 120, false, 4, 0.91689, 62.42410, func(s starcat.VarStar) bool {
			return s.Name == "ZTFJ000340.05+622526.7" && s.Period == 8.79162 && s.Type == "VAR"
		}},
		{starcat.TESSRotation(), 411, true, 5, 84.086572, -0.965509, func(s starcat.VarStar) bool {
			return s.Name == "TIC 427450950" && s.Period == 0.464954 && s.Type == "ROT"
		}},
		{starcat.GCVSExtragalactic(), 160, false, 5, 9.973215, 40.474194, func(s starcat.VarStar) bool {
			return s.ID == 910119 && s.Name == "M31 V0119" && s.Period == 176.55 && s.Type == "CEP:"
		}},
	} {
		t.Run(tc.c.Name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			lines := readLines(t, tc.c.Name+".dat")
			body := []byte(strings.Join(lines, "\n") + "\n")
			file := tc.c.File
			if !tc.plain {
				var z bytes.Buffer
				w := gzip.NewWriter(&z)
				_, _ = w.Write(body)
				_ = w.Close()
				body, file = z.Bytes(), file+".gz"
			}
			readme := fmt.Sprintf("File Summary:\n FileName  Lrecl  Records  Explanations\nReadMe  80  .  This file\n%s  %d  %d  Variables\n", tc.c.File, tc.lrecl, len(lines))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch strings.TrimPrefix(r.URL.Path, "/") {
				case readMe:
					fmt.Fprint(w, readme)
				case file:
					_, _ = w.Write(body)
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
			if err != nil || len(all) != tc.stored {
				t.Fatalf("whole sky: %d rows, want %d: %v", len(all), tc.stored, err)
			}
			got, err := v.Cone(ctx, tc.ra, tc.dec, 3.0/3600, nil)
			if err != nil || len(got) == 0 || !tc.check(got[0]) {
				t.Fatalf("3 arcsec cone at the audit star: %+v, %v", got, err)
			}
		})
	}
}
