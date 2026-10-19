package vetting

import (
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// Blends: a Gaia neighbour within starArcsec of the star, the radius a
// spot is matched to its star in, and under blendMag fainter than it can
// own the change as well as the star can. 299 (Barnard's E, 2026-10-10)
// sits 1.6" from a G 16.35 star, 0.85 fainter than its own G 15.50, and
// ZTF r at 15.29 measures the two together. The confirmed eclipsing
// binaries 1592 (2.3", 0.84 fainter), 16040 (3.0", 0.10 brighter) and
// 3754 (3.1", 0.35 brighter) have such neighbours too and are settled by
// their survey periods before any blend is weighed.
const (
	blendMag = 1.0
	// crowdBlends is how many blending neighbours make a field too
	// crowded to tell which star changed: with two or more, the star the
	// spot was matched to is one guess among several.
	crowdBlends = 2
	// overLight is how many times the star's own G light a fade must take
	// away for the change to be more than the star has. A star's light in
	// one filter stands up to half a magnitude off its G on the comparison
	// stars' scale, as a magnitude of BP-RP moves a blue filter about 1.1
	// against a red one and G lies between, so the fade must exceed the
	// star's light by that factor. 45467 (M 22, 2021-06-29) faded 0.97 mag
	// from 17.21 at the spot, 2.0 times the light of the G 18.52 star it was
	// matched to, in the cluster's core.
	overLight = 1.585
)

// blend is what the star's Gaia neighbours say of who owns the change.
type blend struct {
	near []surveys.Neighbour
	// could are those of near that alone could give the fade.
	could []surveys.Neighbour
	// over is how many times the star's own light the fade took away,
	// 0 for a brightening or an unknown spot light.
	over    float64
	spotMag float64
}

// blendOf weighs the star's neighbours against the change: those within
// starArcsec and under blendMag fainter than the star, which of them alone
// could give a fade of the reported size, and the share of the star's light a
// fade from the spot's reference light, refMag or else the Gaia light
// within starArcsec, took away.
func blendOf(st star, c change, refMag float64) blend {
	var b blend
	if !st.found || !(st.target.G > 0) {
		return b
	}
	g := st.target.G
	light := func(m float64) float64 { return math.Pow(10, -0.4*(m-g)) }
	total := 1.0
	lost := 0.0
	if c.Fainter && c.Mag > 0 {
		lost = 1 - math.Pow(10, -0.4*c.Mag)
	}
	for _, n := range st.neighbours {
		if n.Sep > starArcsec || !(n.G > 0) {
			continue
		}
		total += light(n.G)
	}
	for _, n := range st.neighbours {
		if n.Sep > starArcsec || !(n.G > 0) || n.G > g+blendMag {
			continue
		}
		b.near = append(b.near, n)
		if lost == 0 || light(n.G)/total >= lost {
			b.could = append(b.could, n)
		}
	}
	spot := total
	b.spotMag = g - 2.5*math.Log10(total)
	if !math.IsNaN(refMag) && refMag > 0 {
		spot, b.spotMag = light(refMag), refMag
	}
	if lost > 0 {
		b.over = spot * lost
	}
	return b
}

// notSure turns a plain Not sure into one that names a blend: a fade
// more than the star's own light, a field too crowded to tell, or a
// neighbour that alone could own the change.
func (b blend) notSure(s Suggestion) Suggestion {
	if s.State != app.VetUnsure || s.Say != sayNotSure {
		return s
	}
	var say, why string
	switch {
	case b.over >= overLight:
		say = "Not sure: the change is more light than the star has"
		why = fmt.Sprintf("The spot faded from magnitude %.1f by taking away %.1f times the light of the star there, more than it has: the change is in the light of stars around it.", b.spotMag, b.over)
		if len(b.near) > 0 {
			why += " " + words(b.near)
		}
	case len(b.near) >= crowdBlends:
		say = "Not sure: too crowded to tell which star changed"
		why = words(b.near)
	case len(b.could) > 0:
		say = "Not sure: a nearby star blends with it"
		why = words(b.could) + " It is bright enough to give the change alone, and your frames and the surveys measure the two together, so either could have changed."
	default:
		return s
	}
	s.Say, s.Why = say, joinSentences(why, s.Why)
	return s
}

func words(near []surveys.Neighbour) string {
	parts := make([]string, len(near))
	for i, n := range near {
		parts[i] = fmt.Sprintf("G %.2f %.1f\" away", n.G, n.Sep)
	}
	if len(parts) == 1 {
		return fmt.Sprintf("A Gaia star of %s blends with it.", parts[0])
	}
	return fmt.Sprintf("%d Gaia stars as bright or nearly as bright sit within %.0f\" (%s), and your frames measure them together with it.", len(parts), starArcsec, strings.Join(parts, ", "))
}
