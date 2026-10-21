package minima

import (
	"runtime"
	"sync"
)

// nightCase is a made-up night with its dip and true middle.
type nightCase struct {
	pts   []Point
	d     Dip
	truth float64
}

// timedNight is Time's result on one night.
type timedNight struct {
	m   Minimum
	why string
	ok  bool
}

// timeNights times each night, as many at once as there are CPUs. The
// nights are made beforehand in order, so the results are those of
// timing them one after another.
func timeNights(ns []nightCase) []timedNight {
	out := make([]timedNight, len(ns))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i := range ns {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			m, why, ok := Time(ns[i].pts, ns[i].d)
			out[i] = timedNight{m, why, ok}
		})
	}
	wg.Wait()
	return out
}
