package quality

import (
	"fmt"
	"math"
	"slices"
	"sort"
)

// SkyBinRatio is how much brighter or darker than a sub's sky the subs it is
// cut against may be: under a bright Moon a clear sub collects 7 to 10 times
// the sky of a moonless one and scores as low, though nothing is wrong with
// it. Against the best subs taken in a similar sky it is judged on what the
// sky let it do; its weight still carries the sky, so it counts for less.
const SkyBinRatio = 2.0

// SkyBinMinSubs is the fewest subs a sky bin needs to judge a sub against.
// A thinner bin is widened, SkyBinRatio again at each step, up to all of the
// target's subs in the filter, the cut as it was before sky bins.
const SkyBinMinSubs = 5

// Clarity is what a sub's weight has apart from the sky: transparency squared
// over HFR to the fourth. Focus, seeing, guiding, trails and cloud lower it,
// moonlight and twilight don't. NaN when the HFR is missing.
func Clarity(hfr, transparency float64) float64 {
	if !(hfr > 0) || math.IsNaN(transparency) {
		return math.NaN()
	}
	return transparency * transparency / math.Pow(hfr, 4)
}

// SkyBin is the subs a sub's score is cut against.
type SkyBin struct {
	// Low and High bound the sky of the subs in the bin, in the units of
	// SubScore.SkyRate; both 0 when the bin is every sub of the target in
	// the filter.
	Low  float64
	High float64
	// Subs is how many subs the bin holds: 0 when the sub has none, and
	// BelowCut takes its target's best.
	Subs int
	// Best and PlainBest are the best Score and PlainScore in the bin.
	Best      float64
	PlainBest float64
}

type skySub struct {
	sky, score, plain float64
}

// skyPool is one target's subs in one filter that count for its best, by sky.
type skyPool struct {
	subs                []skySub // sorted by sky, finite sky only
	best, plainBest     float64
	all                 int
	clarity, plainClear []float64
}

func (p *skyPool) add(sky, score, plain, clarity, plainClarity float64) {
	p.all++
	p.best, p.plainBest = math.Max(p.best, score), math.Max(p.plainBest, plain)
	if sky > 0 && !math.IsInf(sky, 0) {
		p.subs = append(p.subs, skySub{sky: sky, score: score, plain: plain})
	}
	if !math.IsNaN(clarity) {
		p.clarity = append(p.clarity, clarity)
	}
	if !math.IsNaN(plainClarity) {
		p.plainClear = append(p.plainClear, plainClarity)
	}
}

func (p *skyPool) sort() {
	slices.SortFunc(p.subs, func(a, b skySub) int {
		switch {
		case a.sky < b.sky:
			return -1
		case a.sky > b.sky:
			return 1
		}
		return 0
	})
}

// bin is the sky bin of a sub with sky rate sky.
func (p *skyPool) bin(sky float64) SkyBin {
	all := SkyBin{Subs: p.all, Best: p.best, PlainBest: p.plainBest}
	if !(sky > 0) || math.IsInf(sky, 0) {
		return all
	}
	for ratio := SkyBinRatio; ; ratio *= SkyBinRatio {
		lo, hi := sky/ratio, sky*ratio
		i := sort.Search(len(p.subs), func(k int) bool { return p.subs[k].sky >= lo })
		j := sort.Search(len(p.subs), func(k int) bool { return p.subs[k].sky > hi })
		if i == 0 && j == len(p.subs) {
			return all
		}
		if j-i < SkyBinMinSubs {
			continue
		}
		b := SkyBin{Low: lo, High: hi, Subs: j - i}
		for _, s := range p.subs[i:j] {
			b.Best, b.PlainBest = math.Max(b.Best, s.score), math.Max(b.PlainBest, s.plain)
		}
		return b
	}
}

// BelowCut reports whether a sub falls under the stacker's cut at minScore,
// a fraction of the best: its score under minScore of the best score in its
// sky bin, or its clarity under minScore of its target's in the filter. It
// says why.
func (s SubScore) BelowCut(minScore float64) (bool, string) {
	if !(s.Score > 0) {
		return true, fmt.Sprintf("score %.2f", s.Score)
	}
	if best := s.CutBest(); s.Score < minScore*best {
		return true, fmt.Sprintf("score %.2f is under the cut %.2f: %g × the best %s score, %.2f, %s",
			s.Score, minScore*best, minScore, s.Filter, best, s.SkyBin.describe())
	}
	if s.Clarity < minScore*s.TargetClarity {
		return true, fmt.Sprintf("stars too soft or too faint, whatever the sky: clarity (transparency² / HFR⁴) %.2f of the target's typical %s sub, under %g (HFR %.2f, transparency %.2f)",
			s.Clarity/s.TargetClarity, s.Filter, minScore, s.HFR, s.Transparency)
	}
	return false, ""
}

// PlainBelowCut is BelowCut without transparency, as subs were cut before it.
func (s SubScore) PlainBelowCut(minScore float64) bool {
	best := s.SkyBin.PlainBest
	if s.SkyBin.Subs == 0 {
		best = s.PlainTargetBest
	}
	return !(s.PlainScore > 0) || s.PlainScore < minScore*best || s.PlainClarity < minScore*s.PlainTargetClarity
}

// CutBest is the best score BelowCut judges the sub's score against: its sky
// bin's, or its target's when it has no bin.
func (s SubScore) CutBest() float64 {
	if s.SkyBin.Subs == 0 {
		return s.TargetBest
	}
	return s.SkyBin.Best
}

func (b SkyBin) describe() string {
	switch {
	case b.Subs == 0:
		return "among the target's subs"
	case b.Low == 0 && b.High == 0:
		return fmt.Sprintf("among the target's %d subs", b.Subs)
	}
	return fmt.Sprintf("among the target's %d subs in a similar sky (%.3g to %.3g ADU/s)", b.Subs, b.Low, b.High)
}

// binnedSub is a scored sub and its target, for applySkyBins.
type binnedSub struct {
	target string
	s      *SubScore
}

// applySkyBins sets each sub's SkyRate, Clarity, SkyBin and TargetClarity
// from the subs of its target and filter that count for its best: those not
// rejected in Target Scheduler, or rejected by the stacker itself.
func applySkyBins(subs []binnedSub) {
	type key struct{ target, filter string }
	pools := map[key]*skyPool{}
	for _, b := range subs {
		s := b.s
		s.SkyRate = math.NaN()
		if s.Exposure > 0 {
			s.SkyRate = s.Sky / s.Exposure
		}
		s.Clarity, s.PlainClarity = Clarity(s.HFR, s.Transparency), Clarity(s.HFR, 1)
		if s.GradingStatus == GradingRejected && !s.StackerRejected {
			continue
		}
		k := key{b.target, s.Filter}
		if pools[k] == nil {
			pools[k] = &skyPool{}
		}
		pools[k].add(s.SkyRate, s.Score, s.PlainScore, s.Clarity, s.PlainClarity)
	}
	clarity := make(map[key][2]float64, len(pools))
	for k, p := range pools {
		p.sort()
		clarity[k] = [2]float64{Reference(p.clarity), Reference(p.plainClear)}
	}
	for _, b := range subs {
		s := b.s
		s.TargetClarity, s.PlainTargetClarity = math.NaN(), math.NaN()
		k := key{b.target, s.Filter}
		if p := pools[k]; p != nil {
			s.SkyBin = p.bin(s.SkyRate)
			s.TargetClarity, s.PlainTargetClarity = clarity[k][0], clarity[k][1]
		}
	}
}
