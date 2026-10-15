package discoveryreport

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	// KnownArcsec is how close a TNS object must be to be the find.
	KnownArcsec   = 3.0
	tnsTimeLayout = "2006-01-02 15:04:05"
	stampLayout   = "Jan 2 15:04"
	atPNV         = "PNV"
	atPSN         = "PSN"
)

// LocalSearch is a search of the local TNS copy.
type LocalSearch struct {
	AsOf time.Time
	Hits []discoveries.Alert
}

// Known is what the page says about a find TNS already has.
type Known struct {
	Name       string    `json:"name"`
	Link       string    `json:"link,omitempty"`
	Text       string    `json:"text"`
	Discovered time.Time `json:"discovered"`
	Early      bool      `json:"early"`
	EarlyText  string    `json:"early_text,omitempty"`
	// EarlyBasis are the measurements from before the discovery.
	EarlyBasis []string `json:"early_basis,omitempty"`
}

// FollowupPoint is one entry of a follow-up photometry report.
type FollowupPoint struct {
	Time     time.Time `json:"time"`
	Night    string    `json:"night"`
	Filter   string    `json:"filter"`
	Mag      *float64  `json:"mag,omitempty"`
	MagErr   *float64  `json:"mag_err,omitempty"`
	Limit    *float64  `json:"limit,omitempty"`
	Exposure float64   `json:"exposure"`
	Early    bool      `json:"early,omitempty"`
	Note     string    `json:"note"`
}

// FollowupDraft is the TNS photometry report of a find TNS already has: our
// detections and the limits from before its discovery.
type FollowupDraft struct {
	Applies      bool            `json:"applies"`
	Name         string          `json:"name,omitempty"`
	Points       []FollowupPoint `json:"points"`
	NonDetection string          `json:"non_detection,omitempty"`
	CanSend      bool            `json:"can_send"`
	SendReason   string          `json:"send_reason,omitempty"`
	SentAt       *time.Time      `json:"sent_at,omitempty"`
	SentReply    string          `json:"sent_reply,omitempty"`
}

// knownEntry is the TNS object the find is: the one the comparison
// matched, else the nearest the local copy has within KnownArcsec now.
func knownEntry(in Input) (discoveries.TNSEntry, bool) {
	c := in.Cand
	if c.Kind != app.DiscoveryKindChange {
		return discoveries.TNSEntry{}, false
	}
	if m, ok := discoveries.TNSMatch(matches(c)); ok {
		return *m.TNS, true
	}
	if in.Local == nil {
		return discoveries.TNSEntry{}, false
	}
	for _, h := range in.Local.Hits {
		if h.TNS == nil || h.SepArc > KnownArcsec {
			continue
		}
		t := *h.TNS
		if discoveries.IsNew(c.Class) {
			t.OurClass = c.Class
		}
		t.Seen = firstDetection(c, in.Run)
		t.EarlyData = discoveries.EarlyBefore(t.Seen, t.Discovered)
		return t, true
	}
	return discoveries.TNSEntry{}, false
}

func firstDetection(c app.DiscoveryCandidate, run app.DiscoveryRun) time.Time {
	if p, ok := discovery(c); ok {
		return p.Time.UTC()
	}
	if run.MidTime != nil {
		return run.MidTime.UTC()
	}
	return time.Time{}
}

func usefulLimit(s discoveries.SpotNight, t discoveries.TNSEntry) bool {
	if s.Detected || s.Limit == nil || !discoveries.EarlyBefore(s.Mid, t.Discovered) {
		return false
	}
	return t.Mag == nil || *s.Limit >= *t.Mag
}

// ownReport says whether the TNS object is the one our own discovery
// report made or was filed under.
func ownReport(in Input, t discoveries.TNSEntry) bool {
	s := in.Saved
	return s != nil && (s.SentAt != nil || strings.EqualFold(strings.Join(strings.Fields(s.TNSName), " "), t.Name))
}

