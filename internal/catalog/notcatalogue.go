package catalog

import (
	"regexp"
	"strings"
)

const (
	ReasonComet     = "Named like a comet, which moves against the stars."
	ReasonStarID    = "A star catalogue number (HD, HIP, HR, SAO or TYC)."
	ReasonStar      = "The name of a star."
	ReasonBayer     = "A Bayer star designation."
	ReasonFlamsteed = "A Flamsteed star designation."
)

var cometPattern = regexp.MustCompile(`(?i)^(?:comet\s+)?(?:[CPDXI]/\d{4}\s+[A-Z]{1,2}\d*|\d+[PDI](?:/[\w-]+)?)\b|^comet\b`)

var starNumberPattern = regexp.MustCompile(`(?i)^(?:HD|HIP|HR|SAO|TYC)\s*[-+]?\d`)

var flamsteedPattern = regexp.MustCompile(`^\d{1,3}\s+([a-z]+)$`)

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

func NotCatalogue(name string) (reason string, certain bool) {
	s := strings.TrimSpace(name)
	switch {
	case cometPattern.MatchString(s):
		return ReasonComet, true
	case starNumberPattern.MatchString(s):
		return ReasonStarID, true
	}
	n := NormalizeName(s)
	if brightStar(n) {
		return ReasonStar, false
	}
	words := strings.Fields(n)
	if len(words) >= 2 && len(words) <= 3 && greekLetter(words[0]) {
		bayer := true
		for _, w := range words[1:] {
			if !constellationGenitive(w) && (len(w) != 1 || w < "1" || w > "9") {
				bayer = false
			}
		}
		if bayer {
			return ReasonBayer, false
		}
	}
	if m := flamsteedPattern.FindStringSubmatch(n); m != nil && constellationGenitive(m[1]) {
		return ReasonFlamsteed, false
	}
	return "", false
}
