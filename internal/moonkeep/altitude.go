package moonkeep

import (
	"fmt"
	"math"
)

const (
	// AltitudeFallback is the minimum altitude used where the history is
	// too thin to show a floor.
	AltitudeFallback = 15.0
	// AltitudeWorthBelow is the share of the kept rate high in the sky
	// that a bin's upper bound must reach to stay above the floor.
	AltitudeWorthBelow = 0.5
	// AltitudeReferenceFrom is where the reference bin starts: subs at or
	// above it are the class's kept rate high in the sky.
	AltitudeReferenceFrom = 40.0
)

const (
	FloorKeptRate = "kept-rate"
	FloorThin     = "thin"
	FloorNoData   = "no-data"
)

// AltitudeEdges are the bins of the target's altitude at mid-exposure, in
// degrees. The last bin is the reference.
func AltitudeEdges() []float64 {
	return []float64{0, 5, 10, 15, 20, 25, 30, 35, AltitudeReferenceFrom, 90}
}

// AltitudeRow is the kept rate of one filter class in one target altitude
// bin, with the Moon down.
type AltitudeRow struct {
	Narrowband bool    `json:"narrowband"`
	From       float64 `json:"from"`
	To         float64 `json:"to"`
	Subs       int     `json:"subs"`
	Kept       int     `json:"kept"`
	Nights     int     `json:"nights"`
	Rate       float64 `json:"rate"`
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Worth      float64 `json:"worth"`
	Usable     bool    `json:"usable"`
	Below      bool    `json:"below"`
}

// AltitudeFloor is the lowest altitude a filter class is worth imaging at:
// below it the upper bound of the kept rate is under AltitudeWorthBelow of
// the class's rate high in the sky.
type AltitudeFloor struct {
	Narrowband bool          `json:"narrowband"`
	Floor      float64       `json:"floor"`
	Basis      string        `json:"basis"`
	Reason     string        `json:"reason"`
	Reference  float64       `json:"reference_rate"`
	Rows       []AltitudeRow `json:"rows"`
}

func altitudeBin(alt float64) int {
	e := AltitudeEdges()
	if math.IsNaN(alt) || alt < e[0] || alt > e[len(e)-1] {
		return -1
	}
	for i := 1; i < len(e); i++ {
		if alt < e[i] {
			return i - 1
		}
	}
	return len(e) - 2
}

func boundsOf(c *counts) (rate, low, high float64) {
	if c == nil || c.subs == 0 {
		return 0, 0, 1
	}
	d := c.deff()
	if d <= 0 {
		d = 1
	}
	d = math.Max(1, math.Min(d, float64(c.subs)/float64(len(c.nights))))
	k, n := float64(c.kept)/d, float64(c.subs)/d
	a, b := k+0.5, n-k+0.5
	return float64(c.kept) / float64(c.subs), BetaQuantile(1-Confidence, a, b), BetaQuantile(Confidence, a, b)
}

// FitAltitude fits the altitude floor of broadband and narrowband filters
// from subs taken with the Moon down, so the Moon's own effect is left to
// the Moon rows.
func FitAltitude(samples []Sample) []AltitudeFloor { return fitAltitude(samples, AltitudeWorthBelow) }

func fitAltitude(samples []Sample, worthBelow float64) []AltitudeFloor {
	e := AltitudeEdges()
	agg := map[bool][]*counts{false: make([]*counts, len(e)-1), true: make([]*counts, len(e)-1)}
	for _, s := range samples {
		if s.Moon.Up() {
			continue
		}
		b := altitudeBin(s.Altitude)
		if b < 0 {
			continue
		}
		if agg[s.Narrowband][b] == nil {
			agg[s.Narrowband][b] = &counts{}
		}
		agg[s.Narrowband][b].add(s)
	}
	out := make([]AltitudeFloor, 0, 2)
	for _, nb := range []bool{false, true} {
		out = append(out, floorOf(nb, agg[nb], worthBelow))
	}
	return out
}

