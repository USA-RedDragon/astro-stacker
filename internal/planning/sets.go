package planning

import (
	"slices"
	"strings"
)

type SetItem struct {
	Template string  `json:"template"`
	Exposure float64 `json:"exposure"`
}

type ExposureSet struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Hint  string    `json:"hint"`
	Items []SetItem `json:"items"`
}

func ExposureSets() []ExposureSet {
	it := func(t string, e float64) SetItem { return SetItem{Template: t, Exposure: e} }
	return []ExposureSet{
		{ID: "hargb", Name: "H-a with RGB stars", Hint: "Suggested for H II regions", Items: []SetItem{it("H-a", 600), it("Red", 600), it("Green", 600), it("Blue", 600)}},
		{ID: "hoo", Name: "HOO", Hint: "Your favourite for SNRs and PNe", Items: []SetItem{it("H-a", 600), it("O-III", 600)}},
		{ID: "hoorgb", Name: "HOO with RGB stars", Hint: "Remnants with star colour", Items: []SetItem{it("H-a", 600), it("O-III", 600), it("Red", 600), it("Green", 600), it("Blue", 600)}},
		{ID: "sho", Name: "SHO", Hint: "For SHO palettes only", Items: []SetItem{it("S-II", 600), it("H-a", 600), it("O-III", 600)}},
		{ID: "lrgb", Name: "LRGB", Hint: "Galaxies, clusters, dark nebulae", Items: []SetItem{it("Luminance", 300), it("Red", 300), it("Green", 300), it("Blue", 300)}},
		{ID: "lrgb600", Name: "LRGB, colour at 600 s", Hint: "Faint broadband fields", Items: []SetItem{it("Luminance", 300), it("Red", 600), it("Green", 600), it("Blue", 600)}},
		{ID: "rgbha", Name: "RGB with H-a", Hint: "Galaxies with H II knots", Items: []SetItem{it("Red", 300), it("Green", 300), it("Blue", 300), it("H-a", 600)}},
		{ID: "shorgb", Name: "SHO with LRGB", Hint: "Everything, for deep projects", Items: []SetItem{it("Luminance", 300), it("Red", 300), it("Green", 300), it("Blue", 300), it("S-II", 600), it("H-a", 600), it("O-III", 600)}},
	}
}

func SetByID(id string) (ExposureSet, bool) {
	for _, s := range ExposureSets() {
		if s.ID == id {
			return s, true
		}
	}
	return ExposureSet{}, false
}

func normTemplate(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func setKey(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = normTemplate(n)
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return strings.Join(out, "|")
}

func SetName(templates []string) string {
	if len(templates) == 0 {
		return "No plans"
	}
	k := setKey(templates)
	for _, s := range ExposureSets() {
		names := make([]string, len(s.Items))
		for i, it := range s.Items {
			names[i] = it.Template
		}
		if setKey(names) == k {
			return s.Name
		}
	}
	uniq := []string{}
	for _, t := range templates {
		if !slices.Contains(uniq, t) {
			uniq = append(uniq, t)
		}
	}
	return "Custom · " + strings.Join(uniq, ", ")
}
