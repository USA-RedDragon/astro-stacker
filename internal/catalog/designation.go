package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

type designationRule struct {
	re     *regexp.Regexp
	format func(m []string) (string, bool)
}

var spaceRun = regexp.MustCompile(`\s+`)

var greenSNR = regexp.MustCompile(`^(?:SNR\s*)?G\s*(\d{1,3}(?:\.\d+)?)\s*([+-])\s*(\d{1,2}(?:\.\d+)?)$`)

var reMessier = regexp.MustCompile(`^(?:M|MESSIER)\s*-?\s*0*(\d{1,3})$`)
var reNGC = regexp.MustCompile(`^NGC\s*-?\s*0*(\d{1,4})\s*([A-Z])?$`)
var reIC = regexp.MustCompile(`^IC\s*-?\s*0*(\d{1,4})\s*([A-Z])?$`)
var reCaldwell = regexp.MustCompile(`^(?:C|CALDWELL)\s*-?\s*0*(\d{1,3})$`)
var reSharpless = regexp.MustCompile(`^(?:SH\s*2\s*-?|SHARPLESS(?:\s*2\s*-)?)\s*0*(\d{1,3})$`)
var reLBN = regexp.MustCompile(`^LBN\s*-?\s*0*(\d{1,4})$`)
var reLDN = regexp.MustCompile(`^(?:LDN|LYNDS)\s*-?\s*0*(\d{1,4})$`)
var reBarnard = regexp.MustCompile(`^(?:B|BARNARD)\s*-?\s*0*(\d{1,3})\s*([A-Z])?$`)
var reVdB = regexp.MustCompile(`^(?:VDB|VAN\s*DEN\s*BERGH)\s*-?\s*0*(\d{1,3})$`)
var reArp = regexp.MustCompile(`^ARP\s*-?\s*0*(\d{1,3})$`)
var reHCG = regexp.MustCompile(`^(?:HCG|HICKSON)\s*-?\s*0*(\d{1,3})$`)
var reRCW = regexp.MustCompile(`^RCW\s*-?\s*0*(\d{1,3})$`)
var reCed = regexp.MustCompile(`^(?:CED|CEDERBLAD)\s*-?\s*0*(\d{1,3})\s*([A-Z])?$`)
var reGum = regexp.MustCompile(`^GUM\s*-?\s*0*(\d{1,3})$`)
var reAbell = regexp.MustCompile(`^(?:ABELL|A)\s*-?\s*0*(\d{1,4})$`)
var reCr = regexp.MustCompile(`^(?:CR|COLLINDER|CL)\s*-?\s*0*(\d{1,3})$`)
var reMel = regexp.MustCompile(`^(?:MEL|MELOTTE)\s*-?\s*0*(\d{1,3})$`)
var rePGC = regexp.MustCompile(`^PGC\s*-?\s*0*(\d{1,7})$`)
var reUGC = regexp.MustCompile(`^UGC\s*-?\s*0*(\d{1,5})$`)

func designationRules() []designationRule {
	return []designationRule{
		{reMessier, numbered("M ", 110)},
		{reNGC, suffixed("NGC ")},
		{reIC, suffixed("IC ")},
		{reCaldwell, numbered("C ", 109)},
		{reSharpless, numbered("Sh2-", 313)},
		{reLBN, numbered("LBN ", 1125)},
		{reLDN, numbered("LDN ", 1802)},
		{reBarnard, suffixedLower("B ")},
		{reVdB, numbered("vdB ", 158)},
		{reArp, numbered("Arp ", 338)},
		{reHCG, numbered("HCG ", 100)},
		{reRCW, numbered("RCW ", 182)},
		{reCed, suffixedLower("Ced ")},
		{reGum, numbered("Gum ", 85)},
		{reAbell, numbered("Abell ", 4076)},
		{reCr, numbered("Cr ", 471)},
		{reMel, numbered("Mel ", 245)},
		{rePGC, numbered("PGC ", 9999999)},
		{reUGC, numbered("UGC ", 99999)},
	}
}

func numbered(prefix string, hi int) func([]string) (string, bool) {
	return func(m []string) (string, bool) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > hi {
			return "", false
		}
		return prefix + strconv.Itoa(n), true
	}
}

func suffixed(prefix string) func([]string) (string, bool) {
	return func(m []string) (string, bool) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return "", false
		}
		return prefix + strconv.Itoa(n) + m[2], true
	}
}

func suffixedLower(prefix string) func([]string) (string, bool) {
	return func(m []string) (string, bool) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return "", false
		}
		return prefix + strconv.Itoa(n) + strings.ToLower(m[2]), true
	}
}

func cleanDesignation(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '‐', '‑', '‒', '–', '—', '−', '_':
			return '-'
		}
		return r
	}, s)
	return strings.ToUpper(strings.TrimSpace(spaceRun.ReplaceAllString(s, " ")))
}

func Canonical(s string) (string, bool) {
	c := cleanDesignation(s)
	if m := greenSNR.FindStringSubmatch(c); m != nil {
		l, err1 := strconv.ParseFloat(m[1], 64)
		b, err2 := strconv.ParseFloat(m[3], 64)
		if err1 != nil || err2 != nil || l >= 360 || b > 90 {
			return "", false
		}
		return fmt.Sprintf("G%05.1f%s%04.1f", l, m[2], b), true
	}
	for _, r := range designationRules() {
		if m := r.re.FindStringSubmatch(c); m != nil {
			return r.format(m)
		}
	}
	return "", false
}

func Key(designation string) string {
	if c, ok := Canonical(designation); ok {
		designation = c
	}
	return strings.ToUpper(strings.Join(strings.Fields(designation), ""))
}

var designationShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,9}[ -]?[+-]?\d[0-9A-Za-z.+\-*?]*$|^\d[A-Za-z]{1,2}\s?\d+(\.\d+)?$`)

func LooksLikeDesignation(s string) bool {
	if _, ok := Canonical(s); ok {
		return true
	}
	s = strings.TrimSpace(s)
	return len(s) <= 24 && designationShape.MatchString(s)
}

var panelSuffix = regexp.MustCompile(`(?i)(?:\s*[-,]?\s*\bpanel\s*\d+|\s+p\d+)\s*$`)

func StripPanel(name string) string {
	return strings.TrimSpace(panelSuffix.ReplaceAllString(name, ""))
}

func genericWord(w string) bool {
	switch w {
	case "nebula", "nebulae", "neb", "galaxy", "galaxies", "cluster", "region", "complex", "the", "sho", "hoo",
		"rgb", "lrgb", "ha", "oiii", "mosaic", "and", "of", "snr", "remnant", "supernova", "co", "s":
		return true
	}
	return false
}

func NormalizeName(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "&", " and "))
	s = strings.ReplaceAll(s, "'s", "s")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func CoreName(s string) string {
	words := strings.Fields(NormalizeName(StripPanel(s)))
	out := words[:0]
	for _, w := range words {
		if !genericWord(w) {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

func trigrams(s string) []uint64 {
	r := []rune("  " + s + " ")
	out := make([]uint64, 0, len(r))
	for i := 0; i+3 <= len(r); i++ {
		out = append(out, uint64(r[i])<<42|uint64(r[i+1])<<21|uint64(r[i+2]))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func trigramSimilarity(ta, tb []uint64) float64 {
	inter, i, j := 0, 0, 0
	for i < len(ta) && j < len(tb) {
		switch {
		case ta[i] == tb[j]:
			inter++
			i++
			j++
		case ta[i] < tb[j]:
			i++
		default:
			j++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func Similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	return trigramSimilarity(trigrams(a), trigrams(b))
}
