package vetting

import (
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// sayIrregular is the verdict on a new variable star that changes with
// no period.
const sayIrregular = "Looks like a new variable star, changes irregularly"

// irregularSuggestion is the verdict on a star no period accounts for
// that scatters in ZTF beyond nearby stars of the same brightness, when
// ZTF has it changed around the user's flagged nights the way the user's
// frames do: a new irregular variable, or a known kind of star that
// changes without a period. ok is false when the test does not hold, the
// user's frames show no change, or the ZTF row puts the change on
// another star.
func irregularSuggestion(pr *period.Result, known Known, groups []Group) (Suggestion, bool) {
	if pr == nil || !pr.Excess.Holds() || status(groups, check.OwnChange) == check.Fail || status(groups, check.ZTF) == check.Fail {
		return Suggestion{}, false
	}
	why := irregularWords(pr.Excess)
	if known.Expected != "" {
		return variesSuggestion(why, known, groups), true
	}
	conf := Medium
	if status(groups, check.OwnChange) != check.Pass {
		conf = Low
	}
	return Suggestion{State: app.VetReal, Say: sayIrregular, Confidence: conf, Kind: KindIrregular,
		Why: joinSentences(why, evidence(groups, check.OwnChange), "No catalog lists the star as variable, and no steady period stands out.")}, true
}

func irregularWords(x *period.Excess) string {
	short := func(b string) string { return strings.TrimSpace(strings.TrimPrefix(b, lc.ZTF)) }
	bands := make([]string, 0, len(x.Bands))
	sizes := make([]string, 0, len(x.Bands))
	most := x.Bands[0]
	for _, b := range x.Bands {
		bands = append(bands, short(b.Band))
		sizes = append(sizes, fmt.Sprintf("%.3f", b.Excess))
		if b.Excess > most.Excess {
			most = b
		}
	}
	out := fmt.Sprintf("In ZTF %s the star scatters %s mag beyond its errors, while %d of %d nearby stars of the same brightness scatter less than half as much in %s.",
		joinWords(bands), joinWords(sizes), most.Steady, most.Comparisons, short(most.Band))
	f := x.Largest()
	way := "brighter"
	if f.Flagged > 0 {
		way = "fainter"
	}
	return out + fmt.Sprintf(" Within %.0f days of your flagged nights ZTF %s has it %.2f mag %s than usual against those stars, as your frames do.",
		period.FlagWindow, short(f.Band), math.Abs(f.Flagged), way)
}
