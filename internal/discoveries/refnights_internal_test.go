package discoveries

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

const filterRed = "Red"

// filterInput is the field's subs in each filter named, each filter's
// drawn with noise of its own and the red source in Red alone; counts are
// the subs of each night, PerNight when nil.
func (f *field) filterInput(t *testing.T, filters []string, counts []int) Input {
	t.Helper()
	m, err := imagedata.Decode(f.masterFITS(t))
	if err != nil {
		t.Fatal(err)
	}
	anchors := Anchors(PlaneOf(m))
	files := map[string][]byte{}
	fis := make([]FilterInput, 0, len(filters))
	id := 1
	for _, name := range filters {
		g := *f
		g.salt, g.red = name, f.red && name == filterRed
		fi := FilterInput{Filter: name, Anchors: anchors}
		for n, nd := range f.Nights {
			k := f.PerNight
			if counts != nil {
				k = counts[n]
			}
			for i := range k {
				key := fmt.Sprintf("registered/T/%s/%d_%d.xisf", name, n, i)
				files[key] = g.xisf(t, n, i)
				s := Sub{FrameID: id, Filter: name, Night: nd, Start: f.subTime(n, i), Exposure: synthSubSeconds, Weight: 1, Key: key}
				id++
				if n == len(f.Nights)-1 {
					fi.Night = append(fi.Night, s)
				} else {
					fi.Ref = append(fi.Ref, s)
				}
			}
		}
		fis = append(fis, fi)
	}
	return Input{W: f.W, H: f.H, WCS: f.WCS, Gaia: f.gaia(), Threads: 2, Filters: fis,
		Load: func(_ context.Context, s Sub) (*Plane, error) {
			b, ok := files[s.Key]
			if !ok {
				return nil, fmt.Errorf("no %s", s.Key)
			}
			im, err := imagedata.Decode(b)
			if err != nil {
				return nil, err
			}
			return PlaneOf(im), nil
		}}
}

// TestReferenceNightCannotMakeAChange replays Andromeda's 2025-01-19: a
// star dimmed on the reference night of most subs reads brighter on a
// night it is not. Its own dimmed night still reads fainter.
func TestReferenceNightCannotMakeAChange(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("a synthetic night takes ten minutes under the race detector; TestRunFindsSyntheticChanges runs the same code there")
	}
	for _, tc := range []struct {
		name string
		dim  int
		sign int
	}{{"eclipse in the reference", 0, 0}, {"eclipse on the night", 3, -1}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newField(t)
			f.steady, f.dim = true, map[int]float64{tc.dim: 0.75}
			res, err := Run(context.Background(), f.filterInput(t, []string{broadbandClear}, []int{18, 3, 3, 6}))
			if err != nil {
				t.Fatal(err)
			}
			v := f.Stars[f.Var]
			got := 0
			for _, c := range res.Candidates {
				if math.Hypot(c.X-v.X, c.Y-v.Y) < 1.5 {
					got = c.Sign
					t.Logf("candidate %+d snr %.1f dmag %v", c.Sign, c.SNR, deref(c.DeltaMag))
				}
			}
			for _, r := range res.Rejected {
				if math.Hypot(r.X-v.X, r.Y-v.Y) < 1.5 {
					t.Logf("rejected %+d snr %.1f: %s", r.Sign, r.SNR, r.Reason)
				}
			}
			if got != tc.sign {
				t.Errorf("the star dimmed on night %d: candidate sign %+d, want %+d", tc.dim, got, tc.sign)
			}
		})
	}
}

func TestNightShares(t *testing.T) {
	t.Parallel()
	day := func(d int) time.Time { return time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC) }
	subs := func(counts ...int) []Sub {
		var out []Sub
		for n, c := range counts {
			for range c {
				out = append(out, Sub{Night: day(n + 1)})
			}
		}
		return out
	}
	weight := func(s []Sub, sh []float64) map[time.Time]float64 {
		w := map[time.Time]float64{}
		for i, x := range s {
			if sh == nil {
				w[x.Night]++
			} else {
				w[x.Night] += sh[i]
			}
		}
		return w
	}
	for _, tc := range []struct {
		counts []int
		most   float64
		none   bool
	}{
		{[]int{28, 1, 7, 2}, 1.0 / 3, false},
		{[]int{12, 12, 12, 12}, 0.25, true},
		{[]int{28, 10}, 0.5, false},
		{[]int{30}, 1, true},
		{[]int{6, 6, 6}, 1.0 / 3, true},
		{[]int{5, 5, 6}, 1.0 / 3, false},
	} {
		s := subs(tc.counts...)
		sh := nightShares(s)
		if (sh == nil) != tc.none {
			t.Errorf("%v: shares %v", tc.counts, sh)
			continue
		}
		var total, top float64
		for _, w := range weight(s, sh) {
			total += w
			top = math.Max(top, w)
		}
		if top/total > tc.most+1e-6 {
			t.Errorf("%v: a night holds %.3f of the weight, want at most %.3f", tc.counts, top/total, tc.most)
		}
	}
}

