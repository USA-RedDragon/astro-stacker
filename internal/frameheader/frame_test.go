package frameheader_test

import (
	"maps"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

func TestNormalizeFilter(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Halpha": "H-a", "H-alpha": "H-a", "H-a": "H-a", "OIII": "O-III", "Oiii": "O-III",
		"O-III": "O-III", "SII": "S-II", "Sii": "S-II", "Red": "Red", "Luminance": "Luminance", "": "",
	} {
		if got := frameheader.NormalizeFilter(in); got != want {
			t.Errorf("NormalizeFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

// Telescope.live writes no RA or DEC, only a plate solution; its centre is
// where the frame pointed.
func TestPointingFromPlateSolution(t *testing.T) {
	t.Parallel()
	tl := frameheader.Keywords{
		"NAXIS1": "4096", "NAXIS2": "4096", "CTYPE1": "RA---TAN-SIP", "CTYPE2": "DEC--TAN-SIP",
		"CRVAL1": "246.393785355", "CRVAL2": "-24.9432664485", "CRPIX1": "2049", "CRPIX2": "2049",
		"CUNIT1": "deg", "CUNIT2": "deg",
		"CD1_1": "-3.03066414149E-05", "CD1_2": "0.0013240730461", "CD2_1": "-0.00132017700099", "CD2_2": "-3.46093717871E-05",
	}
	f := frameheader.FromKeywords(tl)
	if math.Abs(f.RA-246.3938) > 0.001 || math.Abs(f.Dec+24.9433) > 0.001 {
		t.Errorf("pointing %.4f %.4f, want the solution's centre 246.3938 -24.9433", f.RA, f.Dec)
	}

	// The reference pixel in a corner: the centre is 2047.5 pixels along
	// each axis from it, where the solution's forward projection puts it.
	corner := maps.Clone(tl)
	corner["CRPIX1"], corner["CRPIX2"] = "1", "1"
	ra, dec, ok := frameheader.WCSCentre(corner)
	if !ok {
		t.Fatal("no centre")
	}
	x, y := project(ra, dec, 246.393785355, -24.9432664485,
		[4]float64{-3.03066414149e-05, 0.0013240730461, -0.00132017700099, -3.46093717871e-05})
	if math.Abs(x-2047.5) > 0.01 || math.Abs(y-2047.5) > 0.01 {
		t.Errorf("centre %.5f %.5f projects to pixel offset %.3f %.3f, want 2047.5 2047.5", ra, dec, x, y)
	}

	// NINA's own RA and DEC come first.
	nina := maps.Clone(tl)
	nina["RA"], nina["DEC"] = "10.5", "41.2"
	if f := frameheader.FromKeywords(nina); f.RA != 10.5 || f.Dec != 41.2 {
		t.Errorf("pointing %v %v, want NINA's 10.5 41.2", f.RA, f.Dec)
	}

	// A solution in other coordinates says nothing about RA and Dec.
	galactic := maps.Clone(tl)
	galactic["CTYPE1"], galactic["CTYPE2"] = "GLON-TAN", "GLAT-TAN"
	if f := frameheader.FromKeywords(galactic); !math.IsNaN(f.RA) || !math.IsNaN(f.Dec) {
		t.Errorf("galactic solution gave pointing %v %v", f.RA, f.Dec)
	}
}

// project is the gnomonic projection of ra, dec about ra0, dec0, in pixels
// from the reference pixel through the CD matrix cd.
func project(ra, dec, ra0, dec0 float64, cd [4]float64) (x, y float64) {
	const deg = math.Pi / 180
	a, d, d0 := (ra-ra0)*deg, dec*deg, dec0*deg
	den := math.Sin(d)*math.Sin(d0) + math.Cos(d)*math.Cos(d0)*math.Cos(a)
	xi := math.Cos(d) * math.Sin(a) / den / deg
	eta := (math.Sin(d)*math.Cos(d0) - math.Cos(d)*math.Sin(d0)*math.Cos(a)) / den / deg
	det := cd[0]*cd[3] - cd[1]*cd[2]
	return (cd[3]*xi - cd[1]*eta) / det, (-cd[2]*xi + cd[0]*eta) / det
}

// Some Telescope.live subs carry neither RA/DEC nor a plate solution, only
// the target's position.
func TestPointingFromObjectPosition(t *testing.T) {
	t.Parallel()
	f := frameheader.FromKeywords(frameheader.Keywords{
		"IMAGETYP": "Light Frame", "OBJCTRA": "14 39 29.71", "OBJCTDEC": "-60 49 55.99",
	})
	if math.Abs(f.RA-219.873792) > 1e-5 || math.Abs(f.Dec+60.832219) > 1e-5 {
		t.Errorf("pointing %v %v, want 219.873792 -60.832219", f.RA, f.Dec)
	}
	if _, _, ok := frameheader.ObjectPosition(frameheader.Keywords{"OBJCTRA": "25 00 00", "OBJCTDEC": "10"}); ok {
		t.Error("an RA past 24h was read")
	}
}
