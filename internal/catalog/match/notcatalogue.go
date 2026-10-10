package match

import (
	"errors"
	"regexp"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

var ErrNotCatalogue = errors.New("not a catalogue object")

type NotCatalogueError struct {
	Name   string
	Reason string
}

func (e *NotCatalogueError) Error() string {
	return e.Name + " is not a catalogue object: " + e.Reason
}

func (e *NotCatalogueError) Unwrap() error {
	return ErrNotCatalogue
}

var cometPattern = regexp.MustCompile(`(?i)^(?:comet\s+)?(?:[CPDXI]/\d{4}\s+[A-Z]{1,2}\d*|\d+[PDI](?:/[\w-]+)?)\b|^comet\b`)

var starCataloguePattern = regexp.MustCompile(`(?i)^(?:HD|HIP|HR|SAO|TYC|BD|Gaia\s*DR\d)\s*[-+]?\d`)

var flamsteedPattern = regexp.MustCompile(`(?i)^\d{1,3}\s+([a-z]+)$`)

func greekLetter(w string) bool {
	switch w {
	case "alpha", "alp", "beta", "bet", "gamma", "gam", "delta", "del", "epsilon", "eps", "zeta", "zet", "eta",
		"theta", "the", "iota", "iot", "kappa", "kap", "lambda", "lam", "mu", "nu", "xi", "omicron", "omi",
		"pi", "rho", "sigma", "sig", "tau", "upsilon", "ups", "phi", "chi", "psi", "omega", "ome":
		return true
	}
	return false
}

func constellationGenitive(w string) bool {
	if strings.TrimSuffix(w, "is") == "virgin" {
		return true
	}
	switch w {
	case "andromedae", "antliae", "apodis", "aquarii", "aquilae", "arae", "arietis", "aurigae", "bootis",
		"caeli", "camelopardalis", "cancri", "canum", "venaticorum", "canis", "majoris", "minoris", "capricorni",
		"carinae", "cassiopeiae", "centauri", "cephei", "ceti", "chamaeleontis", "circini", "columbae", "comae",
		"berenices", "coronae", "australis", "borealis", "corvi", "crateris", "crucis", "cygni", "delphini",
		"doradus", "draconis", "equulei", "eridani", "fornacis", "geminorum", "gruis", "herculis", "horologii",
		"hydrae", "hydri", "indi", "lacertae", "leonis", "leporis", "librae", "lupi", "lyncis", "lyrae",
		"mensae", "microscopii", "monocerotis", "muscae", "normae", "octantis", "ophiuchi", "orionis", "pavonis",
		"pegasi", "persei", "phoenicis", "pictoris", "piscis", "piscium", "austrini", "puppis", "pyxidis",
		"reticuli", "sagittae", "sagittarii", "scorpii", "sculptoris", "scuti", "serpentis", "sextantis",
		"tauri", "telescopii", "trianguli", "australe", "tucanae", "ursae", "velorum", "volantis",
		"vulpeculae":
		return true
	}
	return false
}

func brightStar(name string) bool {
	switch name {
	case "sirius", "canopus", "arcturus", "vega", "capella", "rigel", "procyon", "achernar", "betelgeuse",
		"hadar", "altair", "acrux", "aldebaran", "antares", "spica", "pollux", "fomalhaut", "deneb", "mimosa",
		"regulus", "adhara", "castor", "gacrux", "shaula", "bellatrix", "elnath", "miaplacidus", "alnilam",
		"alnair", "alnitak", "alioth", "dubhe", "mirfak", "wezen", "sargas", "kaus australis", "avior",
		"alkaid", "menkalinan", "atria", "alhena", "peacock", "alsephina", "mirzam", "alphard", "polaris",
		"hamal", "algieba", "diphda", "nunki", "menkent", "mirach", "alpheratz", "rasalhague", "kochab",
		"saiph", "denebola", "algol", "albireo", "mizar", "rigil kentaurus", "toliman", "proxima centauri",
		"barnards star", "eltanin", "schedar", "caph", "enif", "markab", "scheat", "alcyone", "maia",
		"electra", "merope", "taygeta", "pleione", "atlas", "sadr", "gienah", "mintaka", "meissa":
		return true
	}
	return false
}

func NotCatalogue(name string) (string, bool) {
	reason, _, ok := classify(name)
	return reason, ok
}

func classify(name string) (reason string, certain bool, ok bool) {
	s := strings.TrimSpace(name)
	if cometPattern.MatchString(s) {
		return "it is named like a comet, which moves against the stars", true, true
	}
	if starCataloguePattern.MatchString(s) {
		return "it is a star catalogue designation", true, true
	}
	n := catalog.NormalizeName(s)
	words := strings.Fields(n)
	if brightStar(n) {
		return "it is the name of a star", false, true
	}
	if len(words) >= 2 && len(words) <= 3 && greekLetter(words[0]) {
		all := true
		for _, w := range words[1:] {
			if !constellationGenitive(w) && (len(w) != 1 || w < "1" || w > "9") {
				all = false
			}
		}
		if all {
			return "it is a Bayer star designation", false, true
		}
	}
	if m := flamsteedPattern.FindStringSubmatch(n); m != nil && constellationGenitive(m[1]) {
		return "it is a Flamsteed star designation", false, true
	}
	return "", false, false
}
