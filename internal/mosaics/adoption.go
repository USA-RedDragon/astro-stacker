package mosaics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type TSTarget struct {
	GUID     string  `json:"guid"`
	Name     string  `json:"name"`
	RA       float64 `json:"ra"`
	Dec      float64 `json:"dec"`
	Rotation float64 `json:"rotation"`
	Active   bool    `json:"active"`
}

type TSProject struct {
	GUID     string     `json:"guid"`
	Name     string     `json:"name"`
	IsMosaic bool       `json:"isMosaic"`
	Targets  []TSTarget `json:"targets"`
}

type AdoptedPanel struct {
	TargetGUID  string    `json:"targetGuid"`
	Target      string    `json:"target"`
	Panel       int       `json:"panel"`
	Row         int       `json:"row"`
	Col         int       `json:"col"`
	Centre      Point     `json:"centre"`
	RotationDeg float64   `json:"rotationDeg"`
	Footprint   Footprint `json:"footprint"`
	Neighbours  []string  `json:"neighbours"`
}

type Proposal struct {
	ProjectGUID string         `json:"projectGuid"`
	Project     string         `json:"project"`
	Kind        string         `json:"kind"`
	Confidence  string         `json:"confidence"`
	Issue       string         `json:"issue"`
	Suggestion  string         `json:"suggestion"`
	Fingerprint string         `json:"fingerprint"`
	Auto        bool           `json:"auto"`
	Panels      []AdoptedPanel `json:"panels"`
}

const (
	KindMosaic    = "mosaic"
	KindNotMosaic = "not_mosaic"

	ConfidenceHigh   = "High"
	ConfidenceMedium = "Medium"
	ConfidenceLow    = "Low"

	neighbourOverlap = 0.03
	sameField        = 0.1
)

var PanelName = regexp.MustCompile(`(?i)\bpanel\s*(\d+)\s*$`)

func Propose(projects []TSProject, rig Rig) []Proposal {
	var out []Proposal
	for _, p := range projects {
		if len(p.Targets) < 2 {
			continue
		}
		if prop, ok := propose(p, rig); ok {
			prop.ProjectGUID, prop.Project, prop.Fingerprint = p.GUID, p.Name, Fingerprint(p)
			out = append(out, prop)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Project), strings.ToLower(out[j].Project)
		if a != b {
			return a < b
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].ProjectGUID < out[j].ProjectGUID
	})
	return out
}

func propose(p TSProject, rig Rig) (Proposal, bool) {
	if named, ok := panelTargets(p.Targets); ok {
		return namedMosaic(p, named, rig), true
	}
	if names := colocated(p); len(names) > 0 {
		return Proposal{
			Kind: KindNotMosaic, Confidence: ConfidenceMedium,
			Issue:      fmt.Sprintf("%s on the same field: %s.", fieldCount(len(names), len(p.Targets)), joinNames(names)),
			Suggestion: fmt.Sprintf("Don't adopt it as a mosaic. Treat it as one target with %d exposure sets.", len(names)),
		}, true
	}
	if p.IsMosaic {
		return positionalMosaic(p, rig), true
	}
	return Proposal{}, false
}

type namedTarget struct {
	TSTarget
	number int
	prefix string
}

func panelTargets(targets []TSTarget) ([]namedTarget, bool) {
	out := make([]namedTarget, 0, len(targets))
	seen := map[int]bool{}
	for _, t := range targets {
		m := PanelName.FindStringSubmatchIndex(t.Name)
		if m == nil {
			return nil, false
		}
		n, err := strconv.Atoi(t.Name[m[2]:m[3]])
		if err != nil || seen[n] {
			return nil, false
		}
		seen[n] = true
		out = append(out, namedTarget{TSTarget: t, number: n, prefix: strings.TrimRight(strings.TrimSpace(t.Name[:m[0]]), " -_:,")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].number < out[j].number })
	return out, true
}

func namedMosaic(p TSProject, named []namedTarget, rig Rig) Proposal {
	targets := make([]TSTarget, len(named))
	numbers := make([]int, len(named))
	for i, t := range named {
		targets[i], numbers[i] = t.TSTarget, t.number
	}
	prop := Proposal{Kind: KindMosaic, Confidence: ConfidenceHigh, Panels: adopt(targets, numbers, rig)}
	var issues, suggestions []string
	medium := false
	for _, t := range named {
		if !strings.EqualFold(t.prefix, strings.TrimSpace(p.Name)) {
			issues = append(issues, fmt.Sprintf("Its panels are named %q, not after the project %q.", t.prefix+" Panel N", p.Name))
			suggestions = append(suggestions, fmt.Sprintf("Adopt as %s and keep the panel names, so the stacker still finds its frames.", panelCount(len(named))))
			break
		}
	}
	if !sequential(numbers) {
		medium = true
		issues = append(issues, fmt.Sprintf("Its panels are numbered %s rather than 1 to %d.", joinInts(numbers), len(numbers)))
		suggestions = append(suggestions, "Keep the numbers as they are and place panels by their coordinates, not their numbers.")
	}
	if !p.IsMosaic {
		medium = true
		issues = append(issues, "TS doesn't flag it as a mosaic.")
		suggestions = append(suggestions, "Adopt it as a mosaic and leave the TS project as it is.")
	}
	if lonely := isolated(prop.Panels); len(lonely) > 0 {
		medium = true
		issues = append(issues, fmt.Sprintf("%s %s no other panel by 3%% or more.", panelList(lonely), verb(len(lonely), "overlaps", "overlap")))
		suggestions = append(suggestions, fmt.Sprintf("Check the coordinates of %s in TS before adopting.", panelList(lonely)))
	}
	if medium {
		prop.Confidence = ConfidenceMedium
	}
	if len(issues) == 0 {
		prop.Auto = true
		prop.Suggestion = fmt.Sprintf("Adopt as %s.", panelCount(len(named)))
		return prop
	}
	prop.Issue, prop.Suggestion = strings.Join(issues, " "), strings.Join(suggestions, " ")
	return prop
}

