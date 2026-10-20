package vetting

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Survey alerts on a catalogued star: ZTF sends an alert when its own
// difference image shows new light at a spot, so an alert object on the
// star, changing the same way on the nights the user's frames flagged,
// is a second instrument seeing the same change. 23991 (rho Oph, IC 4604
// Panel 6, 2025-05-20), a G 20.35 star, brightened by magnitude 17.58
// and 18.52 of added light on two nights; ZTF alert ZTF25aaqvlng, 0.2"
// from the star, measured 17.59 and 18.54 on the same nights, and the
// vet still found nothing for or against it.
const (
	// alertStarArcsec is how close an alert object must sit to the star:
	// another star's alert is another star's change.
	alertStarArcsec = 1.0
	// alertNightDays is how far an alert detection may fall from the
	// middle of a flagged night.
	alertNightDays = 2.0
	// palomarEvening is how far back from a ZTF time the evening date of
	// Palomar's night lies: its nights run from about 03:00 to 13:00 UTC
	// of the next date.
	palomarEvening = 14 * time.Hour
)

// palomarNight is the evening date of the Palomar night a ZTF time falls
// in, as the user's nights are named.
func palomarNight(mjd float64) string {
	return lc.MJDTime(mjd).Add(-palomarEvening).Format(time.DateOnly)
}

// alertHit is one alert detection that matches a flagged night.
type alertHit struct {
	oid string
	sep float64
	det ZTFDetection
}

// alertHits are the detections of the alert objects within
// alertStarArcsec of ra, dec that change the same way as one of the
// members on its night, within alertNightDays, and how many of the
// flagged nights they cover, each detection counted for the nearest.
func alertHits(objs []ZTFObject, ra, dec float64, members []app.DiscoveryCandidate) ([]alertHit, int) {
	var hits []alertHit
	nights := map[string]bool{}
	for _, o := range objs {
		sep := arcsec(ra, dec, o.RA, o.Dec)
		if sep > alertStarArcsec {
			continue
		}
		for _, d := range o.Detections {
			best, near := "", alertNightDays
			for _, m := range members {
				if m.Kind != app.DiscoveryKindChange || m.Sign == 0 || d.Positive != (m.Sign > 0) {
					continue
				}
				if off := math.Abs(d.MJD - mjdOf(m.Night.UTC().Add(nightMid))); off <= near {
					best, near = m.Night.UTC().Format(time.DateOnly), off
				}
			}
			if best != "" {
				nights[best] = true
				hits = append(hits, alertHit{oid: o.OID, sep: sep, det: d})
			}
		}
	}
	slices.SortFunc(hits, func(a, b alertHit) int {
		switch {
		case a.det.MJD < b.det.MJD:
			return -1
		case a.det.MJD > b.det.MJD:
			return 1
		}
		return 0
	})
	return hits, len(nights)
}

// alertOpen says the verdict is one survey alerts on the flagged nights
// can settle: the checks found nothing either way, or the surveys' own
// light curves did not see the change.
func alertOpen(s Suggestion) bool {
	return s.State == app.VetUnsure && (s.Say == sayNotSure || s.Say == sayNoSurveySaw)
}

// alertSupport makes an open verdict real when ZTF alert objects on the
// star changed the same way on the flagged nights, saying so.
func alertSupport(s Suggestion, objs []ZTFObject, ra, dec float64, members []app.DiscoveryCandidate, groups []Group) Suggestion {
	if !alertOpen(s) {
		return s
	}
	hits, nights := alertHits(objs, ra, dec, members)
	if len(hits) == 0 {
		return s
	}
	up := 0
	for _, h := range hits {
		if h.det.Positive {
			up++
		}
	}
	brighter := 2*up >= len(hits)
	var parts []string
	var oids []string
	for _, h := range hits {
		if h.det.Positive != brighter {
			continue
		}
		parts = append(parts, fmt.Sprintf("%.2f in %s on the night of %s", h.det.Mag, h.det.Band, palomarNight(h.det.MJD)))
		if !slices.Contains(oids, h.oid) {
			oids = append(oids, h.oid)
		}
	}
	way, say, kind := "added", "Looks like a new outburst, also seen by ZTF", KindBrightens
	if !brighter {
		way, say, kind = "lost", "Looks like a real fade, also seen by ZTF", KindFades
	}
	night, same := "night", "Two instruments saw the same change on the same night."
	if nights > 1 {
		night, same = fmt.Sprintf("%d nights", nights), "Two instruments saw the same change on the same nights."
	}
	why := fmt.Sprintf("ZTF alert %s, %.1f arcsec from the star, saw light %s there on the same %s as your frames: magnitude %s.",
		joinWords(oids), hits[0].sep, way, night, joinWords(parts))
	conf := Medium
	if nights > 1 {
		conf = High
	}
	return Suggestion{State: app.VetReal, Say: say, Confidence: conf, Kind: kind,
		Why: joinSentences(why, same, evidence(groups, check.OwnChange))}
}