// TestReferenceNightEnvelope replays 2MASS J00433343+4139446 on Andromeda's
// 2025-01-19, against the light measured in its reference nights' subs.
func TestReferenceNightEnvelope(t *testing.T) {
	t.Parallel()
	night := []float64{0.0233, 0.0289, 0.0198, 0.0116, 0.0226, 0.0246, 0.0312, 0.0274, 0.0220, 0.0165, 0.0177, 0.0193, 0.0154}
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	c := cand{Candidate: Candidate{Sign: 1}, refLight: map[string]float64{broadbandClear: 0.32}}
	for _, v := range night {
		c.Light = append(c.Light, LightPoint{Filter: broadbandClear})
		c.fluxes, c.errs = append(c.fluxes, v), append(c.errs, 0.0018)
	}
	c.refNights = map[string][]refNight{broadbandClear: {
		{night: day(2025, 1, 21), mean: 0.0155, n: 7, w: 3},
		{night: day(2025, 1, 22), mean: 0.0223, n: 2, w: 2},
		{night: day(2025, 11, 16), mean: 0.0001, n: 1, w: 1},
		{night: day(2026, 10, 11), mean: -0.0281, n: 28, w: 3},
	}}
	if why := c.refNightShift(); !strings.Contains(why, "2025-01-22") {
		t.Errorf("as bright on 2025-01-22: %q", why)
	}
	c.refNights[broadbandClear][1].n = 1
	if why := c.refNightShift(); why != "" {
		t.Errorf("one sub of 2025-01-22 does not count: %q", why)
	}
	c.refNights[broadbandClear][1].n = 2
	c.refLight[broadbandClear] = 0.05
	if why := c.refNightShift(); why != "" {
		t.Errorf("a change by a third of the light, as on 2025-01-22: %q", why)
	}
}

// TestReferenceOutburstCannotMakeAFade replays source 23991 in IC 4604
// Panel 6, whose two outburst nights held 0.20 and 0.33 of the reference
// on 2026-03-20, so the quiet star read 0.28 mag fainter: here a star 2.5
// and 1.75 mag up on two of six reference nights with 0.19 and 0.33 of the
// weight. A real fade of 0.3 mag on the night still reads fainter.
func TestReferenceOutburstCannotMakeAFade(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("a synthetic night takes ten minutes under the race detector; TestRunFindsSyntheticChanges runs the same code there")
	}
	for _, tc := range []struct {
		name  string
		night float64
		sign  int
	}{{"quiet on the night", 0, 0}, {"fade on the night", 0.75, -1}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newField(t)
			f.Nights = nil
			for n := range 7 {
				f.Nights = append(f.Nights, time.Date(2026, 3, 1+n, 0, 0, 0, 0, time.UTC))
			}
			f.start = time.Date(2026, 3, 7, 4, 0, 0, 0, time.UTC)
			f.steady, f.dim = true, map[int]float64{0: 10, 1: 5}
			if tc.night > 0 {
				f.dim[6] = tc.night
			}
			res, err := Run(context.Background(), f.filterInput(t, []string{broadbandClear}, []int{6, 11, 4, 2, 7, 2, 6}))
			if err != nil {
				t.Fatal(err)
			}
			v := f.Stars[f.Var]
			got := 0
			for _, c := range res.Candidates {
				if math.Hypot(c.X-v.X, c.Y-v.Y) < 1.5 {
					got = c.Sign
					t.Logf("candidate %+d snr %.1f dmag %v", c.Sign, c.SNR, deref(c.DeltaMag))
				}
			}
			for _, r := range res.Rejected {
				if math.Hypot(r.X-v.X, r.Y-v.Y) < 1.5 {
					t.Logf("rejected %+d snr %.1f: %s", r.Sign, r.SNR, r.Reason)
				}
			}
			if got != tc.sign {
				t.Errorf("candidate sign %+d, want %+d", got, tc.sign)
			}
		})
	}
}
