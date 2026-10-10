package mosaics_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func angDiff(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return math.Abs(d)
}

func TestProjectDeprojectRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		centre mosaics.Point
		xi     float64
		eta    float64
	}{
		{"equatorial", mosaics.Point{RA: 10, Dec: 0}, 1.2, -0.7},
		{"wraps ra", mosaics.Point{RA: 359.5, Dec: 20}, 2, 1},
		{"southern", mosaics.Point{RA: 246.4, Dec: -23.4}, -3, 2.5},
		{"near pole", mosaics.Point{RA: 30, Dec: 88}, 1.5, 1.5},
		{"origin", mosaics.Point{RA: 187, Dec: 13}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := mosaics.Deproject(tc.centre, tc.xi, tc.eta)
			if p.RA < 0 || p.RA >= 360 {
				t.Fatalf("RA %v outside [0,360)", p.RA)
			}
			xi, eta := mosaics.Project(tc.centre, p)
			if !near(xi, tc.xi, 1e-9) || !near(eta, tc.eta, 1e-9) {
				t.Fatalf("round trip gave %v,%v, want %v,%v", xi, eta, tc.xi, tc.eta)
			}
		})
	}
}

func TestProjectDirections(t *testing.T) {
	t.Parallel()
	c := mosaics.Point{RA: 100, Dec: 30}
	cases := []struct {
		name          string
		p             mosaics.Point
		xiSign, etaSg int
	}{
		{"north", mosaics.Point{RA: 100, Dec: 31}, 0, 1},
		{"south", mosaics.Point{RA: 100, Dec: 29}, 0, -1},
		{"east", mosaics.Point{RA: 101, Dec: 30}, 1, 1},
		{"west", mosaics.Point{RA: 99, Dec: 30}, -1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			xi, eta := mosaics.Project(c, tc.p)
			if sign(xi) != tc.xiSign {
				t.Fatalf("xi %v, want sign %d", xi, tc.xiSign)
			}
			if sign(eta) != tc.etaSg {
				t.Fatalf("eta %v, want sign %d", eta, tc.etaSg)
			}
		})
	}
}

func sign(v float64) int {
	switch {
	case v > 1e-9:
		return 1
	case v < -1e-9:
		return -1
	default:
		return 0
	}
}

func TestPanelFootprint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		centre mosaics.Point
		rot    float64
	}{
		{"equator", mosaics.Point{RA: 50, Dec: 0}, 0},
		{"mid", mosaics.Point{RA: 187, Dec: 13}, 0},
		{"rotated frame", mosaics.Point{RA: 300, Dec: 40}, 37},
		{"south", mosaics.Point{RA: 246.4, Dec: -23.4}, 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := mosaics.PanelFootprint(tc.centre, tc.rot, testRig())
			if a := mosaics.Area(f); !near(a, 3.32*2.22, 0.01) {
				t.Fatalf("area %v, want about %v", a, 3.32*2.22)
			}
			for i, p := range f {
				xi, eta := mosaics.Project(tc.centre, p)
				if !near(math.Hypot(xi, eta), math.Hypot(1.66, 1.11), 1e-9) {
					t.Fatalf("corner %d at %v,%v is not a half diagonal away", i, xi, eta)
				}
			}
		})
	}
	f := mosaics.PanelFootprint(mosaics.Point{RA: 50, Dec: 0}, 0, testRig())
	xi, eta := mosaics.Project(mosaics.Point{RA: 50, Dec: 0}, f[0])
	if !near(xi, 1.66, 1e-9) || !near(eta, 1.11, 1e-9) {
		t.Fatalf("first corner at rotation 0 is %v,%v, want north-east 1.66,1.11", xi, eta)
	}
	f = mosaics.PanelFootprint(mosaics.Point{RA: 50, Dec: 0}, 90, testRig())
	xi, eta = mosaics.Project(mosaics.Point{RA: 50, Dec: 0}, f[0])
	if !near(xi, 1.11, 1e-9) || !near(eta, -1.66, 1e-9) {
		t.Fatalf("first corner at rotation 90 is %v,%v, want 1.11,-1.66", xi, eta)
	}
}

