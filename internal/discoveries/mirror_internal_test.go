package discoveries

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

func TestCatalogsReadTheMirrorsAndFallBackToVizieR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Query().Get("-source") == "B/vsx/vsx" {
			fmt.Fprint(w, "Name\tType\tmax\tf_min\tmin\tn_max\tPeriod\tRAJ2000\tDEJ2000\n\n----\t----\t----\t----\t----\t----\t----\t----\t----\nVSX A\tM\t10\t\t12\tV\t300\t10.0\t41.0\n")
			return
		}
		fmt.Fprint(w, "RA_ICRS\tDE_ICRS\tGmag\tpmRA\tpmDE\n\n----\t----\t----\t----\t----\n10.0\t41.0\t12.0\t\t\n")
	}))
	defer srv.Close()
	b := &starcat.MemBucket{}
	c := &Catalogs{VizierURL: srv.URL, GaiaMirror: starcat.NewGaia(b, "gaia"), VSXMirror: starcat.NewVSX(b, "vsx")}
	stars, err := c.Gaia(ctx, 10, 41, 0.5)
	if err != nil || len(stars) != 1 || asked.Load() != 1 {
		t.Fatalf("without a mirror: %v, %v, VizieR asked %d times", stars, err, asked.Load())
	}
	nan := math.NaN()
	err = starcat.WriteGaia(ctx, b, "gaia", []starcat.GaiaStar{
		{SourceID: 1, RA: 10, Dec: 41, G: 15, PMRA: 3, PMDec: -2},
		{SourceID: 2, RA: 10.01, Dec: 41, G: 9, PMRA: nan, PMDec: nan},
		{SourceID: 3, RA: 10.02, Dec: 41, G: 21.6},
		{SourceID: 4, RA: 10.03, Dec: 41, G: float32(nan)},
		{SourceID: 5, RA: 12, Dec: 41, G: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := starcat.WriteVSX(ctx, b, "vsx", []starcat.VSXStar{
		{OID: 7, Name: "V1", Type: "M", RA: 10, Dec: 41, Max: 10, Min: 2, FMin: "(", NMax: "V", Period: 300, Epoch: nan},
		{OID: 8, Name: "V2", RA: 10.1, Dec: 41, Max: nan, Min: nan, Period: nan, Epoch: nan},
	}); err != nil {
		t.Fatal(err)
	}
	c.GaiaMirror, c.VSXMirror = starcat.NewGaia(b, "gaia"), starcat.NewVSX(b, "vsx")
	asked.Store(0)
	stars, err = c.Gaia(ctx, 10, 41, 0.5)
	want := []Star{{RA: 10.01, Dec: 41, G: 9}, {RA: 10, Dec: 41, G: 15, PMRA: 3, PMDec: -2}}
	if err != nil || fmt.Sprint(stars) != fmt.Sprint(want) {
		t.Fatalf("Gaia from the mirror: %v, %v, want %v", stars, err, want)
	}
	vars, err := c.VSX(ctx, 10, 41, 0.5)
	if err != nil || len(vars) != 2 || vars[0].Name != "V1" || !vars[0].Amplitude || vars[0].Band != "V" || vars[0].Period != 300 || vars[1].Max != 0 {
		t.Fatalf("VSX from the mirror: %+v, %v", vars, err)
	}
	if asked.Load() != 0 {
		t.Fatalf("VizieR asked %d times with the mirrors in place", asked.Load())
	}
}

// TestGaiaFromTheMirrorGoesPastVizieRsCap replays Cygnis Loop Panel 2,
// whose VizieR read stopped at its 400,000th star, G 18.78: a cache that
// short is read again from the mirror, which gives every star.
func TestGaiaFromTheMirrorGoesPastVizieRsCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &starcat.MemBucket{}
	n := gaiaMaxRows + 10
	stars := make([]starcat.GaiaStar, n)
	for i := range stars {
		stars[i] = starcat.GaiaStar{SourceID: int64(i + 1), RA: 312 + float64(i%1000)/1000, Dec: 30 + float64(i/1000)/1000, G: float32(12 + 7*float64(i)/float64(n)), PMRA: 0, PMDec: 0}
	}
	if err := starcat.WriteGaia(ctx, b, "gaia", stars); err != nil {
		t.Fatal(err)
	}
	store := &memStore{m: map[string][]byte{}}
	short := gaiaCache{RA: 312.5, Dec: 30.2, Radius: 2, Fetched: time.Now(), PM: true, Stars: [][5]float64{{312.5, 30.2, 12, 0, 0}}}
	r := New(nil, nil, store, &Catalogs{VizierURL: "http://127.0.0.1:1", GaiaMirror: starcat.NewGaia(b, "gaia")}, Options{})
	if err := r.putJSON(ctx, keyPrefix+"cache/Cygnis Loop Panel 2/gaia.json.zst", short); err != nil {
		t.Fatal(err)
	}
	got, err := r.gaia(ctx, "Cygnis Loop Panel 2", 312.5, 30.2, 1)
	if err != nil || len(got) != n {
		t.Fatalf("%d stars, %v, want all %d", len(got), err, n)
	}
	var c gaiaCache
	if err := r.getJSON(ctx, keyPrefix+"cache/Cygnis Loop Panel 2/gaia.json.zst", &c); err != nil || !c.Full || len(c.Stars) != n {
		t.Errorf("cache full %v with %d stars, %v", c.Full, len(c.Stars), err)
	}
}