// known says what TNS has, and which of our measurements predate its
// discovery. Nothing of ours is early data for an object we reported.
func known(in Input) *Known {
	t, ok := knownEntry(in)
	if !ok {
		return nil
	}
	k := &Known{Name: t.Name, Link: TNSObjectLink(in.Settings.TNSSite, t.Name), Text: discoveries.KnownText(t), Discovered: t.Discovered}
	if ownReport(in, t) {
		return k
	}
	when := t.Discovered.UTC().Format(stampLayout) + " UT"
	if t.EarlyData {
		k.EarlyBasis = append(k.EarlyBasis, fmt.Sprintf("Seen at %s UT; its TNS discovery is %s", t.Seen.UTC().Format(stampLayout), when))
	}
	for _, s := range in.Spot {
		switch {
		case s.Detected && discoveries.EarlyBefore(s.Mid, t.Discovered):
			v := "seen"
			if s.Mag != nil {
				v = fmt.Sprintf("seen at %.2f", *s.Mag)
			}
			k.EarlyBasis = append(k.EarlyBasis, fmt.Sprintf("Night of %s, %s: %s; its TNS discovery is %s", s.Night.Format(time.DateOnly), s.Filter, v, when))
		case usefulLimit(s, t):
			k.EarlyBasis = append(k.EarlyBasis, fmt.Sprintf("Night of %s, %s: not seen, fainter than %.2f; its TNS discovery is %s", s.Night.Format(time.DateOnly), s.Filter, *s.Limit, when))
		}
	}
	if len(k.EarlyBasis) > 0 {
		k.Early, k.EarlyText = true, discoveries.EarlyText
	}
	return k
}

// isClear says whether a filter's magnitudes go to TNS as Clear-: Luminance.
func isClear(filter string) bool { return filter == "Luminance" }

