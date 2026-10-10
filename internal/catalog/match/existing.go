package match

import (
	"fmt"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const (
	existingMinRadiusDeg = 0.5
	existingCoordsBase   = 0.45
	existingCoordsScale  = 0.25
)

type Existing struct {
	Subject   string  `json:"subject"`
	Kind      string  `json:"kind"`
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	RA        float64 `json:"ra"`
	Dec       float64 `json:"dec"`
	HasCoords bool    `json:"hasCoords"`
}

type ExistingMatch struct {
	Subject    string   `json:"subject"`
	Existing   Existing `json:"existing"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Decision   string   `json:"decision,omitempty"`
}

func (e Existing) SubjectKey() string {
	if e.Subject != "" {
		return e.Subject
	}
	return Subject(e.Kind, e.Name)
}

func ExistingMatches(pick catalog.Object, existing []Existing) []ExistingMatch {
	limit := math.Max(existingMinRadiusDeg, pick.MajorArcmin/2/60)
	out := make([]ExistingMatch, 0, len(existing))
	for _, e := range existing {
		if m, ok := matchOne(pick, e, limit); ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Confidence > out[j].Confidence
	})
	return out
}

func matchOne(pick catalog.Object, e Existing, limit float64) (ExistingMatch, bool) {
	var conf, coordsConf float64
	var evidence []string
	if e.HasCoords {
		if d := catalog.Separation(e.RA, e.Dec, pick.RA, pick.Dec); d <= limit {
			coordsConf = existingCoordsBase + existingCoordsScale*(1-d/limit)
			evidence = append(evidence, fmt.Sprintf("%s from the catalogue centre, within %s", formatDistance(d), formatDistance(limit)))
		}
	}
	base, panel := StripPanel(e.Name)
	if h, ok := nameEvidence(base, pick, true); ok {
		conf = h.conf
		if h.method == MethodFuzzyCoords && coordsConf == 0 {
			conf *= farPenalty
			h.evidence += ", but the coordinates do not confirm it"
		}
		named := []string{h.evidence}
		if panel > 0 {
			named = append(named, fmt.Sprintf("it is panel %d of “%s”", panel, base))
		}
		evidence = append(named, evidence...)
	}
	conf = orCombine(conf, coordsConf)
	if conf == 0 {
		return ExistingMatch{}, false
	}
	return ExistingMatch{Subject: e.SubjectKey(), Existing: e, Confidence: conf, Evidence: evidence}, true
}