func TestOffsetMatchesFootprints(t *testing.T) {
	t.Parallel()
	rig := testRig()
	cases := []struct {
		name     string
		centre   mosaics.Point
		rot      float64
		dx, dy   float64
		overlap  float64
		tolerate float64
	}{
		{"edge to edge east", mosaics.Point{RA: 10, Dec: 0}, 0, rig.WidthDeg, 0, 0, 0.005},
		{"edge to edge north", mosaics.Point{RA: 10, Dec: 40}, 0, 0, rig.HeightDeg, 0, 0.005},
		{"edge to edge rotated", mosaics.Point{RA: 200, Dec: -30}, 37, rig.WidthDeg, 0, 0, 0.005},
		{"edge to edge rotated short", mosaics.Point{RA: 80, Dec: 60}, 120, 0, -rig.HeightDeg, 0, 0.015},
		{"15 percent long", mosaics.Point{RA: 187, Dec: 13}, 0, rig.WidthDeg * 0.85, 0, 0.15, 0.005},
		{"20 percent short", mosaics.Point{RA: 187, Dec: 13}, 45, 0, rig.HeightDeg * 0.8, 0.2, 0.005},
		{"diagonal", mosaics.Point{RA: 5, Dec: 20}, 0, rig.WidthDeg * 0.5, rig.HeightDeg * 0.5, 0.25, 0.01},
		{"same", mosaics.Point{RA: 5, Dec: 20}, 10, 0, 0, 1, 1e-9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := mosaics.PanelFootprint(tc.centre, tc.rot, rig)
			p := mosaics.Offset(tc.centre, tc.rot, tc.dx, tc.dy)
			b := mosaics.PanelFootprint(p, tc.rot, rig)
			if got := mosaics.OverlapFraction(a, b); !near(got, tc.overlap, tc.tolerate) {
				t.Fatalf("overlap %v, want %v", got, tc.overlap)
			}
			if got := mosaics.OverlapFraction(b, a); !near(got, tc.overlap, tc.tolerate) {
				t.Fatalf("reverse overlap %v, want %v", got, tc.overlap)
			}
		})
	}
}

func TestOffsetAxes(t *testing.T) {
	t.Parallel()
	c := mosaics.Point{RA: 120, Dec: 0}
	cases := []struct {
		name   string
		rot    float64
		dx, dy float64
		xi     float64
		eta    float64
	}{
		{"long at 0 is east", 0, 1, 0, 1, 0},
		{"short at 0 is north", 0, 0, 1, 0, 1},
		{"short at 90 is east", 90, 0, 1, 1, 0},
		{"long at 90 is south", 90, 1, 0, 0, -1},
		{"short at 180 is south", 180, 0, 1, 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			xi, eta := mosaics.Project(c, mosaics.Offset(c, tc.rot, tc.dx, tc.dy))
			if !near(xi, tc.xi, 1e-9) || !near(eta, tc.eta, 1e-9) {
				t.Fatalf("offset at %v,%v, want %v,%v", xi, eta, tc.xi, tc.eta)
			}
		})
	}
}

func TestOverlapFraction(t *testing.T) {
	t.Parallel()
	c := mosaics.Point{RA: 83, Dec: -5}
	big := mosaics.PanelFootprint(c, 0, testRig())
	small := mosaics.PanelFootprint(c, 25, mosaics.Rig{WidthDeg: 1, HeightDeg: 0.5})
	cases := []struct {
		name string
		a, b mosaics.Footprint
		want float64
		tol  float64
	}{
		{"identical", big, big, 1, 1e-9},
		{"contained", big, small, 1, 1e-6},
		{"contains", small, big, 1, 1e-6},
		{"far", big, mosaics.PanelFootprint(mosaics.Point{RA: 263, Dec: 5}, 0, testRig()), 0, 0},
		{"apart", big, mosaics.PanelFootprint(mosaics.Offset(c, 0, 4, 0), 0, testRig()), 0, 0},
		{"half", big, mosaics.PanelFootprint(mosaics.Offset(c, 0, 1.66, 0), 0, testRig()), 0.5, 0.005},
		{"crossed", big, mosaics.PanelFootprint(c, 90, testRig()), 2.22 * 2.22 / (3.32 * 2.22), 0.005},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mosaics.OverlapFraction(tc.a, tc.b); !near(got, tc.want, tc.tol) {
				t.Fatalf("overlap %v, want %v", got, tc.want)
			}
		})
	}
}

