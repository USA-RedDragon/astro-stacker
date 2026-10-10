package match

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	raHMS    = `\d{1,2}(?:\.\d+)?\s*h\s*(?:\d{1,2}(?:\.\d+)?\s*m?\s*(?:\d{1,2}(?:\.\d+)?\s*s?)?)?|\d{1,2}:\d{1,2}(?::\d{1,2}(?:\.\d+)?)?(?:\.\d+)?|\d{1,2}\s+\d{1,2}\s+\d{1,2}(?:\.\d+)?`
	raDeg    = `\d{1,3}(?:\.\d+)?\s*°?`
	decDMS   = `[+\-−]?\s*\d{1,2}\s*[d°:]\s*\d{1,2}(?:\.\d+)?\s*['′m:]?\s*(?:\d{1,2}(?:\.\d+)?\s*(?:"|″|''|s)?)?|[+\-−]?\d{1,2}\s+\d{1,2}\s+\d{1,2}(?:\.\d+)?`
	decDeg   = `[+\-−]?\s*\d{1,2}(?:\.\d+)?\s*°?`
	raLabel  = `(?:ra\s*[:=]?\s*)?`
	decLabel = `(?:dec\s*[:=]?\s*)?`
	coordSep = `\s*[,;/]?\s*`
)

var coordPattern = regexp.MustCompile(`(?i)^\s*` + raLabel + `(` + raHMS + `|` + raDeg + `)` + coordSep + decLabel + `(` + decDMS + `|` + decDeg + `)\s*$`)

var number = regexp.MustCompile(`\d+(?:\.\d+)?`)

func sexagesimal(s string) (float64, bool) {
	parts := number.FindAllString(s, -1)
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	v := 0.0
	scale := 1.0
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		if i > 0 && f >= 60 {
			return 0, false
		}
		v += f / scale
		scale *= 60
	}
	return v, true
}

func ParseCoordinates(s string) (raDeg, decDeg float64, ok bool) {
	m := coordPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	raText, decText := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	ra, ok := sexagesimal(raText)
	if !ok {
		return 0, 0, false
	}
	lower := strings.ToLower(raText)
	if strings.ContainsAny(lower, "h:") || len(number.FindAllString(raText, -1)) > 1 {
		if ra >= 24 {
			return 0, 0, false
		}
		ra *= 15
	}
	if ra >= 360 {
		return 0, 0, false
	}
	dec, ok := sexagesimal(decText)
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
