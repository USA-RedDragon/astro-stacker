package mosaics_test

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

const singleID = "single"

func TestGrid(t *testing.T) {
	t.Parallel()
	rig := testRig()
	cases := []struct {
		name       string
		o          mosaics.Outline
		rows, cols int
		rot        float64
		id, label  string
		kind       string
		coverage   float64
		tol        float64
	}{
		{"markarian", mosaics.Outline{Centre: mosaics.Point{RA: 187, Dec: 13}, MajorArcmin: 400, MinorArcmin: 250, PADeg: 90}, 3, 3, 0, "grid-3x3", "Grid 3 × 3", mosaics.KindGrid, 1, 0},
		{"rho block", mosaics.Outline{Centre: mosaics.Point{RA: 246.4, Dec: -23.4}, MajorArcmin: 300, MinorArcmin: 240}, 3, 2, 0, "grid-3x2", "Grid 2 × 3", mosaics.KindGrid, 1, 0},
		{"rotated block", mosaics.Outline{Centre: mosaics.Point{RA: 312, Dec: 44}, MajorArcmin: 200, MinorArcmin: 100, PADeg: 40}, 3, 2, 70, "grid-3x2", "Grid 2 × 3", mosaics.KindGrid, 1, 0},
		{"one frame", mosaics.Outline{Centre: mosaics.Point{RA: 83, Dec: -5}, MajorArcmin: 60}, 1, 1, 0, singleID, "One frame", mosaics.KindSingle, 1, 0},
		{"too small to cover", mosaics.Outline{Centre: mosaics.Point{RA: 83, Dec: -5}, MajorArcmin: 600, MinorArcmin: 600}, 1, 2, 0, "grid-1x2", "Grid 2 × 1", mosaics.KindGrid, 0.2, 0.06},
		{"clamped", mosaics.Outline{Centre: mosaics.Point{RA: 83, Dec: -5}, MajorArcmin: 60}, 0, -1, 0, singleID, "One frame", mosaics.KindSingle, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := mosaics.Grid(tc.o, tc.rows, tc.cols, tc.rot, 15, rig)
			if l.ID != tc.id || l.Name != tc.label || l.Kind != tc.kind {
				t.Fatalf("got %q %q %q, want %q %q %q", l.ID, l.Name, l.Kind, tc.id, tc.label, tc.kind)
			}
			if !near(l.Coverage, tc.coverage, tc.tol) {
				t.Fatalf("coverage %v, want %v", l.Coverage, tc.coverage)
			}
			if !strings.HasPrefix(l.Detail, "Covers ") || !strings.HasSuffix(l.Detail, "% of the outline") {
				t.Fatalf("detail %q", l.Detail)
			}
			if l.OverlapPct != 15 || l.RotationDeg != tc.rot {
				t.Fatalf("overlap %v rotation %v", l.OverlapPct, l.RotationDeg)
			}
			rows, cols := max(tc.rows, 1), max(tc.cols, 1)
			if len(l.Panels) != rows*cols || l.Rows != rows || l.Cols != cols {
				t.Fatalf("%d panels in %dx%d, want %dx%d", len(l.Panels), l.Rows, l.Cols, rows, cols)
			}
			var centres []mosaics.Point
			for i, p := range l.Panels {
				if p.N != i+1 || p.Row != i/cols || p.Col != i%cols {
					t.Fatalf("panel %d is N=%d row %d col %d", i, p.N, p.Row, p.Col)
				}
				if p.Covers < 0 || p.Covers > 1 {
					t.Fatalf("panel %d covers %v", p.N, p.Covers)
				}
				if angDiff(p.RotationDeg, tc.rot) > 5 {
					t.Fatalf("panel %d rotation %v strays from %v", p.N, p.RotationDeg, tc.rot)
				}
				centres = append(centres, p.Centre)
			}
			gr, gc := mosaics.GridCells(centres, tc.rot, rig)
			for i, p := range l.Panels {
				if gr[i] != p.Row || gc[i] != p.Col {
					t.Fatalf("GridCells puts panel %d at %d,%d, layout says %d,%d", p.N, gr[i], gc[i], p.Row, p.Col)
				}
			}
			mid := mosaics.Point{}
			for _, c := range centres {
				xi, eta := mosaics.Project(tc.o.Centre, c)
				mid.RA += xi / float64(len(centres))
				mid.Dec += eta / float64(len(centres))
			}
			if !near(mid.RA, 0, 1e-3) || !near(mid.Dec, 0, 1e-3) {
				t.Fatalf("block centred at %v,%v off the outline centre", mid.RA, mid.Dec)
			}
		})
	}
}

