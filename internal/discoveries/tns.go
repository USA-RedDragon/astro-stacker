package discoveries

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

// EarlyDays is how long before a TNS discovery our detections and limits
// still tell when the object appeared.
const EarlyDays = 30

// EarlyBefore says whether a measurement at t is early data for an object
// TNS discovered at discovered: before it, by at most EarlyDays.
func EarlyBefore(t, discovered time.Time) bool {
	return !t.IsZero() && !discovered.IsZero() && t.Before(discovered) && discovered.Sub(t) <= EarlyDays*24*time.Hour
}

// EarlyText is what a known object with our data from before its TNS
// discovery is labelled with.
const EarlyText = "Early data: you have measurements from before its discovery; worth sending to TNS as photometry"

// TNSEntry is what the local copy of the Transient Name Server says about
// a known object, and how our data on it relate to its discovery.
type TNSEntry struct {
	Name       string    `json:"name"`
	Type       string    `json:"type,omitempty"`
	Group      string    `json:"group,omitempty"`
	Reporters  string    `json:"reporters,omitempty"`
	Discovered time.Time `json:"discovered"`
	Mag        *float64  `json:"mag,omitempty"`
	Filter     string    `json:"filter,omitempty"`
	Redshift   *float64  `json:"redshift,omitempty"`
	Internal   string    `json:"internal_names,omitempty"`
	// OurClass is the class the candidate would have without the alert
	// feeds and TNS.
	OurClass string `json:"our_class,omitempty"`
	// Seen is our earliest detection at the spot.
	Seen time.Time `json:"seen"`
	// EarlyData says we detected it in the EarlyDays before its TNS
	// discovery.
	EarlyData bool `json:"early_data,omitempty"`
}

// TNSLocal searches the local TNS copy: the objects within radius
// arcseconds, and false while the copy is not loaded.
type TNSLocal func(ra, dec, radiusArcsec float64) ([]Alert, bool)

// LocalTNS searches the mirror's index, which index returns and which is
// nil until the mirror has loaded.
func LocalTNS(index func() *tnsmirror.Index) TNSLocal {
	return func(ra, dec, radiusArcsec float64) ([]Alert, bool) {
		ix := index()
		if ix == nil {
			return nil, false
		}
		hits := ix.Near(ra, dec, radiusArcsec)
		out := make([]Alert, 0, len(hits))
		for _, h := range hits {
			out = append(out, Alert{Source: sourceTNS, ID: h.FullName(), Class: h.Type, RA: h.RA, Dec: h.Dec, SepArc: h.SepArcsec,
				TNS: &TNSEntry{Name: h.FullName(), Type: h.Type, Group: h.Group(), Reporters: h.Reporters, Discovered: h.Discovered,
					Mag: h.DiscoveryMag, Filter: h.DiscoveryBand, Redshift: h.Redshift, Internal: h.InternalNames}})
		}
		return out, true
	}
}

var errTNSNotLoaded = errors.New("tns: the local copy is not loaded")

// tnsSearch answers TNS from the local copy when it is loaded, else from
// the TNS search API when a key is set.
func (c *Catalogs) tnsSearch(ctx context.Context, ra, dec, radius float64) ([]Alert, error) {
	if c.TNSLocal != nil {
		r := radius
		if c.TNSMatchArcsec > 0 {
			r = c.TNSMatchArcsec
		}
		if as, ok := c.TNSLocal(ra, dec, r); ok {
			return as, nil
		}
		if !c.TNSEnabled() {
			return nil, errTNSNotLoaded
		}
	}
	return c.TNS(ctx, ra, dec, radius)
}

func (c *Catalogs) tnsAvailable() bool { return c.TNSLocal != nil || c.TNSEnabled() }

// KindWords names a class the way a known object's label does, such as
// "nova candidate".
func KindWords(class string) string { return strings.ReplaceAll(class, "_", " ") }

