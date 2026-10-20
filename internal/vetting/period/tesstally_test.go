package period

import (
	"strings"
	"testing"
)

func tallyCase(good int, extra ...tessMin) []tessMin {
	out := make([]tessMin, 0, good+len(extra)+2)
	for i := range good {
		out = append(out, tessMin{sector: 17 + i%2, lag: float64(i%5) - 2, good: true})
	}
	out = append(out, tessMin{sector: 58, faint: true}, tessMin{sector: 24, lag: 90, shared: true})
	return append(out, extra...)
}

// TestTESSTally rebuilds 2290's TESS row under star:19: 33 timed dips,
// one of them on time to 5.3 min but shallow, which failed the row as
// "1 miss the predicted time or depth, by up to 5.3 min" against a
// timing limit of at least 9.75 min. A lone shallow minimum among 32
// good ones passes and is named; a minimum off its time, a second
// shallow one, or one shallow among few still fails, and the text says
// which way each missed.
func TestTESSTally(t *testing.T) {
	t.Parallel()
	shallow := tessMin{sector: 58, at: 2459900.5, lag: 5.3}
	late := tessMin{sector: 77, lag: -41.2, late: true}
	cases := []struct {
		name   string
		mins   []tessMin
		agrees bool
		n      int
		says   []string
	}{
		{"2290 lone shallow", tallyCase(32, shallow), true, 33, nil},
		{"shallow among few", tallyCase(5, shallow), false, 6,
			[]string{"1 is on time to within 5.3 min but at most half as deep as the same dip in the rest of sector 58"}},
		{"two shallow", tallyCase(31, shallow, tessMin{sector: 84, lag: -2.1}), false, 33,
			[]string{"2 are on time to within 5.3 min", "sectors 58 and 84"}},
		{"lone late", tallyCase(32, late), false, 33, []string{"1 misses the predicted time by up to 41.2 min"}},
		{"late and shallow", tallyCase(32, late, shallow), false, 34,
			[]string{"1 misses the predicted time by up to 41.2 min, and 1 is on time to within 5.3 min"}},
		{"all good", tallyCase(32), true, 32, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := tallyTESS(c.mins)
			if got.agrees() != c.agrees || got.n != c.n {
				t.Fatalf("agrees %v over %d minima, want %v over %d", got.agrees(), got.n, c.agrees, c.n)
			}
			text := got.misses()
			for _, w := range c.says {
				if !strings.Contains(text, w) {
					t.Errorf("misses %q, want %q in it", text, w)
				}
			}
			if c.agrees && text != "" && got.lone == nil {
				t.Errorf("agrees but names misses: %q", text)
			}
			if c.name == "2290 lone shallow" && (got.lone == nil || got.lone.sector != 58) {
				t.Errorf("lone shallow minimum not kept: %+v", got.lone)
			}
		})
	}
}