func TestGridNumbersFromNorthEast(t *testing.T) {
	t.Parallel()
	o := mosaics.Outline{Centre: mosaics.Point{RA: 187, Dec: 13}, MajorArcmin: 300, MinorArcmin: 200}
	l := mosaics.Grid(o, 2, 3, 0, 15, testRig())
	first, last := l.Panels[0].Centre, l.Panels[len(l.Panels)-1].Centre
	if first.Dec <= last.Dec || first.RA <= last.RA {
		t.Fatalf("panel 1 at %v is not north-east of panel 6 at %v", first, last)
	}
	if l.Panels[1].Centre.RA >= first.RA {
		t.Fatalf("panel 2 at %v is not west of panel 1 at %v", l.Panels[1].Centre, first)
	}
	if l.Panels[3].Centre.Dec >= first.Dec {
		t.Fatalf("panel 4 at %v is not south of panel 1 at %v", l.Panels[3].Centre, first)
	}
}

func TestGridOverlapMatchesRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		dec     float64
		rot     float64
		overlap float64
	}{
		{"on the equator", 0, 0, 15},
		{"mid north", 41, 0, 20},
		{"rho", -23.4, 0, 15},
		{"high and rotated", 62, 35, 10},
		{"no overlap", 13, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := mosaics.Outline{Centre: mosaics.Point{RA: 100, Dec: tc.dec}, MajorArcmin: 600, MinorArcmin: 400}
			l := mosaics.Grid(o, 3, 4, tc.rot, tc.overlap, testRig())
			for i := range l.Panels {
				a := l.Panels[i]
				for _, b := range l.Panels[i+1:] {
					if b.Row != a.Row || b.Col != a.Col+1 {
						continue
					}
					got := mosaics.OverlapFraction(a.Footprint, b.Footprint)
					if !near(got, tc.overlap/100, 0.006) {
						t.Fatalf("panels %d and %d overlap %v, want %v", a.N, b.N, got, tc.overlap/100)
					}
				}
			}
		})
	}
}

func TestBrick(t *testing.T) {
	t.Parallel()
	rig := testRig()
	o := mosaics.Outline{Centre: mosaics.Point{RA: 187, Dec: 13}, MajorArcmin: 400, MinorArcmin: 300}
	cases := []struct {
		name       string
		rows, cols int
		rot        float64
	}{
		{"2x3", 2, 3, 0},
		{"3x3", 3, 3, 0},
		{"rotated 3x2", 3, 2, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := mosaics.Brick(o, tc.rows, tc.cols, tc.rot, 15, rig)
			if l.Kind != mosaics.KindBrick || len(l.Panels) != tc.rows*tc.cols {
				t.Fatalf("kind %q with %d panels", l.Kind, len(l.Panels))
			}
			step := rig.WidthDeg * 0.85
			first := map[int]float64{}
			for _, p := range l.Panels {
				if p.Col != 0 {
					continue
				}
				xi, eta := mosaics.Project(o.Centre, p.Centre)
				first[p.Row] = xi*cosD(tc.rot) - eta*sinD(tc.rot)
			}
			for r := 1; r < tc.rows; r++ {
				if got := first[r-1] - first[r]; !near(abs(got), step/2, 0.02) {
					t.Fatalf("rows %d and %d shifted by %v, want %v", r-1, r, got, step/2)
				}
			}
		})
	}
}

