package discoveries

import (
	"math"
	"sort"
	"sync"
)

const (
	kernelHalf     = 4
	kernelSize     = 2*kernelHalf + 1
	kernelTerms    = kernelSize*kernelSize + 1
	kernelStampR   = 10
	kernelTilesX   = 6
	kernelTilesY   = 4
	kernelStars    = 120
	kernelMinStars = 15
	kernelIsolated = 20.0
)

// Kernel is a Bramich (2008) pixel kernel: dst ≈ correlate(src, K) + Bg.
type Kernel struct {
	K  [kernelSize][kernelSize]float64
	Bg float64
}

func (k Kernel) Sum() float64 {
	var s float64
	for _, r := range k.K {
		for _, v := range r {
			s += v
		}
	}
	return s
}

// solveKernel fits a kernel and a background offset that turn src into dst
// on stamps around the given stars, by linear least squares.
func solveKernel(src, dst *Plane, stars []Source) (Kernel, bool) {
	var ata [kernelTerms][kernelTerms]float64
	var atb [kernelTerms]float64
	used := 0
	row := make([]float64, kernelTerms)
	R := kernelStampR
	for _, s := range stars {
		xi, yi := int(math.Round(s.X)), int(math.Round(s.Y))
		if xi-R-kernelHalf < 0 || yi-R-kernelHalf < 0 || xi+R+kernelHalf >= src.W || yi+R+kernelHalf >= src.H {
			continue
		}
		bad := false
		for y := yi - R - kernelHalf; y <= yi+R+kernelHalf && !bad; y++ {
			for x := xi - R - kernelHalf; x <= xi+R+kernelHalf; x++ {
				if isNaN32(src.Data[y*src.W+x]) || isNaN32(dst.Data[y*dst.W+x]) {
					bad = true
					break
				}
			}
		}
		if bad {
			continue
		}
		used++
		for y := yi - R; y <= yi+R; y++ {
			for x := xi - R; x <= xi+R; x++ {
				n := 0
				for dy := -kernelHalf; dy <= kernelHalf; dy++ {
					base := (y+dy)*src.W + x
					for dx := -kernelHalf; dx <= kernelHalf; dx++ {
						row[n] = float64(src.Data[base+dx])
						n++
					}
				}
				row[n] = 1
				b := float64(dst.Data[y*dst.W+x])
				for i := range kernelTerms {
					ri := row[i]
					if ri == 0 {
						continue
					}
					atb[i] += ri * b
					for j := i; j < kernelTerms; j++ {
						ata[i][j] += ri * row[j]
					}
				}
			}
		}
	}
	if used < kernelMinStars {
		return Kernel{}, false
	}
	for i := range kernelTerms {
		for j := range i {
			ata[i][j] = ata[j][i]
		}
	}
	sol, ok := solveSPD(ata[:], atb[:])
	if !ok {
		return Kernel{}, false
	}
	var k Kernel
	for i := range kernelSize * kernelSize {
		k.K[i/kernelSize][i%kernelSize] = sol[i]
	}
	k.Bg = sol[kernelTerms-1]
	return k, true
}

