// Package discoveryalert sends a Telegram alert as soon as a night
// comparison stores a candidate that passed every check and matched nothing
// known, once per source, and asks for the field to be imaged again.
package discoveryalert

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// ClassNova is the class of a new star flaring in a nearby galaxy.
const ClassNova = "nova_candidate"

// MatchArcsec is how close two candidates of a night and class must be to
// count as one source.
const MatchArcsec = 2.0

// MaxPerRun is the most candidates of one run sent one by one; a run with
// more gets one summary message, since that many at once is most often a bad
// night or a fault.
const MaxPerRun = 5

// MaxAgeNights is how many nights back a candidate's night may be and still
// alert; older nights compared again are history, not a fast path.
const MaxAgeNights = 2

// newClasses are the classes that mean something new.
func newClasses() []string {
	return []string{discoveries.ClassNewVariable, discoveries.ClassSupernova, ClassNova, discoveries.ClassUnknownMover}
}

// NewClass says whether a class means a source that matched nothing known.
func NewClass(class string) bool {
	return slices.Contains(newClasses(), class)
}

// RequiredChecks are the catalogues a run must have consulted, without
// failing, for a candidate of the kind to be trusted as new: the variable
// stars and the known asteroids. A change also needs at least one of the
// transient brokers, OptionalBrokers, to have answered.
func RequiredChecks(_ string) []string {
	return []string{"VSX", "SkyBoT"}
}

// OptionalBrokers are the alert brokers a change needs one of; TNS is
// asked only with a key and never required.
func OptionalBrokers() []string { return []string{"ALeRCE", "Fink"} }

func listOf(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ChecksPassed says whether a run's catalogue checks back a candidate of
// the kind: every required one consulted, no catalogue failed but the
// brokers and TNS, and for a change at least one broker that answered.
func ChecksPassed(run app.DiscoveryRun, kind string) bool {
	checked, failed := listOf(run.Checked), listOf(run.Failed)
	optional := append(OptionalBrokers(), "TNS")
	for _, f := range failed {
		if !slices.Contains(optional, f) {
			return false
		}
	}
	for _, c := range RequiredChecks(kind) {
		if !slices.Contains(checked, c) {
			return false
		}
	}
	if kind == app.DiscoveryKindMover {
		return true
	}
	return slices.ContainsFunc(OptionalBrokers(), func(b string) bool { return slices.Contains(checked, b) && !slices.Contains(failed, b) })
}

// EarlyKnown is a change TNS already has, from the local TNS copy, that
// we measured in the days before its discovery.
func EarlyKnown(c app.DiscoveryCandidate) (discoveries.TNSEntry, bool) {
	if c.Kind != app.DiscoveryKindChange || c.Class != discoveries.ClassKnownTransient {
		return discoveries.TNSEntry{}, false
	}
	m, ok := discoveries.TNSMatch(matches(c))
	if !ok || !m.TNS.EarlyData {
		return discoveries.TNSEntry{}, false
	}
	return *m.TNS, true
}

// Alertable says whether a stored candidate is a high-confidence new find,
// or a known object we have early data on, from a finished run whose
// catalogue checks back it. A change on a moving object's path is part of
// that mover, not a find of its own.
func Alertable(c app.DiscoveryCandidate, run app.DiscoveryRun) bool {
	_, early := EarlyKnown(c)
	if run.State != app.DiscoveryDone || !NewClass(c.Class) && !early {
		return false
	}
	if c.Vetting == app.VetArtifact || c.Vetting == app.VetKnown {
		return false
	}
	if c.Kind == app.DiscoveryKindMover {
		if c.Class != discoveries.ClassUnknownMover {
			return false
		}
	} else if c.Class == discoveries.ClassUnknownMover {
		return false
	}
	return ChecksPassed(run, c.Kind)
}

// SepArcsec is the angle between two positions in arcseconds.
func SepArcsec(ra1, dec1, ra2, dec2 float64) float64 {
	return discoveries.Separation(ra1, dec1, ra2, dec2) * 3600
}

// moverWindow is how far apart in time two movers are compared on their
// predicted paths.
const moverWindow = 7 * 24 * time.Hour

// Motion is where a moving object was at a time and how it moved.
type Motion struct {
	At         time.Time
	RA, Dec    float64
	RateArcmin float64
	PA         float64
}

// MotionOf reads a mover candidate's middle detection, rate and direction
// from its track.
func MotionOf(c app.DiscoveryCandidate) (Motion, bool) {
	var t discoveries.TrackRecord
	if c.Kind != app.DiscoveryKindMover || c.Track == "" || json.Unmarshal([]byte(c.Track), &t) != nil || len(t.Points) == 0 {
		return Motion{}, false
	}
	mid := t.Points[len(t.Points)/2]
	return Motion{At: mid.Time.UTC(), RA: mid.RA, Dec: mid.Dec, RateArcmin: t.RateArcmin, PA: t.PA}, true
}

func alertMotion(a app.DiscoveryAlert) (Motion, bool) {
	if a.At == nil || a.RateArcmin == nil || a.PA == nil {
		return Motion{}, false
	}
	return Motion{At: *a.At, RA: a.RA, Dec: a.Dec, RateArcmin: *a.RateArcmin, PA: *a.PA}, true
}

// OnPath says whether b sits where a's motion puts it at b's time: within
// 10″ plus 3% of the distance moved.
func OnPath(a, b Motion) bool {
	dt := b.At.Sub(a.At)
	if dt.Abs() > moverWindow {
		return false
	}
	ra, dec, moved := discoveryfollowup.Predict(a.RA, a.Dec, a.RateArcmin, a.PA, dt)
	return SepArcsec(ra, dec, b.RA, b.Dec) <= 10+0.03*moved
}

// Same says whether a candidate is the source an alert covered. A change
// is the same at the same place of the same target on any night, whatever
// its class, so a rerun that relabels it, a later night that sees it again
// and a source that keeps coming back alert once. A moving object is the
// same when it lies on the alerted one's path.
func Same(a app.DiscoveryAlert, c app.DiscoveryCandidate) bool {
	if a.Kind != c.Kind || a.Object != c.Object {
		return false
	}
	if c.Kind != app.DiscoveryKindMover {
		return SepArcsec(a.RA, a.Dec, c.RA, c.Dec) <= MatchArcsec
	}
	am, aok := alertMotion(a)
	cm, cok := MotionOf(c)
	if aok && cok {
		return OnPath(am, cm)
	}
	return a.Night.Equal(c.Night) && SepArcsec(a.RA, a.Dec, c.RA, c.Dec) <= MatchArcsec
}

// fresh drops the candidates an alert already covers, keeping the first
// of any two in the list that are the same source.
func fresh(cands []app.DiscoveryCandidate, have []app.DiscoveryAlert) []app.DiscoveryCandidate {
	out := make([]app.DiscoveryCandidate, 0, len(cands))
	for _, c := range cands {
		dup := slices.ContainsFunc(have, func(a app.DiscoveryAlert) bool { return Same(a, c) }) ||
			slices.ContainsFunc(out, func(o app.DiscoveryCandidate) bool { return Same(alertRow(o, false), c) })
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

func finite(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) }