func TestAlternatives(t *testing.T) {
	t.Parallel()
	rig := testRig()
	cases := []struct {
		name      string
		o         mosaics.Outline
		rot       float64
		firstID   string
		wantIDs   []string
		wantKinds []string
		maxLen    int
	}{
		{"point", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 41}}, 0, singleID, []string{singleID}, nil, 1},
		{"small galaxy", mosaics.Outline{Centre: mosaics.Point{RA: 148.9, Dec: 69}, MajorArcmin: 27, MinorArcmin: 14, PADeg: 157}, 0, singleID, []string{singleID}, nil, 1},
		{"andromeda aligned", mosaics.Outline{Centre: mosaics.Point{RA: 10.68, Dec: 41.27}, MajorArcmin: 190, MinorArcmin: 60, PADeg: 35}, 125, singleID, nil, nil, 5},
		{"rho block", mosaics.Outline{Centre: mosaics.Point{RA: 246.4, Dec: -23.4}, MajorArcmin: 300, MinorArcmin: 240}, 90, "grid-2x2", nil, nil, 5},
		{"veil", mosaics.Outline{Centre: mosaics.Point{RA: 312.75, Dec: 30.7}, MajorArcmin: 180, MinorArcmin: 180}, 0, singleID, []string{singleID, "grid-2x1"}, nil, 5},
		{"elongated", mosaics.Outline{Centre: mosaics.Point{RA: 187, Dec: 13}, MajorArcmin: 420, MinorArcmin: 100}, 90, "grid-1x2", nil, []string{mosaics.KindStrip}, 5},
		{"sadr", mosaics.Outline{Centre: mosaics.Point{RA: 305, Dec: 40}, MajorArcmin: 480, MinorArcmin: 360, PADeg: 20}, 0, "", nil, []string{mosaics.KindGrid, mosaics.KindBrick}, 5},
		{"enormous", mosaics.Outline{Centre: mosaics.Point{RA: 90, Dec: 20}, MajorArcmin: 1500, MinorArcmin: 900, PADeg: 10}, 0, "", nil, []string{mosaics.KindGrid}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ls := mosaics.Alternatives(tc.o, tc.rot, 15, rig)
			if len(ls) == 0 || len(ls) > tc.maxLen {
				t.Fatalf("%d alternatives", len(ls))
			}
			if tc.firstID != "" && ls[0].ID != tc.firstID {
				t.Fatalf("first is %q, want %q", ls[0].ID, tc.firstID)
			}
			ids := map[string]bool{}
			kinds := map[string]bool{}
			prev := -1.0
			for i, l := range ls {
				if ids[l.ID] {
					t.Fatalf("duplicate %q", l.ID)
				}
				ids[l.ID] = true
				kinds[l.Kind] = true
				score := float64(len(l.Panels)) + 4*(1-l.Coverage)
				if score < prev-1e-9 {
					t.Fatalf("option %d (%s) scores %v after %v", i, l.ID, score, prev)
				}
				prev = score
				for _, k := range ls[:i] {
					if len(k.Panels) == len(l.Panels) && near(k.Coverage, l.Coverage, 0.0099) {
						t.Fatalf("%s duplicates %s", l.ID, k.ID)
					}
				}
				if !strings.HasPrefix(l.Detail, "Covers ") {
					t.Fatalf("detail %q", l.Detail)
				}
				for j, p := range l.Panels {
					if p.N != j+1 {
						t.Fatalf("%s panel %d numbered %d", l.ID, j, p.N)
					}
				}
			}
			if tc.wantIDs != nil && len(ids) != len(tc.wantIDs) {
				t.Fatalf("ids %v, want %v", ids, tc.wantIDs)
			}
			for _, id := range tc.wantIDs {
				if !ids[id] {
					t.Fatalf("missing %q in %v", id, ids)
				}
			}
			for _, k := range tc.wantKinds {
				if !kinds[k] {
					t.Fatalf("missing kind %q in %v", k, kinds)
				}
			}
		})
	}
}

