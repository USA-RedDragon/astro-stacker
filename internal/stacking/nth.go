package stacking

import (
	"cmp"
	"math/bits"
	"slices"
)

// nth32 returns the value slices.Sort would put at index k of s, in linear
// time. It reorders s.
func nth32(s []float32, k int) float32 {
	lo, hi := 0, len(s)-1
	for budget := 2 * bits.Len(uint(len(s))); lo < hi; budget-- {
		if budget == 0 {
			slices.Sort(s[lo : hi+1])
			break
		}
		p := median3(s[lo], s[lo+(hi-lo)/2], s[hi])
		lt, i, gt := lo, lo, hi
		for i <= gt {
			switch cmp.Compare(s[i], p) {
			case -1:
				s[lt], s[i] = s[i], s[lt]
				lt++
				i++
			case 1:
				s[i], s[gt] = s[gt], s[i]
				gt--
			default:
				i++
			}
		}
		switch {
		case k < lt:
			hi = lt - 1
		case k > gt:
			lo = gt + 1
		default:
			return s[k]
		}
	}
	return s[k]
}

func median3(a, b, c float32) float32 {
	if cmp.Less(b, a) {
		a, b = b, a
	}
	if cmp.Less(c, b) {
		b = c
		if cmp.Less(b, a) {
			b = a
		}
	}
	return b
}
