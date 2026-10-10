package planning

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
)

const setExamples = 2

type SetItem struct {
	Template string  `json:"template"`
	Exposure float64 `json:"exposure"`
	Desired  int     `json:"desired"`
}

type ExposureSet struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hint       string    `json:"hint"`
	Items      []SetItem `json:"items"`
	Projects   int       `json:"projects"`
	ProjectIDs []int     `json:"projectIds"`
	Examples   []string  `json:"examples"`
}

type setAcc struct {
	set     ExposureSet
	desired [][]int
	seen    map[int]bool
}

func sameItem(it SetItem, p Plan) bool {
	return normTemplate(it.Template) == normTemplate(p.Template) && it.Exposure == p.Exposure
}

func planSetKey(plans []Plan) string {
	parts := make([]string, 0, len(plans))
	for _, p := range plans {
		parts = append(parts, normTemplate(p.Template)+"@"+strconv.FormatFloat(p.Exposure, 'f', -1, 64))
	}
	slices.Sort(parts)
	return strings.Join(slices.Compact(parts), "|")
}

func setID(key string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("set-%016x", h.Sum64())
}

func enabledPlans(t Target) []Plan {
	out := make([]Plan, 0, len(t.Plans))
	for _, p := range t.Plans {
		if p.Enabled && p.Template != "" {
			out = append(out, p)
		}
	}
	return out
}

func newSetAcc(key string, plans []Plan) *setAcc {
	acc := &setAcc{seen: map[int]bool{}, set: ExposureSet{ID: setID(key), ProjectIDs: []int{}, Examples: []string{}}}
	names := []string{}
	for _, pl := range plans {
		if slices.ContainsFunc(acc.set.Items, func(it SetItem) bool { return sameItem(it, pl) }) {
			continue
		}
		acc.set.Items = append(acc.set.Items, SetItem{Template: pl.Template, Exposure: pl.Exposure})
		acc.desired = append(acc.desired, nil)
		names = append(names, pl.Template)
	}
	acc.set.Name = SetName(names)
	return acc
}

func DeriveSets(projects []Project) []ExposureSet {
	byKey := map[string]*setAcc{}
	var order []string
	for _, p := range projects {
		for _, t := range p.Targets {
			plans := enabledPlans(t)
			if len(plans) == 0 {
				continue
			}
			key := planSetKey(plans)
			acc, ok := byKey[key]
			if !ok {
				acc = newSetAcc(key, plans)
				byKey[key] = acc
				order = append(order, key)
			}
			for _, pl := range plans {
				for i, it := range acc.set.Items {
					if sameItem(it, pl) {
						acc.desired[i] = append(acc.desired[i], pl.Desired)
					}
				}
			}
			if !acc.seen[p.ID] {
				acc.seen[p.ID] = true
				acc.set.ProjectIDs = append(acc.set.ProjectIDs, p.ID)
				if len(acc.set.Examples) < setExamples {
					acc.set.Examples = append(acc.set.Examples, p.Name)
				}
			}
		}
	}
	out := make([]ExposureSet, 0, len(order))
	for _, k := range order {
		acc := byKey[k]
		for i := range acc.set.Items {
			acc.set.Items[i].Desired = medianInt(acc.desired[i])
		}
		acc.set.Projects = len(acc.set.ProjectIDs)
		acc.set.Hint = usageHint(acc.set)
		out = append(out, acc.set)
	}
	slices.SortStableFunc(out, func(a, b ExposureSet) int { return b.Projects - a.Projects })
	return out
}

func usageHint(s ExposureSet) string {
	n := "1 project"
	if s.Projects != 1 {
		n = fmt.Sprintf("%d projects", s.Projects)
	}
	if len(s.Examples) == 0 {
		return "Used on " + n
	}
	return fmt.Sprintf("Used on %s, e.g. %s", n, strings.Join(s.Examples, ", "))
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m] + 1) / 2
}

func (s *Snapshot) SetByID(id string) (ExposureSet, bool) {
	for _, set := range s.Sets {
		if set.ID == id {
			return set, true
		}
	}
	return ExposureSet{}, false
}

func normTemplate(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func SetName(templates []string) string {
	if len(templates) == 0 {
		return "No plans"
	}
	uniq := []string{}
	for _, t := range templates {
		if !slices.ContainsFunc(uniq, func(u string) bool { return normTemplate(u) == normTemplate(t) }) {
			uniq = append(uniq, t)
		}
	}
	return strings.Join(uniq, ", ")
}