// followupPoints are the detections a photometry report sends: the night's
// broadband stack, as the discovery report sends it, and the Luminance
// detections on earlier nights at the spot.
func followupPoints(in Input, t discoveries.TNSEntry, exposure float64) []FollowupPoint {
	c, run := in.Cand, in.Run
	var out []FollowupPoint
	if p, ok := discovery(c); ok && discoveries.NewLight(c) != nil && run.Group == discoveries.GroupBroadband {
		out = append(out, FollowupPoint{Time: p.Time.UTC(), Night: c.Night.Format(time.DateOnly), Filter: filterWords(run), Mag: discoveries.NewLight(c), MagErr: magErr(c),
			Exposure: exposure, Early: p.Time.Before(t.Discovered), Note: fmt.Sprintf("Night stack of %d subs", run.NightSubs)})
	}
	for _, s := range in.Spot {
		if isClear(s.Filter) && !s.Mid.IsZero() && s.Detected && s.Mag != nil {
			out = append(out, FollowupPoint{Time: s.Mid.UTC(), Night: s.Night.Format(time.DateOnly), Filter: s.Filter, Mag: s.Mag, MagErr: s.MagErr,
				Exposure: s.Exposure, Early: s.Mid.Before(t.Discovered), Note: fmt.Sprintf("Forced photometry, %d subs", s.Subs)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Time.Before(out[b].Time) })
	return out
}

// followupNonDetection is the last Luminance limit before our first
// detection in points.
func followupNonDetection(spot []discoveries.SpotNight, points []FollowupPoint) (discoveries.SpotNight, bool) {
	var first time.Time
	for _, p := range points {
		if p.Mag != nil {
			first = p.Time
			break
		}
	}
	if first.IsZero() {
		return discoveries.SpotNight{}, false
	}
	var best discoveries.SpotNight
	found := false
	for _, s := range spot {
		if s.Detected || s.Limit == nil || !isClear(s.Filter) || s.Mid.IsZero() || !s.Mid.Before(first) {
			continue
		}
		if !found || s.Mid.After(best.Mid) {
			best, found = s, true
		}
	}
	return best, found
}

func followupDraft(in Input, k *Known) FollowupDraft {
	d := FollowupDraft{Points: []FollowupPoint{}}
	t, ok := knownEntry(in)
	if !ok || k == nil {
		return d
	}
	d.Applies, d.Name = k.Early, t.Name
	d.Points = followupPoints(in, t, in.Exposure)
	nd, haveND := followupNonDetection(in.Spot, d.Points)
	if haveND {
		d.NonDetection = fmt.Sprintf("night of %s, %s, fainter than %.2f", nd.Night.Format(time.DateOnly), nd.Filter, *nd.Limit)
	}
	if in.Saved != nil {
		d.SentAt, d.SentReply = in.Saved.FollowupSentAt, in.Saved.FollowupReply
	}
	s := in.Settings
	detections := 0
	for _, p := range d.Points {
		if p.Mag != nil {
			detections++
		}
	}
	switch {
	case !d.Applies:
		d.SendReason = "None of your measurements here are from before its discovery"
	case !s.TNSKey:
		d.SendReason = "Add your TNS bot key in the stacker's settings"
	case s.Reporter == "" || s.TNSGroupID == "" || s.TNSInstrumentID == "":
		d.SendReason = "Set your TNS reporter name, group id and instrument id in the stacker's settings"
	case in.Saved != nil && in.Saved.FollowupSentAt != nil:
		d.SendReason = "Already sent"
	case detections == 0:
		d.SendReason = "No Luminance detection with a measured magnitude"
	case !haveND:
		d.SendReason = "TNS needs a Luminance non-detection before the first detection; measure the earlier nights"
	case uncertaintyMissing(in):
		d.SendReason = "The position's uncertainty is " + notMeasured
	default:
		d.CanSend = true
	}
	return d
}

// atTypeOf is the TNS at type a photometry report of an existing object
// gives: the one our class suggests, else what TNS's name or type says.
func atTypeOf(t discoveries.TNSEntry) string {
	if _, at := typeGuess(app.DiscoveryCandidate{Class: t.OurClass}); at != "" {
		return at
	}
	switch {
	case strings.HasPrefix(t.Name, "SN ") || strings.HasPrefix(t.Type, "SN"):
		return atPSN
	case strings.Contains(strings.ToLower(t.Type), "nova"):
		return atPNV
	}
	return "Other"
}

func uncertaintyMissing(in Input) bool {
	_, ok := uncertainty(in.Cand, in.Run)
	return !ok
}

// FollowupReport is the TNS bulk AT report carrying our photometry of a
// find TNS already has; TNS files it under the existing object.
func FollowupReport(in Input, vals Values) (map[string]any, error) {
	d := followupDraft(in, known(in))
	if !d.CanSend {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, d.SendReason)
	}
	t, _ := knownEntry(in)
	ids, err := idsFor(vals, atTypeOf(t))
	if err != nil {
		return nil, err
	}
	unc, _ := uncertainty(in.Cand, in.Run)
	nd, _ := followupNonDetection(in.Spot, d.Points)
	phot := make([]tnsFlux, 0, len(d.Points))
	for _, p := range d.Points {
		phot = append(phot, tnsFlux{Obs: p.Time, Mag: p.Mag, Err: p.MagErr, Exposure: p.Exposure, Comments: p.Note})
	}
	first := d.Points[0].Time
	ndFlux := tnsFlux{Obs: nd.Mid, Limit: nd.Limit, Exposure: nd.Exposure, Comments: forcedComment}
	entry := atEntry(in.Cand, unc, in.Settings, ids, first, "Photometry of "+t.Name+" from our images", ndFlux, phot)
	return map[string]any{"at_report": map[string]any{"0": entry}}, nil
}

func localCheck(in Input) (Check, bool) {
	if in.Local == nil {
		return Check{}, false
	}
	c := in.Cand
	ck := Check{Key: "tns", Title: "Search TNS by position", Link: TNSSearchLink(in.Settings.TNSSite, c.RA, c.Dec), LinkText: "Open the TNS search for this spot"}
	asOf := in.Local.AsOf.UTC().Format(stampLayout) + " UT"
	var found []string
	for _, h := range in.Local.Hits {
		if h.SepArc > SearchRadius {
			continue
		}
		s := h.ID
		if h.TNS != nil {
			t := *h.TNS
			if discoveries.IsNew(c.Class) {
				t.OurClass = c.Class
			}
			t.Seen = firstDetection(c, in.Run)
			s = strings.TrimPrefix(discoveries.KnownText(t), "already known: ")
		}
		found = append(found, fmt.Sprintf("%s, %.1f″ away", s, h.SepArc))
	}
	if len(found) > 0 {
		ck.State, ck.Detail = CheckBad, fmt.Sprintf("Already in TNS: %s. Checked automatically against the local TNS copy of %s", strings.Join(found, "; "), asOf)
	} else {
		ck.State, ck.Detail = CheckOK, fmt.Sprintf("Checked automatically against the local TNS copy of %s within %g″; nothing there", asOf, SearchRadius)
	}
	return ck, true
}
