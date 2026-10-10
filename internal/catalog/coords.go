package catalog

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	coordRAHours  = `\d{1,2}(?:\.\d+)?\s*h\s*(?:\d{1,2}(?:\.\d+)?\s*m?\s*(?:\d{1,2}(?:\.\d+)?\s*s?)??)??|\d{1,2}:\d{1,2}(?::\d{1,2}(?:\.\d+)?)?(?:\.\d+)?|\d{1,2}\s+\d{1,2}\s+\d{1,2}(?:\.\d+)?`
	coordRADeg    = `\d{1,3}(?:\.\d+)?\s*°?`
	coordDecDMS   = `[+\-−]?\s*\d{1,2}\s*[d°:]\s*\d{1,2}(?:\.\d+)?\s*['′m:]?\s*(?:\d{1,2}(?:\.\d+)?\s*(?:"|″|''|s)?)?|[+\-−]?\d{1,2}\s+\d{1,2}\s+\d{1,2}(?:\.\d+)?`
	coordDecDeg   = `[+\-−]?\s*\d{1,2}(?:\.\d+)?\s*°?`
	coordRALabel  = `(?:ra\s*[:=]?\s*)?`
	coordDecLabel = `(?:dec\s*[:=]?\s*)?`
	coordSep      = `\s*[,;/]?\s*`
)

var coordPattern = regexp.MustCompile(`(?i)^\s*` + coordRALabel + `(` + coordRAHours + `|` + coordRADeg + `)` + coordSep +
	coordDecLabel + `(` + coordDecDMS + `|` + coordDecDeg + `)\s*$`)

var coordNumber = regexp.MustCompile(`\d+(?:\.\d+)?`)

func sexagesimal(s string) (float64, bool) {
	parts := coordNumber.FindAllString(s, -1)
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	v, scale := 0.0, 1.0
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || i > 0 && f >= 60 {
			return 0, false
		}
		v += f / scale
		scale *= 60
	}
	return v, true
}

func ParseCoordinates(s string) (ra, dec float64, ok bool) {
	m := coordPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	raText, decText := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	ra, ok = sexagesimal(raText)
	if !ok {
		return 0, 0, false
	}
	if strings.ContainsAny(strings.ToLower(raText), "h:") || len(coordNumber.FindAllString(raText, -1)) > 1 {
		if ra >= 24 {
			return 0, 0, false
		}
		ra *= 15
	}
	if ra >= 360 {
		return 0, 0, false
	}
	dec, ok = sexagesimal(decText)
	if !ok {
		return 0, 0, false
	}
	if strings.HasPrefix(decText, "-") || strings.HasPrefix(decText, "−") {
		dec = -dec
	}
	if dec > 90 || dec < -90 {
		return 0, 0, false
	}
	return ra, dec, true
}
