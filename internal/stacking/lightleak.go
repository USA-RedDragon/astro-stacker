package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// A clean dark is flat: over this camera's darks the medians of 32×24 blocks
// spread (5th to 95th percentile) by 1 ADU, since each median is taken over
// thousands of pixels and pixel noise barely moves it. Light reaching the
// sensor adds a gradient and dust shadows: 155 ADU in a dark taken with the
// cover off in daylight, and a ramp from 2 to 14 ADU over a set whose last
// frames were taken as the morning brightened. A dark is leaky when its
// spread exceeds maxCleanADU.
const maxCleanADU = 3

func leaky(spread float64) bool { return spread > maxCleanADU }

// darkSpread measures a raw dark's large-scale structure and pixel noise,
// both in 16-bit ADU.
func darkSpread(d []float32, w, h int) (spread, noise float64) {
	const adu = 65535
	med := func(v []float32) float32 {
		s := slices.Clone(v)
		slices.Sort(s)
		return s[len(s)/2]
	}
	sample := make([]float32, 0, len(d)/16+1)
	for i := 0; i < len(d); i += 16 {
		sample = append(sample, d[i])
	}
	m := med(sample)
	for i, v := range sample {
		sample[i] = float32(math.Abs(float64(v - m)))
	}
	noise = 1.4826 * float64(med(sample)) * adu
	const nx, ny = 32, 24
	blocks := make([]float32, 0, nx*ny)
	v := make([]float32, 0, (w/nx+1)*(h/ny+1)/4)
	for by := range ny {
		for bx := range nx {
			v = v[:0]
			for y := by * h / ny; y < (by+1)*h/ny; y += 2 {
				for x := bx * w / nx; x < (bx+1)*w/nx; x += 2 {
					v = append(v, d[y*w+x])
				}
			}
			blocks = append(blocks, med(v))
		}
	}
	slices.Sort(blocks)
	return float64(blocks[len(blocks)*95/100]-blocks[len(blocks)*5/100]) * adu, noise
}

// dropLeakyDarks checks each downloaded dark for light and removes the
// leaky ones from files, recording the measure on every frame and the leak
// on the leaky ones. It returns how many are left.
func (p *Pipeline) dropLeakyDarks(ctx context.Context, frames []app.Frame, files []string) (int, error) {
	left := 0
	for i, f := range frames {
		b, err := os.ReadFile(files[i])
		if err != nil {
			return 0, err
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", f.Key, err)
		}
		spread, noise := darkSpread(im.Data, im.W, im.H)
		var leak *float64
		if leaky(spread) {
			leak = &spread
			slog.Warn("Leaving out a dark with a light leak", "key", f.Key, "spread_adu", math.Round(spread), "noise_adu", math.Round(noise))
			if err := os.Remove(files[i]); err != nil {
				return 0, err
			}
		} else {
			left++
		}
		if err := p.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).
			UpdateColumns(map[string]any{"light_leak": leak, "dark_spread": spread}).Error; err != nil {
			return 0, err
		}
	}
	return left, nil
}
