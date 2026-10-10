package mosaics

import (
	"math"
	"sort"
)

type Rig struct {
	WidthDeg    float64 `json:"widthDeg"`
	HeightDeg   float64 `json:"heightDeg"`
	ScaleArcsec float64 `json:"scaleArcsec"`
}

func DefaultRig() Rig {
	return Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
}

type Point struct {
	RA  float64 `json:"ra"`
	Dec float64 `json:"dec"`
}

type Footprint [4]Point

type Gap struct {
	Fraction float64 `json:"fraction"`
	AreaDeg2 float64 `json:"areaDeg2"`
	Where    string  `json:"where"`
}

const deg = math.Pi / 180

type vec2 struct{ x, y float64 }

type polygon []vec2

type vec3 [3]float64

func sinD(d float64) float64 { return math.Sin(d * deg) }
func cosD(d float64) float64 { return math.Cos(d * deg) }

func Project(centre, p Point) (xi, eta float64) {
	d0, d, da := centre.Dec*deg, p.Dec*deg, (p.RA-centre.RA)*deg
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(da)
	if cosc < 1e-9 {
		cosc = 1e-9
	}
	xi = math.Cos(d) * math.Sin(da) / cosc / deg
	eta = (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(da)) / cosc / deg
	return xi, eta
}

func Deproject(centre Point, xi, eta float64) Point {
	x, y, d0 := xi*deg, eta*deg, centre.Dec*deg
	den := math.Cos(d0) - y*math.Sin(d0)
	ra := centre.RA + math.Atan2(x, den)/deg
	dec := math.Atan2(math.Sin(d0)+y*math.Cos(d0), math.Hypot(x, den)) / deg
	return Point{RA: wrap360(ra), Dec: dec}
}

func wrap360(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}

func wrap180(a float64) float64 {
	a = wrap360(a)
	if a > 180 {
		a -= 360
	}
	return a
}

func frameAxes(rotationDeg float64) (long, up vec2) {
	s, c := sinD(rotationDeg), cosD(rotationDeg)
	return vec2{c, -s}, vec2{s, c}
}

func Offset(centre Point, rotationDeg, dx, dy float64) Point {
	long, up := frameAxes(rotationDeg)
	return Deproject(centre, dx*long.x+dy*up.x, dx*long.y+dy*up.y)
}

func PanelFootprint(centre Point, rotationDeg float64, rig Rig) Footprint {
	w, h := rig.WidthDeg/2, rig.HeightDeg/2
	return Footprint{
		Offset(centre, rotationDeg, w, h),
		Offset(centre, rotationDeg, -w, h),
		Offset(centre, rotationDeg, -w, -h),
		Offset(centre, rotationDeg, w, -h),
	}
}

func unit(p Point) vec3 {
	return vec3{cosD(p.Dec) * cosD(p.RA), cosD(p.Dec) * sinD(p.RA), sinD(p.Dec)}
}

func meanPoint(ps []Point) Point {
	var s vec3
	for _, p := range ps {
		u := unit(p)
		s[0], s[1], s[2] = s[0]+u[0], s[1]+u[1], s[2]+u[2]
	}
	return Point{RA: wrap360(math.Atan2(s[1], s[0]) / deg), Dec: math.Atan2(s[2], math.Hypot(s[0], s[1])) / deg}
}

func centreOf(f Footprint) Point { return meanPoint(f[:]) }

func separation(a, b Point) float64 {
	c := sinD(a.Dec)*sinD(b.Dec) + cosD(a.Dec)*cosD(b.Dec)*cosD(a.RA-b.RA)
	return math.Acos(math.Max(-1, math.Min(1, c))) / deg
}

func radius(f Footprint, c Point) float64 {
	r := 0.0
	for _, p := range f {
		r = math.Max(r, separation(c, p))
	}
	return r
}