func positionalMosaic(p TSProject, rig Rig) Proposal {
	targets := append([]TSTarget(nil), p.Targets...)
	centres := make([]Point, len(targets))
	for i, t := range targets {
		centres[i] = Point{RA: t.RA, Dec: t.Dec}
	}
	rows, cols := GridCells(centres, targets[0].Rotation, rig)
	order := make([]int, len(targets))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if rows[i] != rows[j] {
			return rows[i] < rows[j]
		}
		return cols[i] < cols[j]
	})
	sorted := make([]TSTarget, len(targets))
	numbers := make([]int, len(targets))
	for k, i := range order {
		sorted[k], numbers[k] = targets[i], k+1
	}
	return Proposal{
		Kind: KindMosaic, Confidence: ConfidenceLow, Panels: adopt(sorted, numbers, rig),
		Issue:      "Its targets aren't named \"<name> Panel N\", so their panel numbers come from where they sit in the grid.",
		Suggestion: "Check the panel order on the preview, then adopt it as a mosaic and keep the target names.",
	}
}

func adopt(targets []TSTarget, numbers []int, rig Rig) []AdoptedPanel {
	centres := make([]Point, len(targets))
	fps := make([]Footprint, len(targets))
	for i, t := range targets {
		centres[i] = Point{RA: wrap360(t.RA), Dec: t.Dec}
		fps[i] = PanelFootprint(centres[i], t.Rotation, rig)
	}
	rows, cols := GridCells(centres, targets[0].Rotation, rig)
	nb := Neighbours(fps, neighbourOverlap)
	out := make([]AdoptedPanel, len(targets))
	for i, t := range targets {
		guids := make([]string, len(nb[i]))
		for k, j := range nb[i] {
			guids[k] = targets[j].GUID
		}
		out[i] = AdoptedPanel{
			TargetGUID: t.GUID, Target: t.Name, Panel: numbers[i], Row: rows[i], Col: cols[i],
			Centre: centres[i], RotationDeg: t.Rotation, Footprint: fps[i], Neighbours: guids,
		}
	}
	return out
}

func sequential(numbers []int) bool {
	for i, n := range numbers {
		if n != i+1 {
			return false
		}
	}
	return true
}

func isolated(panels []AdoptedPanel) []int {
	var out []int
	for _, p := range panels {
		if len(p.Neighbours) == 0 {
			out = append(out, p.Panel)
		}
	}
	return out
}

func colocated(p TSProject) []string {
	in := make([]bool, len(p.Targets))
	for i, a := range p.Targets {
		for j := i + 1; j < len(p.Targets); j++ {
			b := p.Targets[j]
			if separation(Point{RA: a.RA, Dec: a.Dec}, Point{RA: b.RA, Dec: b.Dec}) > sameField {
				continue
			}
			if p.IsMosaic || sharePrefix(a.Name, b.Name) {
				in[i], in[j] = true, true
			}
		}
	}
	var names []string
	for i, t := range p.Targets {
		if in[i] {
			names = append(names, t.Name)
		}
	}
	return names
}

func sharePrefix(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	fa, fb := strings.Fields(a), strings.Fields(b)
	return fa[0] == fb[0]
}

func fieldCount(n, total int) string {
	if n == total {
		return fmt.Sprintf("Its %d targets are", n)
	}
	return fmt.Sprintf("%d of its %d targets are", n, total)
}

func panelCount(n int) string {
	s := strconv.Itoa(n)
	article := "a"
	if n == 11 || n == 18 || strings.HasPrefix(s, "8") {
		article = "an"
	}
	return fmt.Sprintf("%s %d-panel mosaic", article, n)
}

func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func panelList(numbers []int) string {
	if len(numbers) == 1 {
		return fmt.Sprintf("Panel %d", numbers[0])
	}
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = strconv.Itoa(n)
	}
	return "Panels " + joinAnd(parts)
}

func joinInts(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func joinNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return joinAnd(quoted)
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func Fingerprint(p TSProject) string {
	targets := append([]TSTarget(nil), p.Targets...)
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].GUID != targets[j].GUID {
			return targets[i].GUID < targets[j].GUID
		}
		return targets[i].Name < targets[j].Name
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%t", p.Name, p.IsMosaic)
	for _, t := range targets {
		fmt.Fprintf(&b, "\x00%s\x00%s\x00%s\x00%s\x00%s", t.GUID, t.Name, round4(t.RA), round4(t.Dec), round4(t.Rotation))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

func round4(v float64) string {
	r := math.Round(v*1e4) / 1e4
	if r == 0 {
		r = 0
	}
	return strconv.FormatFloat(r, 'f', 4, 64)
}
