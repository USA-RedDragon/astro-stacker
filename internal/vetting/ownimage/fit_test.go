package ownimage

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

func TestCloseNeighboursAreMeasuredWithTheStar(t *testing.T) {
	t.Parallel()
	// Source 15523 at 1.9"/px: G 18.7 and G 19.5 neighbours 0.88 and 1.37
	// px from the G 16.9 star, star width 1.25 px.
	const side, sigma, flux, sky, noise = 41, 1.25, 1.0, 0.1, 0.004
	c := float64(side / 2)
	stars := [][3]float64{{c, c, flux}, {c + 0.80, c + 0.37, 0.18}, {c - 0.87, c + 1.06, 0.09}, {c - 0.18, c + 5.90, 0.4}}
	lone := &starModel{x: c, y: c, nbs: [][2]float64{{c, c}, {c - 0.18, c + 5.90}}, g: 16.9}
	m := &starModel{x: c, y: c, g: 16.9}
	for _, s := range stars {
		m.nbs = append(m.nbs, [2]float64{s[0], s[1]})
	}
	rng := rand.New(rand.NewPCG(15523, 1))
	measure := func(model *starModel, blend [][3]float64) (spread, err, avg float64) {
		var fs, es []float64
		for range 200 {
			p := &discoveries.Plane{W: side, H: side, Data: make([]float32, side*side)}
			for k := range p.Data {
				x, y := float64(k%side), float64(k/side)
				v := sky + noise*rng.NormFloat64()
				for _, s := range blend {
					r2 := (x-s[0])*(x-s[0]) + (y-s[1])*(y-s[1])
					v += s[2] * math.Exp(-r2/(2*sigma*sigma)) / (2 * math.Pi * sigma * sigma)
				}
				p.Data[k] = float32(v)
			}
			o := fitStar(p, model, sigma, 0, 0, true)
			if !o.ok {
				t.Fatal("no fit")
			}
			fs, es = append(fs, o.flux), append(es, o.err)
		}
		avg = mean(fs)
		for _, f := range fs {
			spread += (f - avg) * (f - avg)
		}
		return math.Sqrt(spread/float64(len(fs)-1)) / avg, median(es) / avg, avg
	}
	soloSpread, _, _ := measure(lone, [][3]float64{stars[0], stars[3]})
	spread, err, mean := measure(m, stars)
	if math.Abs(mean-1.27) > 0.07 {
		t.Errorf("blend measured as %.3f, want the three stars' 1.27", mean)
	}
	if spread > 1.3*soloSpread || err > 1.3*soloSpread {
		t.Errorf("blend scatters %.4f with error %.4f a fit, a lone star %.4f", spread, err, soloSpread)
	}
	if spread > 1.3*err || err > 1.3*spread {
		t.Errorf("blend scatters %.4f but its error says %.4f", spread, err)
	}
}
