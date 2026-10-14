package calmatch

import (
	"sort"
	"time"
)

// Epoch is the start of a dust epoch: from its first night on, the dust
// shadows on the sensor are different from the nights before. A flat is
// never taken across a boundary while the lights' own epoch has one.
type Epoch struct {
	// Start is the epoch's first night.
	Start time.Time
	// Fallback says which flats lights of this epoch get when it has none
	// in their filter (FallbackPrevious, FallbackNext, or "" for the
	// nearest night).
	Fallback string
	// Note says what changed and how the boundary was dated.
	Note string
}

// What lights get when their dust epoch has no flat in their filter.
const (
	// FallbackPrevious takes the flats of the nearest earlier epoch that
	// has one: they correct the vignetting but not the dust that came at
	// the boundary.
	FallbackPrevious = "previous"
	// FallbackNext takes the flats of the nearest later epoch that has one:
	// they correct the dust that came at the boundary but add a bright copy
	// of any dust that came later.
	FallbackNext = "next"
)

// Reasons a flat from another epoch was chosen (Match.FallbackWhy).
const (
	WhyEpochPrevious = "no flat in the lights' dust epoch; flat from an earlier epoch"
	WhyEpochNext     = "no flat in the lights' dust epoch; flat from a later epoch"
	WhyEpochNearest  = "no flat in the lights' dust epoch; nearest flat from another epoch"
)

// Epochs are the dust epoch boundaries, any order.
type Epochs []Epoch

func (e Epochs) sorted() Epochs {
	out := append(Epochs(nil), e...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Of is the dust epoch of a night: 0 before the first boundary, i after the
// i-th (in date order).
func (e Epochs) Of(night time.Time) int {
	n := 0
	for _, b := range e {
		if !night.Before(b.Start) {
			n++
		}
	}
	return n
}

// fallback is the policy for lights in epoch i.
func (e Epochs) fallback(i int) string {
	if i == 0 {
		return ""
	}
	return e.sorted()[i-1].Fallback
}

// flatCandidates narrows flats to those of the night's epoch, or, when it
// has none, to the epoch its Fallback names, and says why when that is
// another epoch.
func (e Epochs) flatCandidates(night time.Time, cands []Set) ([]Set, string) {
	if len(e) == 0 {
		return cands, ""
	}
	want := e.Of(night)
	byEpoch := map[int][]Set{}
	for _, s := range cands {
		byEpoch[e.Of(s.Night)] = append(byEpoch[e.Of(s.Night)], s)
	}
	if own := byEpoch[want]; len(own) > 0 {
		return own, ""
	}
	switch e.fallback(want) {
	case FallbackPrevious:
		for i := want - 1; i >= 0; i-- {
			if len(byEpoch[i]) > 0 {
				return byEpoch[i], WhyEpochPrevious
			}
		}
	case FallbackNext:
		for i := want + 1; i <= len(e); i++ {
			if len(byEpoch[i]) > 0 {
				return byEpoch[i], WhyEpochNext
			}
		}
	}
	best := cands[0]
	for _, s := range cands[1:] {
		if days(night, s.Night) < days(night, best.Night) {
			best = s
		}
	}
	return byEpoch[e.Of(best.Night)], WhyEpochNearest
}
