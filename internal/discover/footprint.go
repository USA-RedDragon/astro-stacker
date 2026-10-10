package discover

import (
	"encoding/json"
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	FieldFrames   = "frames"
	FieldPointing = "pointing"
	FieldPlan     = "plan"

	minCoverage    = 0.25
	coverageSteps  = 17
	pointObjectDeg = 2.0 / 60
)

type field struct {
	kind           string
	circle         bool
	target         string
	wcs            goals.WCS
	x0, y0, x1, y1 float64
	ra, dec        float64
	radius         float64
}

func (f field) contains(ra, dec float64) bool {
	if f.kind == FieldPointing {
		return false
	}
	if f.circle {
		return catalog.Separation(f.ra, f.dec, ra, dec) <= f.radius
	}
	x, y, ok := f.wcs.ToPixel(ra, dec)
	return ok && x >= f.x0 && x <= f.x1 && y >= f.y0 && y <= f.y1
}

func newWCSField(kind, target string, w goals.WCS, x0, y0, x1, y1 float64) field {
	f := field{kind: kind, target: target, wcs: w, x0: x0, y0: y0, x1: x1, y1: y1}
	f.ra, f.dec = w.ToSky((x0+x1)/2, (y0+y1)/2)
	for _, c := range [][2]float64{{x0, y0}, {x0, y1}, {x1, y0}, {x1, y1}} {
		ra, dec := w.ToSky(c[0], c[1])
		f.radius = math.Max(f.radius, catalog.Separation(f.ra, f.dec, ra, dec))
	}
	return f
}

func parseWCS(cardsJSON string) (goals.WCS, bool) {
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(cardsJSON), &cards); err != nil {
		return goals.WCS{}, false
	}
	kw := frameheader.Keywords{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	return goals.ParseWCS(kw)
}

func framesField(target, wcs string, w, h int, crop *[4]int) (field, bool) {
	g, ok := parseWCS(wcs)
	if !ok || w <= 0 || h <= 0 {
		return field{}, false
	}
	x0, y0, x1, y1 := 0.5, 0.5, float64(w)+0.5, float64(h)+0.5
	if crop != nil && crop[2] > 0 && crop[3] > 0 {
		x0, y0 = float64(crop[0])+0.5, float64(crop[1])+0.5
		x1, y1 = x0+float64(crop[2]), y0+float64(crop[3])
	}
	return newWCSField(FieldFrames, target, g, x0, y0, x1, y1), true
}

func planField(target string, ra, dec, rotation float64, frame sky.Frame) (field, bool) {
	scale := frame.Scale() / 3600
	if !(scale > 0) || frame.WidthPx <= 0 || frame.HeightPx <= 0 {
		return field{}, false
	}
	th := rotation * math.Pi / 180
	w, h := float64(frame.WidthPx), float64(frame.HeightPx)
	g := goals.WCS{RA0: ra, Dec0: dec, PX0: (w + 1) / 2, PY0: (h + 1) / 2,
		CD: [2][2]float64{{-scale * math.Cos(th), scale * math.Sin(th)}, {scale * math.Sin(th), scale * math.Cos(th)}}}
	return newWCSField(FieldPlan, target, g, 0.5, 0.5, w+0.5, h+0.5), true
}

func circleField(kind, target string, ra, dec, radius float64) field {
	return field{kind: kind, circle: true, target: target, ra: ra, dec: dec, radius: radius}
}

func pointingField(target string, ra, dec float64) field {
	return field{kind: FieldPointing, target: target, ra: ra, dec: dec}
}

type cover struct {
	fraction float64
	centre   bool
	pointing bool
	nearest  float64
	targets  []string
}

func (c cover) hit() bool {
	return c.centre || c.pointing || c.fraction >= minCoverage
}

func semiAxes(o catalog.Object) (float64, float64) {
	a := o.MajorArcmin / 120
	b := o.Minor() / 120
	if b <= 0 || b > a {
		b = a
	}
	if o.PA == nil && b < a {
		r := math.Sqrt(a * b)
		return r, r
	}
	return a, b
}

func offset(o catalog.Object, east, north float64) (float64, float64) {
	r0, d0 := o.RA*math.Pi/180, o.Dec*math.Pi/180
	x, y := east*math.Pi/180, north*math.Pi/180
	rho := math.Hypot(x, y)
	if rho == 0 {
		return o.RA, o.Dec
	}
	c := math.Atan(rho)
	dec := math.Asin(math.Cos(c)*math.Sin(d0) + y*math.Sin(c)*math.Cos(d0)/rho)
	ra := r0 + math.Atan2(x*math.Sin(c), rho*math.Cos(d0)*math.Cos(c)-y*math.Sin(d0)*math.Sin(c))
	return math.Mod(ra*180/math.Pi+360, 360), dec * 180 / math.Pi
}

func ellipseContains(o catalog.Object, ra, dec float64) bool {
	a, b := semiAxes(o)
	a, b = math.Max(a, pointObjectDeg), math.Max(b, pointObjectDeg)
	d0, d, dr := o.Dec*math.Pi/180, dec*math.Pi/180, (ra-o.RA)*math.Pi/180
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(dr)
	if cosc <= 0 {
		return false
	}
	east := math.Cos(d) * math.Sin(dr) / cosc * 180 / math.Pi
	north := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(dr)) / cosc * 180 / math.Pi
	pa := o.PAOr(0) * math.Pi / 180
	u := east*math.Sin(pa) + north*math.Cos(pa)
	v := east*math.Cos(pa) - north*math.Sin(pa)
	return (u/a)*(u/a)+(v/b)*(v/b) <= 1
}

func reaches(f field, o catalog.Object) bool {
	a, _ := semiAxes(o)
	return math.Abs(f.dec-o.Dec) <= f.radius+a+pointObjectDeg && catalog.Separation(f.ra, f.dec, o.RA, o.Dec) <= f.radius+a+pointObjectDeg
}

func measureCover(o catalog.Object, fields []field) cover {
	c := cover{nearest: -1}
	var near []field
	for _, f := range fields {
		d := catalog.Separation(f.ra, f.dec, o.RA, o.Dec)
		if c.nearest < 0 || d < c.nearest {
			c.nearest = d
		}
		if reaches(f, o) {
			near = append(near, f)
		}
	}
	if len(near) == 0 {
		return c
	}
	used := map[string]bool{}
	inside := func(ra, dec float64) bool {
		hit := false
		for _, f := range near {
			if f.contains(ra, dec) {
				used[f.target] = true
				hit = true
			}
		}
		return hit
	}
	c.centre = inside(o.RA, o.Dec)
	for _, f := range near {
		if f.kind == FieldPointing && ellipseContains(o, f.ra, f.dec) {
			c.pointing = true
			used[f.target] = true
		}
	}
	a, b := semiAxes(o)
	if a <= 0 {
		if c.centre {
			c.fraction = 1
		}
	} else {
		pa := o.PAOr(0) * math.Pi / 180
		total, in := 0, 0
		for i := range coverageSteps {
			for j := range coverageSteps {
				u := 2*(float64(i)+0.5)/coverageSteps - 1
				v := 2*(float64(j)+0.5)/coverageSteps - 1
				if u*u+v*v > 1 {
					continue
				}
				east := u*a*math.Sin(pa) + v*b*math.Cos(pa)
				north := u*a*math.Cos(pa) - v*b*math.Sin(pa)
				total++
				if inside(offset(o, east, north)) {
					in++
				}
			}
		}
		c.fraction = float64(in) / float64(total)
	}
	for t := range used {
		c.targets = append(c.targets, t)
	}
	slices.Sort(c.targets)
	return c
}