// solveSPD solves a symmetric positive definite system by Cholesky
// decomposition, with a tiny ridge for stability.
func solveSPD(a [][kernelTerms]float64, b []float64) ([]float64, bool) {
	n := len(b)
	l := make([][]float64, n)
	var trace float64
	for i := range n {
		trace += a[i][i]
	}
	ridge := 1e-12 * trace / float64(n)
	for i := range n {
		l[i] = make([]float64, n)
		for j := 0; j <= i; j++ {
			s := a[i][j]
			if i == j {
				s += ridge
			}
			for k := range j {
				s -= l[i][k] * l[j][k]
			}
			if i == j {
				if s <= 0 {
					return nil, false
				}
				l[i][i] = math.Sqrt(s)
			} else {
				l[i][j] = s / l[j][j]
			}
		}
	}
	y := make([]float64, n)
	for i := range n {
		s := b[i]
		for k := range i {
			s -= l[i][k] * y[k]
		}
		y[i] = s / l[i][i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for k := i + 1; k < n; k++ {
			s -= l[k][i] * x[k]
		}
		x[i] = s / l[i][i]
	}
	return x, true
}

// kernelStars picks bright, unsaturated, isolated stars of dst for fitting
// kernels.
func kernelStarsOf(dst *Plane) []Source {
	bg := Background(dst, 128, nil)
	work := &Plane{W: dst.W, H: dst.H, Data: make([]float32, len(dst.Data))}
	copy(work.Data, dst.Data)
	bg.Subtract(work)
	stars := Extract(work, bg.RMSPlane(), nil, 50, 5)
	var maxv float64
	for _, v := range dst.Data {
		if !isNaN32(v) {
			maxv = math.Max(maxv, float64(v))
		}
	}
	var keep []Source
	for _, s := range stars {
		if !s.Edge && s.Peak < 0.3*maxv {
			keep = append(keep, s)
		}
	}
	return isolated(keep, kernelIsolated)
}

func isolated(stars []Source, r float64) []Source {
	pts := make([][2]float64, len(stars))
	for i, s := range stars {
		pts[i] = [2]float64{s.X, s.Y}
	}
	g := NewGrid(pts, r)
	var out []Source
	for i, s := range stars {
		if len(g.Within(s.X, s.Y, r)) == 1 {
			out = append(out, stars[i])
		}
	}
	return out
}

// MatchResult is how the sharper image was blurred to match the other.
type MatchResult struct {
	Tiles     int
	KernelSum float64
}

// MatchPSF blurs src to dst's star profile, tile by tile on a 6x4 grid,
// writing into out. Tiles without enough stars use the median kernel of
// the others.
func MatchPSF(src, dst *Plane, threads int) (*Plane, MatchResult, bool) {
	stars := kernelStarsOf(dst)
	type tile struct {
		xa, xb, ya, yb int
		k              Kernel
		ok             bool
	}
	W, H := src.W, src.H
	tiles := make([]tile, 0, kernelTilesX*kernelTilesY)
	for ty := range kernelTilesY {
		for tx := range kernelTilesX {
			tiles = append(tiles, tile{xa: tx * W / kernelTilesX, xb: (tx + 1) * W / kernelTilesX, ya: ty * H / kernelTilesY, yb: (ty + 1) * H / kernelTilesY})
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(threads, 1))
	for i := range tiles {
		t := &tiles[i]
		var sel []Source
		for _, s := range stars {
			if s.X >= float64(t.xa) && s.X < float64(t.xb) && s.Y >= float64(t.ya) && s.Y < float64(t.yb) {
				sel = append(sel, s)
			}
		}
		sort.Slice(sel, func(a, b int) bool { return sel[a].Flux > sel[b].Flux })
		if len(sel) > kernelStars {
			sel = sel[:kernelStars]
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			t.k, t.ok = solveKernel(src, dst, sel)
		}()
	}
	wg.Wait()
	var good []Kernel
	for _, t := range tiles {
		if t.ok {
			good = append(good, t.k)
		}
	}
	if len(good) == 0 {
		return nil, MatchResult{}, false
	}
	var fallback Kernel
	for i := range kernelSize {
		for j := range kernelSize {
			v := make([]float64, len(good))
			for n, k := range good {
				v[n] = k.K[i][j]
			}
			fallback.K[i][j] = median64(v)
		}
	}
	bgs := make([]float64, len(good))
	sums := make([]float64, len(good))
	for n, k := range good {
		bgs[n] = k.Bg
		sums[n] = k.Sum()
	}
	fallback.Bg = median64(bgs)
	out := NewPlane(W, H)
	for i := range tiles {
		t := &tiles[i]
		if !t.ok {
			t.k = fallback
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			correlate(src, out, t.k, t.xa, t.xb, t.ya, t.yb)
		}()
	}
	wg.Wait()
	return out, MatchResult{Tiles: len(good), KernelSum: median64(sums)}, true
}

// correlate writes correlate(src, k)+bg into out over a tile, with
// nearest-pixel edges and NaN treated as zero; pixels NaN in src stay NaN.
func correlate(src, out *Plane, k Kernel, xa, xb, ya, yb int) {
	W, H := src.W, src.H
	var kf [kernelSize][kernelSize]float32
	for i := range kernelSize {
		for j := range kernelSize {
			kf[i][j] = float32(k.K[i][j])
		}
	}
	bg := float32(k.Bg)
	for y := ya; y < yb; y++ {
		for x := xa; x < xb; x++ {
			if isNaN32(src.Data[y*W+x]) {
				out.Data[y*W+x] = float32(math.NaN())
				continue
			}
			var s float32
			for dy := -kernelHalf; dy <= kernelHalf; dy++ {
				yy := min(max(y+dy, 0), H-1)
				row := src.Data[yy*W:]
				kr := &kf[dy+kernelHalf]
				if x-kernelHalf >= 0 && x+kernelHalf < W {
					for dx := -kernelHalf; dx <= kernelHalf; dx++ {
						v := row[x+dx]
						if !isNaN32(v) {
							s += kr[dx+kernelHalf] * v
						}
					}
					continue
				}
				for dx := -kernelHalf; dx <= kernelHalf; dx++ {
					xx := min(max(x+dx, 0), W-1)
					v := row[xx]
					if !isNaN32(v) {
						s += kr[dx+kernelHalf] * v
					}
				}
			}
			out.Data[y*W+x] = s + bg
		}
	}
}
