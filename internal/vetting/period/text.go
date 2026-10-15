package period

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func commas(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func fmtPeriod(p, perr float64) string {
	dec := 5
	if perr > 0 && !math.IsInf(perr, 0) {
		dec = int(-math.Floor(math.Log10(3 * perr)))
	}
	dec = min(max(dec, 2), 6)
	return strconv.FormatFloat(p, 'f', dec, 64)
}

func bjdTime(bjd float64) time.Time {
	return time.Unix(0, int64((bjd-2440587.5)*86400e9)).UTC()
}

func fmtDate(bjd float64) string { return bjdTime(bjd).Format("2006-01-02") }

func yearSpan(t0, t1 float64) string {
	y0, y1 := bjdTime(t0).Year(), bjdTime(t1).Year()
	if y0 == y1 {
		return fmt.Sprintf("in %d", y0)
	}
	return fmt.Sprintf("from %d to %d", y0, y1)
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func mag2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func bandLabel(g *group) string {
	switch g.survey {
	case lc.ZTF:
		return g.band
	case lc.TESS:
		return "TESS"
	}
	return g.survey + " " + g.band
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return commas(n) + " " + many
}

func minutes(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + " min"
}
