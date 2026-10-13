package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestNth32MatchesSort(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	for n := 1; n < 300; n++ {
		s := make([]float32, n)
		for i := range s {
			switch r.IntN(6) {
			case 0:
				s[i] = 0
			case 1:
				s[i] = float32(r.IntN(3))
			case 2:
				s[i] = float32(math.NaN())
			default:
				s[i] = float32(r.NormFloat64())
			}
		}
		if n%7 == 0 {
			slices.Sort(s)
		}
		if n%11 == 0 {
			slices.Reverse(s)
		}
		want := slices.Clone(s)
		slices.Sort(want)
		for _, k := range []int{0, n / 2, n - 1, r.IntN(n)} {
			got := nth32(slices.Clone(s), k)
			if got != want[k] && !(math.IsNaN(float64(got)) && math.IsNaN(float64(want[k]))) {
				t.Fatalf("n %d k %d: %v, want %v", n, k, got, want[k])
			}
		}
	}
}