// KnownText is a TNS match's label: "already known: AT 2026acsi (nova
// candidate, XOSS, Sep 22)". The kind is TNS's type when it has one, else
// the class our checks gave.
func KnownText(t TNSEntry) string {
	var parts []string
	switch {
	case t.Type != "":
		parts = append(parts, t.Type)
	case t.OurClass != "":
		parts = append(parts, KindWords(t.OurClass))
	}
	if t.Group != "" {
		parts = append(parts, t.Group)
	}
	if !t.Discovered.IsZero() {
		layout := "Jan 2"
		if t.Seen.IsZero() || t.Seen.Year() != t.Discovered.Year() {
			layout = "Jan 2 2006"
		}
		parts = append(parts, t.Discovered.Format(layout))
	}
	s := "already known: " + t.Name
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

// TNSMatch is a candidate's TNS match from the local copy, if any.
func TNSMatch(ms []Match) (Match, bool) {
	for _, m := range ms {
		if m.Catalog == sourceTNS && m.TNS != nil {
			return m, true
		}
	}
	return Match{}, false
}

// firstSeen is the time of the first sub showing a candidate at 3 sigma,
// else the start of the night's subs.
func firstSeen(c Candidate, res *Result) time.Time {
	var first time.Time
	for _, p := range c.Light {
		if p.FluxErr > 0 && float64(c.Sign)*p.Flux >= 3*p.FluxErr && (first.IsZero() || p.Time.Before(first)) {
			first = p.Time
		}
	}
	if !first.IsZero() {
		return first.UTC()
	}
	if !res.Start.IsZero() {
		return res.Start.UTC()
	}
	return res.MidTime.UTC()
}

// annotateTNS fills in each TNS match's class, our first sighting, and
// whether that came before TNS's discovery, and labels it.
func annotateTNS(l *Labelled, res *Result) {
	for i := range l.Matches {
		m := &l.Matches[i]
		if m.TNS == nil {
			continue
		}
		t := *m.TNS
		var rest []Match
		for _, x := range l.Matches {
			if x.Catalog != sourceTNS && x.Catalog != sourceAlerce && x.Catalog != sourceFink {
				rest = append(rest, x)
			}
		}
		t.OurClass = classOf(Labelled{Candidate: l.Candidate, Matches: rest})
		t.Seen = firstSeen(l.Candidate, res)
		t.EarlyData = EarlyBefore(t.Seen, t.Discovered)
		m.TNS = &t
		m.Note = KnownText(t)
	}
}

// earlierTNS marks a TNS match as early data when an earlier night's
// comparison of the target found the same change in the EarlyDays before
// TNS's discovery.
func (r *Runner) earlierTNS(ctx context.Context, object string, ls []Labelled) {
	for i := range ls {
		for j := range ls[i].Matches {
			m := &ls[i].Matches[j]
			if m.TNS == nil || m.TNS.EarlyData || m.TNS.Discovered.IsZero() {
				continue
			}
			seen, err := r.earliestAt(ctx, object, ls[i].RA, ls[i].Dec, ls[i].Sign, m.TNS.Discovered.Add(-EarlyDays*24*time.Hour), m.TNS.Discovered)
			if err != nil {
				continue
			}
			if !seen.IsZero() {
				t := *m.TNS
				t.Seen, t.EarlyData = seen, true
				m.TNS = &t
				m.Note = KnownText(t)
			}
		}
	}
}

// earliestAt is the middle of the earliest night from since to before
// with a change of the sign within the TNS match radius, or zero.
func (r *Runner) earliestAt(ctx context.Context, object string, ra, dec float64, sign int, since, before time.Time) (time.Time, error) {
	radius := alertMatchArcsec
	if r.cat != nil && r.cat.TNSMatchArcsec > 0 {
		radius = r.cat.TNSMatchArcsec
	}
	var rows []struct {
		RA      float64
		Dec     float64
		MidTime *time.Time
	}
	band := radius / 3600
	if err := r.db.WithContext(ctx).Table("discovery_candidates c").Select("c.ra, c.dec, dr.mid_time").
		Joins("JOIN discovery_runs dr ON dr.id = c.run_id").
		Where("c.object = ? AND c.kind = ? AND c.sign = ? AND c.dec BETWEEN ? AND ? AND c.class <> ? AND c.vetting <> ? AND dr.mid_time >= ? AND dr.mid_time < ?",
			object, app.DiscoveryKindChange, sign, dec-band, dec+band, ClassLowConfidence, app.VetArtifact, since, before).
		Scan(&rows).Error; err != nil {
		return time.Time{}, fmt.Errorf("earlier sightings: %w", err)
	}
	var first time.Time
	for _, row := range rows {
		if row.MidTime == nil || Separation(ra, dec, row.RA, row.Dec)*3600 > radius {
			continue
		}
		if first.IsZero() || row.MidTime.Before(first) {
			first = row.MidTime.UTC()
		}
	}
	return first, nil
}
