package skycutout_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	imgjpeg "image/jpeg"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
)

const tileFixtures = "testdata/tiles"

type dirStore struct {
	root  string
	fetch bool
}

func (d dirStore) Tile(ctx context.Context, order int, npix int64) ([]byte, error) {
	path := filepath.Join(d.root, skycutout.TilePath(order, npix))
	b, err := os.ReadFile(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) || !d.fetch {
		return b, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://stpubdata.s3.amazonaws.com/mast/skybackgrounds/DSSColor/"+skycutout.TilePath(order, npix), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	if b, err = io.ReadAll(resp.Body); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0o600)
}

type goldenField struct {
	name                   string
	ra, dec, fov, rotation float64
	width, height, order   int
}

func goldenFields() []goldenField {
	return []goldenField{
		{"equator", 83.82, -5.39, 1, 0, 300, 300, 6},
		{"equator-rot", 83.82, -5.39, 1, 30, 300, 200, 6},
		{"pole", 0, 90, 3, 0, 300, 300, 4},
		{"nearpole", 37.95, 89.5, 3, 0, 300, 300, 4},
		{"south", 120, -89, 2, 0, 300, 300, 5},
		{"rawrap", 0, 30, 2, 0, 300, 300, 5},
		{"wide", 280, 0, 74, 0, 600, 400, 0},
		{"narrow", 202.47, 47.195, 0.5, 0, 300, 300, 7},
		{"wide-rot", 266.4, -29, 74, 45, 400, 600, 0},
		{"narrow-order8", 202.47, 47.195, 0.25, 0, 300, 300, 8},
	}
}

func (f goldenField) params() skycutout.Params {
	return skycutout.Params{RA: f.ra, Dec: f.dec, FOV: f.fov, Rotation: f.rotation, Width: f.width, Height: f.height}
}

func luminance(img image.Image) ([]float64, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float64, w*h)
	for y := range h {
		for x := range w {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			out[y*w+x] = 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
		}
	}
	fine, coarse := boxBlur(out, w, h, 1), boxBlur(out, w, h, 8)
	for i := range out {
		out[i] = fine[i] - coarse[i]
	}
	return out, w, h
}

func boxBlur(in []float64, w, h, r int) []float64 {
	tmp := make([]float64, len(in))
	out := make([]float64, len(in))
	for y := range h {
		for x := range w {
			var s, n float64
			for k := max(0, x-r); k <= min(w-1, x+r); k++ {
				s += in[y*w+k]
				n++
			}
			tmp[y*w+x] = s / n
		}
	}
	for y := range h {
		for x := range w {
			var s, n float64
			for k := max(0, y-r); k <= min(h-1, y+r); k++ {
				s += tmp[k*w+x]
				n++
			}
			out[y*w+x] = s / n
		}
	}
	return out
}

func ncc(a, b []float64, w, x0, y0, x1, y1, dx, dy int) float64 {
	var sa, sb, saa, sbb, sab, n float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			va := a[y*w+x]
			vb := b[(y+dy)*w+x+dx]
			sa += va
			sb += vb
			saa += va * va
			sbb += vb * vb
			sab += va * vb
			n++
		}
	}
	cov := sab/n - sa/n*sb/n
	va := saa/n - sa/n*sa/n
	vb := sbb/n - sb/n*sb/n
	if va <= 0 || vb <= 0 {
		return 0
	}
	return cov / math.Sqrt(va*vb)
}

type regionFit struct {
	dx, dy int
	score  float64
	fx, fy float64
}

const searchRadius = 6

// alignment finds, in each quadrant, the shift of golden against ours that
// correlates best.
func alignment(ours, golden image.Image) ([]regionFit, error) {
	a, w, h := luminance(ours)
	b, gw, gh := luminance(golden)
	if w != gw || h != gh {
		return nil, fmt.Errorf("size %dx%d, golden %dx%d", w, h, gw, gh)
	}
	m := searchRadius
	var fits []regionFit
	for qy := range 2 {
		for qx := range 2 {
			x0, x1 := m+qx*(w/2-m), m+qx*(w/2-m)+w/2-m
			y0, y1 := m+qy*(h/2-m), m+qy*(h/2-m)+h/2-m
			x1, y1 = min(x1, w-m), min(y1, h-m)
			best := regionFit{score: -2}
			for dy := -m; dy <= m; dy++ {
				for dx := -m; dx <= m; dx++ {
					if s := ncc(a, b, w, x0, y0, x1, y1, dx, dy); s > best.score {
						best = regionFit{dx: dx, dy: dy, score: s}
					}
				}
			}
			peak := func(lo, mid, hi float64) float64 {
				d := lo - 2*mid + hi
				if d >= 0 {
					return 0
				}
				return 0.5 * (lo - hi) / d
			}
			best.fx = float64(best.dx)
			best.fy = float64(best.dy)
			if abs(best.dx) < m && abs(best.dy) < m {
				best.fx += peak(ncc(a, b, w, x0, y0, x1, y1, best.dx-1, best.dy), best.score, ncc(a, b, w, x0, y0, x1, y1, best.dx+1, best.dy))
				best.fy += peak(ncc(a, b, w, x0, y0, x1, y1, best.dx, best.dy-1), best.score, ncc(a, b, w, x0, y0, x1, y1, best.dx, best.dy+1))
			}
			fits = append(fits, best)
		}
	}
	return fits, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func renderGolden(t *testing.T, r *skycutout.Renderer, f goldenField) (image.Image, image.Image) {
	t.Helper()
	b, err := r.Render(context.Background(), f.params())
	if err != nil {
		t.Fatalf("%s: render: %v", f.name, err)
	}
	ours, err := imgjpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	gb, err := os.ReadFile(filepath.Join("testdata", "golden", f.name+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := imgjpeg.Decode(bytes.NewReader(gb))
	if err != nil {
		t.Fatal(err)
	}
	return ours, golden
}

// TestGoldenAlignment renders fields covering the pole, the equator, the RA
// wrap and wide, narrow and rotated views from fixture tiles and checks each
// quadrant lines up with what hips2fits returned for the same request.
// SKYCUTOUT_FETCH_TILES=1 downloads missing fixture tiles from STScI.
func TestGoldenAlignment(t *testing.T) {
	t.Parallel()
	store := dirStore{root: tileFixtures, fetch: os.Getenv("SKYCUTOUT_FETCH_TILES") != ""}
	for _, f := range goldenFields() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			r := &skycutout.Renderer{Store: store, MaxOrder: 8}
			if order, ok := r.Order(f.params()); !ok || order != f.order {
				t.Fatalf("order %d (%v), want %d", order, ok, f.order)
			}
			ours, golden := renderGolden(t, r, f)
			fits, err := alignment(ours, golden)
			if err != nil {
				t.Fatal(err)
			}
			for q, ft := range fits {
				t.Logf("quadrant %d: shift %+.2f,%+.2f px, correlation %.3f", q, ft.fx, ft.fy, ft.score)
				if math.Abs(ft.fx) > 1 || math.Abs(ft.fy) > 1 || ft.score < 0.6 {
					t.Errorf("quadrant %d is off hips2fits by %+.2f,%+.2f px with correlation %.3f", q, ft.fx, ft.fy, ft.score)
				}
			}
		})
	}
}