func TestAlternativesFullAndCheap(t *testing.T) {
	t.Parallel()
	o := mosaics.Outline{Centre: mosaics.Point{RA: 305, Dec: 40}, MajorArcmin: 480, MinorArcmin: 360, PADeg: 20}
	ls := mosaics.Alternatives(o, 0, 15, testRig())
	full, cheap := -1, -1
	for i, l := range ls {
		if l.Kind == mosaics.KindGrid && l.Coverage >= 0.98 && (full < 0 || len(l.Panels) < len(ls[full].Panels)) {
			full = i
		}
	}
	if full < 0 {
		t.Fatalf("no full grid in %v", ids(ls))
	}
	for i, l := range ls {
		if len(l.Panels) < len(ls[full].Panels) && l.Coverage >= 0.85 {
			cheap = i
		}
	}
	if cheap < 0 {
		t.Fatalf("no cheaper option than %s in %v", ls[full].ID, ids(ls))
	}
	smaller := mosaics.Grid(o, ls[full].Rows, ls[full].Cols-1, 0, 15, testRig())
	smallerRows := mosaics.Grid(o, ls[full].Rows-1, ls[full].Cols, 0, 15, testRig())
	if smaller.Coverage >= 0.98 || smallerRows.Coverage >= 0.98 {
		t.Fatalf("%s is not the smallest full grid", ls[full].ID)
	}
}

func TestAlternativesDropEmptyPanels(t *testing.T) {
	t.Parallel()
	o := mosaics.Outline{Centre: mosaics.Point{RA: 305, Dec: 40}, MajorArcmin: 480, MinorArcmin: 360, PADeg: 20}
	for _, l := range mosaics.Alternatives(o, 0, 15, testRig()) {
		if l.Kind != mosaics.KindBrick && l.Kind != mosaics.KindGrid {
			continue
		}
		dropped := len(l.Panels) < l.Rows*l.Cols
		if dropped != strings.Contains(l.Detail, "dropped") {
			t.Fatalf("%s has %d of %d panels, detail %q", l.ID, len(l.Panels), l.Rows*l.Cols, l.Detail)
		}
		for _, p := range l.Panels {
			if p.Covers == 0 {
				t.Fatalf("%s keeps panel %d with nothing of the object", l.ID, p.N)
			}
		}
	}
}

func ids(ls []mosaics.Layout) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

func TestSuggestRotation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		o    mosaics.Outline
		want float64
	}{
		{"point", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 10}}, 0},
		{"round", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 10}, MajorArcmin: 300, MinorArcmin: 300, PADeg: 45}, 0},
		{"small fits any way", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 10}, MajorArcmin: 60, MinorArcmin: 20, PADeg: 33}, 0},
		{"east west", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 10}, MajorArcmin: 420, MinorArcmin: 90, PADeg: 90}, 0},
		{"north south", mosaics.Outline{Centre: mosaics.Point{RA: 10, Dec: 10}, MajorArcmin: 420, MinorArcmin: 90, PADeg: 0}, 90},
		{"andromeda", mosaics.Outline{Centre: mosaics.Point{RA: 10.68, Dec: 41.27}, MajorArcmin: 190, MinorArcmin: 60, PADeg: 35}, 125},
		{"tilted strip", mosaics.Outline{Centre: mosaics.Point{RA: 200, Dec: -10}, MajorArcmin: 500, MinorArcmin: 80, PADeg: 150}, 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mosaics.SuggestRotation(tc.o, 15, testRig())
			if got < 0 || got >= 180 {
				t.Fatalf("rotation %v outside [0,180)", got)
			}
			if !near(got, tc.want, 1e-6) {
				t.Fatalf("rotation %v, want %v", got, tc.want)
			}
		})
	}
}
