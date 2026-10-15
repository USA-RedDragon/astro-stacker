package discoveries

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Candidate 13268, Andromeda's 2026-10-11 night, was the brighter lobe of
// the dipole Gaia DR3 381120971054786304 (G 16.2, +167/+20 mas/yr) left by
// moving between the reference (January 2025) and the night: 4.2" from the
// star's 2016 position, so with no Gaia match it read as a supernova.
func TestHighProperMotionStarIsAMovingStarNotAChange(t *testing.T) {
	t.Parallel()
	star := Star{RA: 9.92443827189, Dec: 40.43591244637, G: 16.19673, PMRA: 167.078, PMDec: 20.166}
	candRA, candDec := 9.925947627771222, 40.43608717674044
	const scale = 1.9224130190471593
	w, err := ParseWCS(tanWCS(star.RA, star.Dec, scale, 500.5, 500.5), 1000)
	if err != nil {
		t.Fatal(err)
	}
	refEpoch := time.Date(2025, 1, 21, 3, 0, 0, 0, time.UTC)
	night := time.Date(2026, 10, 12, 10, 4, 12, 0, time.UTC)
	in := &Input{WCS: w, Gaia: []Star{star}}
	in.gaiaRef, in.gaiaNight = starsAt(in.Gaia, refEpoch), starsAt(in.Gaia, night)
	x, y, _ := w.PixelOf(candRA, candDec)
	gi, sep, moved := in.newGaiaIndex(1000, 1000).match(x, y)
	if gi != 0 {
		t.Fatalf("candidate 13268 is %.2f\" from the star's 2016 position and should match it at the night's epoch", Separation(candRA, candDec, star.RA, star.Dec)*3600)
	}
	if sep > gaiaMatchPx || moved < minStarMotionPx {
		t.Errorf("sep %.2f px, moved %.3f px", sep, moved)
	}
	g := star.G
	sigma := 1.748225450515747 / hfrToSigma
	c := Candidate{Sign: 1, GaiaG: &g, GaiaMotionPx: moved, Dipole: 0.5164845482404861, RefFlux: 0.051895321394549686, DiffFlux: 0.003756667521047348}
	if !c.movingStar(sigma) {
		t.Error("candidate 13268's dipole of 0.52 on a star that moved is a moving-star residual")
	}
	nova := Candidate{Sign: 1, Dipole: 0.3396736362761891, RefFlux: 0.013816234437508684, DiffFlux: 0.005225720348680625}
	if nova.movingStar(sigma) {
		t.Error("the M 31 nova (candidate 13267) has no star under it and is not a moving star")
	}
	still := c
	still.GaiaMotionPx = 0.004
	if still.movingStar(sigma) {
		t.Error("a star that barely moved is judged by its brightness, not as a moving star")
	}
	flare := c
	flare.DiffFlux = 0.3 * c.RefFlux
	if flare.movingStar(sigma) {
		t.Error("a change far larger than the star's motion could leave, such as a flare, is not a moving star")
	}
}

func TestStarAtMovesByProperMotion(t *testing.T) {
	t.Parallel()
	s := Star{RA: 10, Dec: 60, PMRA: 3600, PMDec: -3600}
	ra, dec := s.At(gaiaEpoch().Add(time.Duration(365.25 * 24 * float64(time.Hour))))
	if d := dec - 59.999; d > 1e-9 || d < -1e-9 {
		t.Errorf("dec %.9f", dec)
	}
	if d := ra - 10.002; d > 1e-9 || d < -1e-9 {
		t.Errorf("ra %.9f: 3.6\"/yr along the sky at Dec 60 is 7.2\" of RA", ra)
	}
	if ra, dec := (Star{RA: 1, Dec: 2}).At(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)); ra != 1 || dec != 2 {
		t.Error("a star with no proper motion stays put")
	}
}

func TestGaiaCacheWithoutProperMotionsIsFetchedAgain(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if out := r.URL.Query().Get("-out"); out != "RA_ICRS,DE_ICRS,Gmag,pmRA,pmDE" {
			t.Errorf("asked Gaia for %s", out)
		}
		fmt.Fprint(w, "#\nRA_ICRS\tDE_ICRS\tGmag\tpmRA\tpmDE\n-------\t-------\t----\t----\t----\n")
		fmt.Fprint(w, "009.92443827\t+40.43591245\t16.197\t167.078\t20.166\n010.00000000\t+40.50000000\t15.000\t\t\n")
	}))
	defer srv.Close()
	ctx := context.Background()
	store := &memStore{m: map[string][]byte{}}
	r := New(nil, nil, store, &Catalogs{VizierURL: srv.URL}, Options{})
	old := struct {
		RA, Dec, Radius float64
		Fetched         time.Time
		Stars           [][3]float64
	}{10, 40.5, 1, time.Now(), [][3]float64{{9.92443827, 40.43591245, 16.197}}}
	if err := r.putJSON(ctx, keyPrefix+"cache/Andromeda/gaia.json.zst", old); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stars, err := r.gaia(ctx, "Andromeda", 10, 40.5, 0.5)
		if err != nil {
			t.Fatal(err)
		}
		if len(stars) != 2 || stars[0].PMRA != 167.078 || stars[0].PMDec != 20.166 || stars[1].PMRA != 0 {
			t.Fatalf("stars %+v", stars)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("asked VizieR %d times: a cache without proper motions is fetched again once, then used", calls.Load())
	}
}
