package minima

import (
	"fmt"
	"math"
)

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %s", n, word)
}

func daysText(d float64) string {
	switch {
	case d < 1:
		return "one night"
	case d < 2*365.25:
		return fmt.Sprintf("%.0f days", d)
	}
	return fmt.Sprintf("%.1f years", d/365.25)
}

// countOver is "2 of the second over 40 days", "1 of the second" or
// "none of the second".
func countOver(n int, what string, span float64) string {
	switch n {
	case 0:
		return "none " + what
	case 1:
		return "1 " + what
	}
	return fmt.Sprintf("%d %s over %s", n, what, daysText(span))
}

func needText(n, wantN int, what string, cycles, wantCycles int) string {
	return fmt.Sprintf("Needs at least %d %s on %d different nights; has %s on %s.", wantN, what, wantCycles,
		plural(n, "timing"), plural(cycles, "night"))
}

func curveNeed(n int, span float64, seasons int) string {
	return fmt.Sprintf("Needs at least %d timings over a year or more, in %d seasons; has %s over %s in %s.", minCurveN, minCurveSeasons,
		plural(n, "timing"), daysText(span), plural(seasons, "season"))
}

// OneIn is a chance in words, "1 in 2,400", to two figures.
func OneIn(p float64) string {
	if !(p > 0) || 1/p >= 1e6 {
		return "less than 1 in a million"
	}
	n := 1 / p
	mag := math.Pow(10, math.Max(0, math.Floor(math.Log10(n))-1))
	return "1 in " + thousands(int(math.Round(n/mag)*mag))
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