func floorOf(nb bool, bins []*counts, worthBelow float64) AltitudeFloor {
	e := AltitudeEdges()
	f := AltitudeFloor{Narrowband: nb, Rows: make([]AltitudeRow, len(bins))}
	for i, c := range bins {
		r := AltitudeRow{Narrowband: nb, From: e[i], To: e[i+1]}
		if c != nil {
			r.Subs, r.Kept, r.Nights = c.subs, c.kept, len(c.nights)
			rate, low, high := boundsOf(c)
			r.Rate, r.Low, r.High = round(rate), round(low), round(high)
		}
		r.Usable = r.Subs >= MinSubs && r.Nights >= MinNights
		f.Rows[i] = r
	}
	ref := &f.Rows[len(f.Rows)-1]
	if !ref.Usable {
		f.Floor, f.Basis = AltitudeFallback, FloorNoData
		f.Reason = fmt.Sprintf("fewer than %d subs on %d nights above %g° with the Moon down, so the floor is %g°", MinSubs, MinNights, AltitudeReferenceFrom, AltitudeFallback)
		return f
	}
	f.Reference = ref.Rate
	limit := worthBelow * ref.Rate
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Subs > 0 && ref.Rate > 0 {
			r.Worth = round(math.Min(1, r.Rate/ref.Rate))
		}
		r.Below = r.Usable && r.High < limit
	}
	f.Floor, f.Basis = 0, FloorKeptRate
	f.Reason = "every bin down to the horizon keeps enough"
	for i := len(f.Rows) - 2; i >= 0; i-- {
		r := f.Rows[i]
		if !r.Usable {
			f.Floor = math.Min(r.To, AltitudeFallback)
			f.Basis = FloorThin
			f.Reason = fmt.Sprintf("kept enough down to %g°; %g-%g° has %d subs on %d nights, too few to judge", r.To, r.From, r.To, r.Subs, r.Nights)
			if f.Floor < r.To {
				f.Reason += fmt.Sprintf(", so the floor falls back to %g°", AltitudeFallback)
			}
			return f
		}
		if r.Below {
			f.Floor = r.To
			f.Reason = fmt.Sprintf("%g-%g° kept %d of %d (%.0f%%, at most %.0f%%), under %.0f%% of the %.0f%% kept above %g°", r.From, r.To, r.Kept, r.Subs, r.Rate*100, r.High*100, worthBelow*100, ref.Rate*100, AltitudeReferenceFrom)
			return f
		}
	}
	return f
}

// AltitudeFloorFor is the floor of a filter class, AltitudeFallback when
// the fit has none.
func AltitudeFloorFor(floors []AltitudeFloor, narrowband bool) AltitudeFloor {
	for _, f := range floors {
		if f.Narrowband == narrowband {
			return f
		}
	}
	return AltitudeFloor{Narrowband: narrowband, Floor: AltitudeFallback, Basis: FloorNoData, Reason: fmt.Sprintf("no altitude fit yet, so the floor is %g°", AltitudeFallback)}
}

// AltitudeThresholds are the worth thresholds the replay compares.
func AltitudeThresholds() []float64 { return []float64{0.3, 0.4, 0.5, 0.6, 0.7} }

// AltitudeReplayRow is what one worth threshold would have left out of the
// past subs taken with the Moon down.
type AltitudeReplayRow struct {
	WorthBelow     float64 `json:"worth_below"`
	BroadbandFloor float64 `json:"broadband_floor"`
	NarrowFloor    float64 `json:"narrowband_floor"`
	SubsBelow      int     `json:"subs_below"`
	KeptBelow      int     `json:"kept_below"`
	HoursBelow     float64 `json:"hours_below"`
}

// AltitudeReplay is the floors and the subs under them for a few worth
// thresholds, for choosing AltitudeWorthBelow.
func AltitudeReplay(samples []Sample, thresholds []float64) []AltitudeReplayRow {
	out := make([]AltitudeReplayRow, 0, len(thresholds))
	for _, th := range thresholds {
		fl := fitAltitude(samples, th)
		row := AltitudeReplayRow{WorthBelow: th, BroadbandFloor: AltitudeFloorFor(fl, false).Floor, NarrowFloor: AltitudeFloorFor(fl, true).Floor}
		for _, s := range samples {
			if s.Moon.Up() || s.Altitude >= AltitudeFloorFor(fl, s.Narrowband).Floor {
				continue
			}
			row.SubsBelow++
			row.HoursBelow += s.Exposure / 3600
			if s.Kept {
				row.KeptBelow++
			}
		}
		row.HoursBelow = math.Round(row.HoursBelow*10) / 10
		out = append(out, row)
	}
	return out
}
