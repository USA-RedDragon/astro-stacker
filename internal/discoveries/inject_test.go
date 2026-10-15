package discoveries_test

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// injected is one star whose night subs were made fainter or brighter.
type injected struct {
	RA, Dec, X, Y, G float64
	DeltaMag         float64
	Found            bool
	SNR              float64
	Measured         *float64
	LowConf          bool
	Class            string
	Present, Subs    int
	Rejected         string
}

// TestInjectRecover scales real stars in a night's real subs by known
// amounts, runs the night, and writes what was found of them and what the
// night's change null test found, as JSON, to DISCOVERIES_INJECT_OUT. The
// data layout is TestDiagnoseNight's: DISCOVERIES_INJECT_DIR,
// DISCOVERIES_INJECT_NIGHT and DISCOVERIES_INJECT_EPOCH. Stars are G 12.5
// to 17.5, at least 100 px from the edges, with no other Gaia star brighter
// than G 19 within 12 arcsec and no VSX entry within 30 arcsec.
func TestInjectRecover(t *testing.T) {
	t.Parallel()
	dir, nightS, out := os.Getenv("DISCOVERIES_INJECT_DIR"), os.Getenv("DISCOVERIES_INJECT_NIGHT"), os.Getenv("DISCOVERIES_INJECT_OUT")
	if dir == "" || out == "" {
		t.Skip("DISCOVERIES_INJECT_DIR or DISCOVERIES_INJECT_OUT not set")
	}
	night, err := time.Parse(time.DateOnly, nightS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := nightInput(t, dir, night, colourFilters())
	vsxB, err := os.ReadFile(filepath.Join(dir, "vsx.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	vsx := parseVSX(t, string(vsxB))
	stars := pickInjections(in, vsx, 320)
	nightIDs := map[int]bool{}
	for _, f := range in.Filters {
		for _, s := range f.Night {
			nightIDs[s.FrameID] = true
		}
	}
	load := in.Load
	in.Load = func(ctx context.Context, s discoveries.Sub) (*discoveries.Plane, error) {
		p, err := load(ctx, s)
		if err != nil || !nightIDs[s.FrameID] {
			return p, err
		}
		for _, st := range stars {
			scaleStar(p, st.X, st.Y, math.Pow(10, -0.4*st.DeltaMag))
		}
		return p, nil
	}
	res, err := discoveries.Run(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	labelled, tracks, checked, failed := discoveries.Classify(ctx, res, discoveries.Known{Variables: vsx}, nil)
	t.Logf("%d tracks, checked %v, failed %v", len(tracks), checked, failed)
	for i := range stars {
		st := &stars[i]
		best := 2.0
		for _, l := range labelled {
			if d := math.Hypot(l.X-st.X, l.Y-st.Y); d <= best && l.Sign == sign(st.DeltaMag) {
				best = d
				st.Found, st.SNR, st.Measured, st.LowConf, st.Class, st.Present, st.Subs = true, l.SNR, l.DeltaMag, l.LowConf, l.Class, l.PresentIn, l.SubsTested
			}
		}
		if !st.Found {
			for _, r := range res.Rejected {
				if math.Hypot(r.X-st.X, r.Y-st.Y) <= 2 && r.Sign == sign(st.DeltaMag) {
					st.Rejected, st.SNR = r.Reason, r.SNR
				}
			}
		}
	}
	b, err := json.Marshal(map[string]any{"night": nightS, "dir": filepath.Base(dir), "night_subs": res.NightSubs, "injected": stars, "null": res.ChangeNull})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, s := range stars {
		if s.Found {
			found++
		}
	}
	t.Logf("injected %d stars, found %d; null test %d changes", len(stars), found, len(res.ChangeNull.Changes))
}

func sign(dm float64) int {
	if dm > 0 {
		return -1
	}
	return 1
}

func pickInjections(in discoveries.Input, vsx []discoveries.Variable, n int) []injected {
	grid := map[[2]int][]discoveries.Star{}
	cell := func(ra, dec float64) [2]int {
		return [2]int{int(math.Floor(ra * 3600 / 30)), int(math.Floor(dec * 3600 / 30))}
	}
	for _, s := range in.Gaia {
		if s.G < 19 {
			k := cell(s.RA, s.Dec)
			grid[k] = append(grid[k], s)
		}
	}
	near := func(ra, dec float64, list func(k [2]int) int) int {
		k := cell(ra, dec)
		c := 0
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				c += list([2]int{k[0] + dx, k[1] + dy})
			}
		}
		return c
	}
	rng := rand.New(rand.NewPCG(11, 13))
	var out []injected
	for _, i := range rng.Perm(len(in.Gaia)) {
		s := in.Gaia[i]
		if s.G < 12.5 || s.G > 17.5 {
			continue
		}
		x, y, ok := in.WCS.PixelOf(s.RA, s.Dec)
		if !ok || x < 100 || y < 100 || x > float64(in.W-100) || y > float64(in.H-100) {
			continue
		}
		crowded := near(s.RA, s.Dec, func(k [2]int) int {
			c := 0
			for _, o := range grid[k] {
				if o != s && discoveries.Separation(s.RA, s.Dec, o.RA, o.Dec)*3600 < 12 {
					c++
				}
			}
			return c
		})
		if crowded > 0 {
			continue
		}
		variable := false
		for _, v := range vsx {
			if discoveries.Separation(s.RA, s.Dec, v.RA, v.Dec)*3600 < 30 {
				variable = true
				break
			}
		}
		if variable {
			continue
		}
		amps := []float64{0.03, 0.05, 0.07, 0.1, 0.15, 0.2, 0.3, 0.5}
		k := len(out)
		dm := amps[k%len(amps)]
		if (k/len(amps))%2 == 1 {
			dm = -dm
		}
		out = append(out, injected{RA: s.RA, Dec: s.Dec, X: x, Y: y, G: s.G, DeltaMag: dm})
		if len(out) == n {
			break
		}
	}
	return out
}

// scaleStar multiplies a star's light above its local sky by f: the sky is
// the median of an annulus 9 to 13 px out, and the light is scaled within
// 7 px.
func scaleStar(p *discoveries.Plane, x, y, f float64) {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	var ring []float64
	for dy := -13; dy <= 13; dy++ {
		for dx := -13; dx <= 13; dx++ {
			r := math.Hypot(float64(dx), float64(dy))
			px, py := cx+dx, cy+dy
			if r < 9 || r > 13 || px < 0 || py < 0 || px >= p.W || py >= p.H {
				continue
			}
			if v := float64(p.Data[py*p.W+px]); !math.IsNaN(v) {
				ring = append(ring, v)
			}
		}
	}
	if len(ring) < 20 {
		return
	}
	sky := medianOf(ring)
	for dy := -7; dy <= 7; dy++ {
		for dx := -7; dx <= 7; dx++ {
			px, py := cx+dx, cy+dy
			if math.Hypot(float64(dx), float64(dy)) > 7 || px < 0 || py < 0 || px >= p.W || py >= p.H {
				continue
			}
			v := float64(p.Data[py*p.W+px])
			if !math.IsNaN(v) {
				p.Data[py*p.W+px] = float32(sky + f*(v-sky))
			}
		}
	}
}

func medianOf(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}
