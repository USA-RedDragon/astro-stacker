package ownimage

import (
	"fmt"
	"sort"
	"strings"
)

// Mirror is a filter in which two flagged nights are each other's only
// reference: each night's change there is the other's turned around, so
// the two are one measurement. Barnard's E's 2026-10-10 and 2026-10-13
// were its only nights with Red and Green subs, so "brighter on
// 2026-10-10" and "fainter on 2026-10-13" were the same difference.
type Mirror struct {
	Nights [2]string `json:"nights"`
	Filter string    `json:"filter"`
}

// mirrorsOf finds the mirrored filters among the rebuilt references.
func mirrorsOf(subs []Sub, sets []refSet) []Mirror {
	type key struct{ night, filter string }
	only := map[key]string{}
	for _, set := range sets {
		other := ""
		for _, i := range set.refs {
			k := nightKey(subs[i].Night)
			if other != "" && k != other {
				other = ""
				break
			}
			other = k
		}
		if other != "" {
			only[key{set.night, set.filter}] = other
		}
	}
	var out []Mirror
	for k, other := range only {
		if k.night < other && only[key{other, k.filter}] == k.night {
			out = append(out, Mirror{Nights: [2]string{k.night, other}, Filter: k.filter})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Nights != out[b].Nights {
			return out[a].Nights[0] < out[b].Nights[0] || out[a].Nights[0] == out[b].Nights[0] && out[a].Nights[1] < out[b].Nights[1]
		}
		return out[a].Filter < out[b].Filter
	})
	return out
}

// mirrored says whether a night's level in a filter is half of a mirror.
func (sp *spotPlan) mirrored(night, filter string) (Mirror, bool) {
	for _, m := range sp.mirrors {
		if m.Filter == filter && (m.Nights[0] == night || m.Nights[1] == night) {
			return m, true
		}
	}
	return Mirror{}, false
}

// independentDetections is how many of the flagged nights are
// independent detections, nights joined by a mirror counting as one.
func independentDetections(sp *spotPlan) int {
	root := map[string]string{}
	for n := range sp.flagged {
		root[n] = n
	}
	var find func(string) string
	find = func(n string) string {
		if root[n] == n {
			return n
		}
		root[n] = find(root[n])
		return root[n]
	}
	for _, m := range sp.mirrors {
		a, b := find(m.Nights[0]), find(m.Nights[1])
		if a != b {
			root[a] = b
		}
	}
	k := 0
	for n := range root {
		if find(n) == n {
			k++
		}
	}
	return k
}

// mirrorNote says which flagged nights were each other's only reference
// and how many independent detections that leaves, or "".
func mirrorNote(c *Curve) string {
	if len(c.Mirrors) == 0 {
		return ""
	}
	byPair := map[[2]string][]string{}
	var pairs [][2]string
	for _, m := range c.Mirrors {
		if _, ok := byPair[m.Nights]; !ok {
			pairs = append(pairs, m.Nights)
		}
		byPair[m.Nights] = append(byPair[m.Nights], m.Filter)
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s and %s are each other's only %s reference", p[0], p[1], andList(byPair[p])))
	}
	word := "detections"
	if c.Detections == 1 {
		word = "detection"
	}
	return fmt.Sprintf(" %s, so each night's change there is the other's turned around and counts once; the %d flagged nights are %d independent %s.",
		strings.Join(parts, "; "), c.FlaggedNights, c.Detections, word)
}

func andList(v []string) string {
	switch len(v) {
	case 0:
		return ""
	case 1:
		return v[0]
	}
	return strings.Join(v[:len(v)-1], ", ") + " and " + v[len(v)-1]
}