func TestArea(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rig  mosaics.Rig
		dec  float64
	}{
		{"default", testRig(), 10},
		{"square", mosaics.Rig{WidthDeg: 1, HeightDeg: 1}, -50},
		{"tiny", mosaics.Rig{WidthDeg: 0.1, HeightDeg: 0.05}, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := mosaics.PanelFootprint(mosaics.Point{RA: 40, Dec: tc.dec}, 33, tc.rig)
			want := tc.rig.WidthDeg * tc.rig.HeightDeg
			if got := mosaics.Area(f); !near(got, want, want*1e-6) {
				t.Fatalf("area %v, want %v", got, want)
			}
		})
	}
}

func gridCentres(c mosaics.Point, rows, cols int, rot, overlap float64, rig mosaics.Rig) []mosaics.Point {
	out := make([]mosaics.Point, 0, rows*cols)
	sx, sy := rig.WidthDeg*(1-overlap), rig.HeightDeg*(1-overlap)
	for r := range rows {
		for k := range cols {
			dx := (float64(cols-1)/2 - float64(k)) * sx
			dy := (float64(rows-1)/2 - float64(r)) * sy
			out = append(out, mosaics.Offset(c, rot, dx, dy))
		}
	}
	return out
}

func TestNeighbours(t *testing.T) {
	t.Parallel()
	rig := testRig()
	c := mosaics.Point{RA: 187, Dec: 13}
	fps := func(ps []mosaics.Point) []mosaics.Footprint {
		out := make([]mosaics.Footprint, len(ps))
		for i, p := range ps {
			out[i] = mosaics.PanelFootprint(p, 0, rig)
		}
		return out
	}
	cases := []struct {
		name string
		fps  []mosaics.Footprint
		min  float64
		want [][]int
	}{
		{"strip", fps(gridCentres(c, 1, 3, 0, 0.15, rig)), 0.03, [][]int{{1}, {0, 2}, {1}}},
		{"grid without diagonals", fps(gridCentres(c, 2, 2, 0, 0.15, rig)), 0.03, [][]int{{1, 2}, {0, 3}, {0, 3}, {1, 2}}},
		{"grid with diagonals", fps(gridCentres(c, 2, 2, 0, 0.15, rig)), 0.01, [][]int{{1, 2, 3}, {0, 2, 3}, {0, 1, 3}, {0, 1, 2}}},
		{"threshold too high", fps(gridCentres(c, 1, 2, 0, 0.15, rig)), 0.5, [][]int{{}, {}}},
		{"touching only", fps(gridCentres(c, 1, 2, 0, 0, rig)), 0.03, [][]int{{}, {}}},
		{"empty", nil, 0.03, [][]int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mosaics.Neighbours(tc.fps, tc.min)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if !equalInts(got[i], tc.want[i]) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGridCells(t *testing.T) {
	t.Parallel()
	rig := testRig()
	cases := []struct {
		name       string
		centre     mosaics.Point
		rows, cols int
		rot        float64
		overlap    float64
	}{
		{"markarian 3x3", mosaics.Point{RA: 187, Dec: 13}, 3, 3, 0, 0.15},
		{"rho 3x4", mosaics.Point{RA: 246.4, Dec: -23.4}, 3, 4, 0, 0.2},
		{"rotated 2x3", mosaics.Point{RA: 310, Dec: 42}, 2, 3, 60, 0.15},
		{"strip", mosaics.Point{RA: 0.5, Dec: 0}, 1, 5, 0, 0.1},
		{"column", mosaics.Point{RA: 83, Dec: 5}, 4, 1, 90, 0.3},
		{"wraps ra", mosaics.Point{RA: 359.8, Dec: 61}, 3, 2, 0, 0.15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			centres := gridCentres(tc.centre, tc.rows, tc.cols, tc.rot, tc.overlap, rig)
			perm := rand.New(rand.NewPCG(1, 2)).Perm(len(centres))
			shuffled := make([]mosaics.Point, len(centres))
			for i, j := range perm {
				shuffled[i] = centres[j]
			}
			rows, cols := mosaics.GridCells(shuffled, tc.rot, rig)
			for i, j := range perm {
				if rows[i] != j/tc.cols || cols[i] != j%tc.cols {
					t.Fatalf("panel %d at row %d col %d, want row %d col %d", j, rows[i], cols[i], j/tc.cols, j%tc.cols)
				}
			}
		})
	}
	rows, cols := mosaics.GridCells(nil, 0, rig)
	if len(rows) != 0 || len(cols) != 0 {
		t.Fatalf("empty input gave %v %v", rows, cols)
	}
}

func TestGridCellsEastIsColumnZero(t *testing.T) {
	t.Parallel()
	c := mosaics.Point{RA: 100, Dec: 20}
	east := mosaics.Point{RA: 102, Dec: 20}
	_, cols := mosaics.GridCells([]mosaics.Point{c, east}, 0, testRig())
	if cols[1] != 0 || cols[0] != 1 {
		t.Fatalf("cols %v, want the east panel first", cols)
	}
	north := mosaics.Point{RA: 100, Dec: 22}
	rows, _ := mosaics.GridCells([]mosaics.Point{c, north}, 0, testRig())
	if rows[1] != 0 || rows[0] != 1 {
		t.Fatalf("rows %v, want the north panel first", rows)
	}
}

func TestCoverageGap(t *testing.T) {
	t.Parallel()
	rig := testRig()
	c := mosaics.Point{RA: 187, Dec: 13}
	planned := mosaics.PanelFootprint(c, 0, rig)
	shifted := func(rot, dx, dy float64) []mosaics.Footprint {
		return []mosaics.Footprint{mosaics.PanelFootprint(mosaics.Offset(c, rot, dx, dy), rot, rig)}
	}
	cases := []struct {
		name     string
		planned  mosaics.Footprint
		actual   []mosaics.Footprint
		fraction float64
		tol      float64
		where    string
	}{
		{"covered", planned, []mosaics.Footprint{planned}, 0, 0, ""},
		{"covered by bigger", planned, []mosaics.Footprint{mosaics.PanelFootprint(c, 0, mosaics.Rig{WidthDeg: 4, HeightDeg: 3})}, 0, 0, ""},
		{"nothing", planned, nil, 1, 0, "centre"},
		{"drifted west", planned, shifted(0, -0.332, 0), 0.1, 0.02, "E edge"},
		{"drifted east", planned, shifted(0, 0.332, 0), 0.1, 0.02, "W edge"},
		{"drifted south", planned, shifted(0, 0, -0.222), 0.1, 0.02, "N edge"},
		{"drifted north", planned, shifted(0, 0, 0.222), 0.1, 0.02, "S edge"},
		{"drifted south west", planned, shifted(0, -0.332, -0.222), 0.19, 0.02, "NE corner"},
		{"drifted north east", planned, shifted(0, 0.332, 0.222), 0.19, 0.02, "SW corner"},
		{"drifted north west", planned, shifted(0, -0.332, 0.222), 0.19, 0.02, "SE corner"},
		{"hole", planned, []mosaics.Footprint{
			mosaics.PanelFootprint(mosaics.Offset(c, 0, 1.2, 0), 0, mosaics.Rig{WidthDeg: 1, HeightDeg: 2.3}),
			mosaics.PanelFootprint(mosaics.Offset(c, 0, -1.2, 0), 0, mosaics.Rig{WidthDeg: 1, HeightDeg: 2.3}),
			mosaics.PanelFootprint(mosaics.Offset(c, 0, 0, 0.8), 0, mosaics.Rig{WidthDeg: 3.4, HeightDeg: 0.7}),
			mosaics.PanelFootprint(mosaics.Offset(c, 0, 0, -0.8), 0, mosaics.Rig{WidthDeg: 3.4, HeightDeg: 0.7}),
		}, 0.17, 0.02, "centre"},
		{"rotated plan, drifted along short axis", mosaics.PanelFootprint(c, 90, rig), shifted(90, 0, -0.222), 0.1, 0.02, "E edge"},
		{"two panels cover it", planned, []mosaics.Footprint{
			mosaics.PanelFootprint(mosaics.Offset(c, 0, 1.5, 0), 0, rig),
			mosaics.PanelFootprint(mosaics.Offset(c, 0, -1.5, 0), 0, rig),
		}, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := mosaics.CoverageGap(tc.planned, tc.actual)
			if !near(g.Fraction, tc.fraction, tc.tol) {
				t.Fatalf("fraction %v, want %v", g.Fraction, tc.fraction)
			}
			if !near(g.AreaDeg2, g.Fraction*mosaics.Area(tc.planned), 1e-9) {
				t.Fatalf("area %v does not match fraction %v", g.AreaDeg2, g.Fraction)
			}
			if g.Where != tc.where {
				t.Fatalf("where %q, want %q", g.Where, tc.where)
			}
		})
	}
}

func cosD(d float64) float64 { return math.Cos(d * math.Pi / 180) }
func sinD(d float64) float64 { return math.Sin(d * math.Pi / 180) }

func abs(v float64) float64 { return math.Abs(v) }