func projectFootprint(c Point, f Footprint) polygon {
	out := make(polygon, len(f))
	for i, p := range f {
		x, y := Project(c, p)
		out[i] = vec2{x, y}
	}
	if signedArea(out) < 0 {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

func signedArea(p polygon) float64 {
	s := 0.0
	for i := range p {
		j := (i + 1) % len(p)
		s += p[i].x*p[j].y - p[j].x*p[i].y
	}
	return s / 2
}

func cross(a, b, p vec2) float64 {
	return (b.x-a.x)*(p.y-a.y) - (b.y-a.y)*(p.x-a.x)
}

func intersect(p, q, a, b vec2) vec2 {
	cp, cq := cross(a, b, p), cross(a, b, q)
	t := cp / (cp - cq)
	return vec2{p.x + t*(q.x-p.x), p.y + t*(q.y-p.y)}
}

func clip(subject, clipper polygon) polygon {
	out := subject
	for i := range clipper {
		if len(out) == 0 {
			break
		}
		a, b := clipper[i], clipper[(i+1)%len(clipper)]
		in := out
		out = make(polygon, 0, len(in)+2)
		for j := range in {
			p, q := in[j], in[(j+1)%len(in)]
			pin, qin := cross(a, b, p) >= 0, cross(a, b, q) >= 0
			switch {
			case pin && qin:
				out = append(out, q)
			case pin:
				out = append(out, intersect(p, q, a, b))
			case qin:
				out = append(out, intersect(p, q, a, b), q)
			}
		}
	}
	return out
}

func (p polygon) contains(v vec2) bool {
	for i := range p {
		if cross(p[i], p[(i+1)%len(p)], v) < 0 {
			return false
		}
	}
	return true
}

func bilinear(p polygon, u, v float64) vec2 {
	w0, w1, w2, w3 := (1-u)*(1-v), u*(1-v), u*v, (1-u)*v
	return vec2{
		w0*p[0].x + w1*p[1].x + w2*p[2].x + w3*p[3].x,
		w0*p[0].y + w1*p[1].y + w2*p[2].y + w3*p[3].y,
	}
}

func OverlapFraction(a, b Footprint) float64 {
	ca, cb := centreOf(a), centreOf(b)
	if separation(ca, cb) > radius(a, ca)+radius(b, cb) {
		return 0
	}
	pa, pb := projectFootprint(ca, a), projectFootprint(ca, b)
	smaller := math.Min(signedArea(pa), signedArea(pb))
	if smaller <= 0 {
		return 0
	}
	inter := clip(pb, pa)
	if len(inter) < 3 {
		return 0
	}
	return math.Max(0, math.Min(1, math.Abs(signedArea(inter))/smaller))
}

func Area(f Footprint) float64 {
	return math.Abs(signedArea(projectFootprint(centreOf(f), f)))
}

func Neighbours(fps []Footprint, minFraction float64) [][]int {
	out := make([][]int, len(fps))
	for i := range fps {
		out[i] = []int{}
	}
	for i := range fps {
		for j := i + 1; j < len(fps); j++ {
			f := OverlapFraction(fps[i], fps[j])
			if f > 0 && f >= minFraction {
				out[i] = append(out[i], j)
				out[j] = append(out[j], i)
			}
		}
	}
	for i := range out {
		sort.Ints(out[i])
	}
	return out
}

func GridCells(centres []Point, rotationDeg float64, rig Rig) (rows, cols []int) {
	if len(centres) == 0 {
		return []int{}, []int{}
	}
	c := meanPoint(centres)
	long, up := frameAxes(rotationDeg)
	along, across := make([]float64, len(centres)), make([]float64, len(centres))
	for i, p := range centres {
		x, y := Project(c, p)
		along[i] = x*long.x + y*long.y
		across[i] = x*up.x + y*up.y
	}
	return cluster(across, rig.HeightDeg/2), cluster(along, rig.WidthDeg/2)
}

func cluster(v []float64, tol float64) []int {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] > v[idx[b]] })
	out := make([]int, len(v))
	k, anchor := -1, 0.0
	for _, i := range idx {
		if k < 0 || anchor-v[i] > tol {
			k++
			anchor = v[i]
		}
		out[i] = k
	}
	return out
}

func CoverageGap(planned Footprint, actual []Footprint) Gap {
	c := centreOf(planned)
	pp := projectFootprint(c, planned)
	polys := make([]polygon, 0, len(actual))
	for _, a := range actual {
		if separation(c, centreOf(a)) < 60 {
			polys = append(polys, projectFootprint(c, a))
		}
	}
	const nu, nv = 60, 40
	miss := 0
	var su, sv, sx, sy float64
	for i := range nu {
		for j := range nv {
			u, v := (float64(i)+0.5)/nu, (float64(j)+0.5)/nv
			p := bilinear(pp, u, v)
			if coveredBy(polys, p) {
				continue
			}
			miss++
			su, sv, sx, sy = su+u, sv+v, sx+p.x, sy+p.y
		}
	}
	frac := float64(miss) / (nu * nv)
	g := Gap{Fraction: frac, AreaDeg2: frac * math.Abs(signedArea(pp))}
	if miss == 0 {
		return g
	}
	m := float64(miss)
	g.Where = where(su/m-0.5, sv/m-0.5, sx/m, sy/m)
	return g
}

func coveredBy(polys []polygon, p vec2) bool {
	for _, q := range polys {
		if q.contains(p) {
			return true
		}
	}
	return false
}

func where(du, dv, x, y float64) string {
	a, b := math.Abs(du)/0.5, math.Abs(dv)/0.5
	hi, lo := math.Max(a, b), math.Min(a, b)
	if hi < 0.2 {
		return "centre"
	}
	dir := compass(math.Atan2(x, y) / deg)
	if lo/hi > 0.5 {
		return dir + " corner"
	}
	return dir + " edge"
}

func compass(pa float64) string {
	names := [8]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	return names[int(math.Round(wrap360(pa)/45))%8]
}
