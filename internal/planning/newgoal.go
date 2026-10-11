package planning

import (
	"fmt"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
)

type NewGoal struct {
	Setting schedcmd.GoalSetting
	Basis   string
}

func familyWord(filter string) string {
	if skybright.Broadband(filter) {
		return "broadband"
	}
	return "narrowband"
}

func (s *Snapshot) GoalsSetUp() bool { return s.goalSettings != nil }

func (s *Snapshot) HasGoal(guid, filter string) bool {
	_, ok := s.goalSettings.For(guid, filter)
	return ok
}

func (s *Snapshot) GoalForNewFilter(guid, filter string) NewGoal {
	fam := familyWord(filter)
	all := make([]goals.Setting, 0, len(s.goalSettings[guid]))
	var same []goals.Setting
	for _, g := range s.goalSettings[guid] {
		all = append(all, g)
		if familyWord(g.Filter) == fam {
			same = append(same, g)
		}
	}
	refs, basis := same, fmt.Sprintf("like the target's other %s goals", fam)
	if len(refs) == 0 {
		refs, basis = all, "like the target's other goals"
	}
	def := s.Defaults.Goal
	if def.SNR <= 0 {
		dg := goals.DefaultGoal("")
		def.Kind, def.SNR, def.PlateauStop = dg.Kind, dg.SNR, dg.PlateauStop
	}
	if len(refs) == 0 {
		return NewGoal{Setting: settingFor(def.Kind, def.SNR, def.PlateauStop, filter), Basis: "the default goal, as the target has no other goals"}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Filter < refs[j].Filter })
	depth, stops := 0, 0
	snrVotes := map[float64]int{}
	for _, g := range refs {
		if g.IsDepth() {
			depth++
		} else if g.SNRGoal != nil && *g.SNRGoal > 0 {
			snrVotes[*g.SNRGoal]++
		}
		if g.PlateauStop == nil || *g.PlateauStop != 0 {
			stops++
		}
	}
	kind := goals.KindSNR
	if 2*depth > len(refs) {
		kind = goals.KindDepth
	}
	snr, best := def.SNR, 0
	for v, n := range snrVotes {
		if n > best || (n == best && v > snr) {
			snr, best = v, n
		}
	}
	plateau := def.PlateauStop
	if 2*stops != len(refs) {
		plateau = 2*stops > len(refs)
	}
	return NewGoal{Setting: settingFor(kind, snr, plateau, filter), Basis: basis}
}

func settingFor(kind goals.Kind, snr float64, plateau bool, filter string) schedcmd.GoalSetting {
	g := schedcmd.GoalSetting{Kind: schedcmd.GoalKindSNR, SNRGoal: snr, PlateauStop: plateau}
	if kind == goals.KindDepth {
		d := goals.DefaultDepth(frameheader.NormalizeFilter(filter))
		g.Kind, g.DepthGoal = schedcmd.GoalKindDepth, &d
	}
	return g
}

func GoalText(g schedcmd.GoalSetting) string {
	s := fmt.Sprintf("SNR %g", g.SNRGoal)
	if g.Kind == schedcmd.GoalKindDepth && g.DepthGoal != nil {
		s = fmt.Sprintf("%g mag/arcsec² at SNR %g", *g.DepthGoal, goals.DepthSNR)
	}
	if !g.PlateauStop {
		s += ", no plateau stop"
	}
	return s
}
