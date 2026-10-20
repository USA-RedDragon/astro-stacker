package discoveries

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const (
	fixtureStamp = 2*(kernelStampR+kernelHalf) + 1
	mosaicPad    = 32
)

// readKernelMosaic lays out, side by side, stamps cut around the 45
// brightest kernel stars of the kernel tile at x 2083-3124, y 0-1044 of
// Cygnus Loop Panel 2's broadband night of 2026-08-10: src is the
// reference, blurred to match dst, the night. Stamp 40's kernel star has a
// saturated star 14 px away, inside its stamp.
func readKernelMosaic(t *testing.T) (src, dst *Plane, centres [][2]float64) {
	t.Helper()
	f, err := os.Open("testdata/kernel-cygnus-loop-p2-2026-08-10.bin.zst")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	type stamp struct {
		fx, fy   float64
		src, dst []float32
	}
	var stamps []stamp
	for {
		var hdr [4]int32
		if err := binary.Read(zr, binary.LittleEndian, &hdr); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var pos [2]float32
		s := stamp{src: make([]float32, fixtureStamp*fixtureStamp), dst: make([]float32, fixtureStamp*fixtureStamp)}
		for _, v := range []any{&pos, s.src, s.dst} {
			if err := binary.Read(zr, binary.LittleEndian, v); err != nil {
				t.Fatal(err)
			}
		}
		s.fx, s.fy = float64(pos[0])-float64(hdr[2]), float64(pos[1])-float64(hdr[3])
		stamps = append(stamps, s)
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(stamps)))))
	rows := (len(stamps) + cols - 1) / cols
	W, H := cols*fixtureStamp+2*mosaicPad, rows*fixtureStamp+2*mosaicPad
	src, dst = NewPlane(W, H), NewPlane(W, H)
	for n, s := range stamps {
		ox, oy := mosaicPad+n%cols*fixtureStamp, mosaicPad+n/cols*fixtureStamp
		for y := range fixtureStamp {
			copy(src.Data[(oy+y)*W+ox:], s.src[y*fixtureStamp:(y+1)*fixtureStamp])
			copy(dst.Data[(oy+y)*W+ox:], s.dst[y*fixtureStamp:(y+1)*fixtureStamp])
		}
		c := float64(fixtureStamp / 2)
		centres = append(centres, [2]float64{float64(ox) + c + s.fx, float64(oy) + c + s.fy})
	}
	return src, dst, centres
}

// TestKernelStarsLeaveOutStarsBesideASaturatedOne is the known answer of
// the tile that filled Cygnus Loop Panel 2's nights with fading changes: its
// kernel, fitted with stamp 40 and the saturated star in it, sums to 1.03
// with almost nothing at its centre, and leaves a dark lobe beside every
// star.
func TestKernelStarsLeaveOutStarsBesideASaturatedOne(t *testing.T) {
	t.Parallel()
	src, dst, centres := readKernelMosaic(t)
	var all []Source
	for _, c := range centres {
		all = append(all, Source{X: c[0], Y: c[1], Peak: float64(dst.At(int(math.Round(c[0])), int(math.Round(c[1]))))})
	}
	sat := Source{Peak: math.Inf(-1)}
	for y := -fixtureStamp / 2; y <= fixtureStamp/2; y++ {
		for x := -fixtureStamp / 2; x <= fixtureStamp/2; x++ {
			px, py := int(math.Round(centres[40][0]))+x, int(math.Round(centres[40][1]))+y
			if v := float64(dst.At(px, py)); v > sat.Peak {
				sat = Source{X: float64(px), Y: float64(py), Peak: v}
			}
		}
	}
	if d := math.Hypot(sat.X-centres[40][0], sat.Y-centres[40][1]); sat.Peak < 0.9 || d < 10 {
		t.Fatalf("stamp 40's brightest pixel is %.3f, %.1f px from its star", sat.Peak, d)
	}
	maxv := sat.Peak

	k, ok := solveKernel(src, dst, all)
	if !ok {
		t.Fatal("no kernel")
	}
	if sum, c := k.Sum(), k.K[kernelHalf][kernelHalf]; sum < 1.02 || c > 0.05 {
		t.Fatalf("kernel with stamp 40: sum %.3f centre %.3f, want the broken kernel of the night (1.03, 0.03)", sum, c)
	}

	stars := pickKernelStars(append(all, sat), maxv)
	for _, s := range stars {
		if s == all[40] || s == sat {
			t.Fatalf("kernel star at %.1f,%.1f is saturated or has a saturated star 14 px away", s.X, s.Y)
		}
	}
	if len(stars) < len(all)-3 {
		t.Fatalf("%d kernel stars of %d", len(stars), len(all))
	}
	k, ok = solveKernel(src, dst, stars)
	if !ok {
		t.Fatal("no kernel")
	}
	if sum := k.Sum(); math.Abs(sum-1) > 0.015 {
		t.Fatalf("kernel sum %.3f, want about 1 as in the night's other tiles", sum)
	}
	if c := k.K[kernelHalf][kernelHalf]; c < 0.1 {
		t.Fatalf("kernel centre %.3f: the kernel is split around its centre", c)
	}
	var res []float64
	for _, s := range stars {
		var night, left float64
		xi, yi := int(math.Round(s.X)), int(math.Round(s.Y))
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				v := k.Bg
				for ky := -kernelHalf; ky <= kernelHalf; ky++ {
					for kx := -kernelHalf; kx <= kernelHalf; kx++ {
						v += k.K[ky+kernelHalf][kx+kernelHalf] * float64(src.At(xi+dx+kx, yi+dy+ky))
					}
				}
				d := float64(dst.At(xi+dx, yi+dy))
				night += d
				left += d - v
			}
		}
		res = append(res, left/night)
	}
	if m := median64(res); math.Abs(m) > 0.01 {
		t.Fatalf("median residual %.3f of the night's light at the kernel stars", m)
	}
}
