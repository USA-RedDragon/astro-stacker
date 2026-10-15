// Package discoveryreport drafts the reports a real find goes to: a TNS
// discovery report for a supernova or nova, an ADES batch for the Minor
// Planet Center for a moving object, and the facts VSX asks for a new
// variable star, from the stored comparison and forced photometry on the
// earlier nights at the find's place. Nothing is sent but on an explicit
// request.
package discoveryreport

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	notMeasured   = "not measured"
	titleAsteroid = "Not a known asteroid"
	titleVariable = "Not a known variable star"
	titleAlerts   = "No alert-feed object at this spot"
	keyAsteroid   = "asteroid"
	keyVariable   = "variable"
	keyAlerts     = "alerts"
)

// Check states.
const (
	CheckOK   = "ok"
	CheckTodo = "todo"
	CheckWait = "wait"
	CheckBad  = "bad"
)

// Check is one line of "Before you report".
type Check struct {
	Key      string `json:"key"`
	State    string `json:"state"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Link     string `json:"link,omitempty"`
	LinkText string `json:"link_text,omitempty"`
}

// Field is one value of a report draft, with where it came from. Missing
// fields hold the reason instead of a value.
type Field struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Missing bool   `json:"missing,omitempty"`
}

// EarlierNight is one row of the earlier nights at the find's place.
type EarlierNight struct {
	Night  string `json:"night"`
	Filter string `json:"filter"`
	Subs   int    `json:"subs"`
	Result string `json:"result"`
	Hit    bool   `json:"hit,omitempty"`
}

// TNSDraft is the TNS tab.
type TNSDraft struct {
	Applies     bool           `json:"applies"`
	Fields      []Field        `json:"fields"`
	Earlier     []EarlierNight `json:"earlier"`
	EarlierNote string         `json:"earlier_note"`
	CopyText    string         `json:"copy_text"`
	CanSend     bool           `json:"can_send"`
	SendReason  string         `json:"send_reason,omitempty"`
	Name        string         `json:"name,omitempty"`
	NameLink    string         `json:"name_link,omitempty"`
	SentAt      *time.Time     `json:"sent_at,omitempty"`
	SentReply   string         `json:"sent_reply,omitempty"`
}

// MPCDraft is the Minor Planet Center tab.
type MPCDraft struct {
	Applies bool   `json:"applies"`
	Reason  string `json:"reason,omitempty"`
	Code    string `json:"code"`
	PSV     string `json:"psv,omitempty"`
	XML     string `json:"xml,omitempty"`
}

// LightPoint is one brightness of the find for the VSX tab.
type LightPoint struct {
	Time   time.Time `json:"time"`
	Filter string    `json:"filter"`
	Mag    float64   `json:"mag"`
	MagErr *float64  `json:"mag_err,omitempty"`
}

// VSXDraft is the variable star tab.
type VSXDraft struct {
	Applies   bool         `json:"applies"`
	Position  string       `json:"position"`
	TypeGuess string       `json:"type_guess"`
	Range     string       `json:"range"`
	Points    []LightPoint `json:"points"`
}

// Spot is the state of the forced photometry on earlier nights.
type Spot struct {
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
}

// Report is everything the report page shows.
type Report struct {
	CandidateID int      `json:"candidate_id"`
	Object      string   `json:"object"`
	Night       string   `json:"night"`
	Class       string   `json:"class"`
	Kind        string   `json:"kind"`
	RA          string   `json:"ra"`
	Dec         string   `json:"dec"`
	Facts       []Field  `json:"facts"`
	Checks      []Check  `json:"checks"`
	TNS         TNSDraft `json:"tns"`
	MPC         MPCDraft `json:"mpc"`
	VSX         VSXDraft `json:"vsx"`
	Spot        Spot     `json:"spot"`
}

// Settings are the reporter's own values.
type Settings struct {
	Reporter        string
	TNSGroupID      string
	TNSInstrumentID string
	MPCCode         string
	// TNSKey says whether a TNS bot key is configured.
	TNSKey bool
}

// Input is what a report is drafted from.
type Input struct {
	Cand       app.DiscoveryCandidate
	Run        app.DiscoveryRun
	Followup   *app.DiscoveryFollowup
	Saved      *app.DiscoveryReport
	Spot       []discoveries.SpotNight
	Instrument string
	Settings   Settings
	// TNSFound are the TNS objects within the search radius when the
	// search ran; nil when it did not.
	TNSFound []string
	TNSError string
}

// SearchRadius is the TNS position search radius in arcseconds.
const SearchRadius = 10.0

// RAText and DecText are sexagesimal positions for the TNS form.
func RAText(deg float64) string {
	deg = math.Mod(math.Mod(deg, 360)+360, 360)
	ms := int64(math.Round(deg / 15 * 3600 * 1000))
	ms %= 24 * 3600 * 1000
	return fmt.Sprintf("%02d:%02d:%06.3f", ms/3600000, ms/60000%60, float64(ms%60000)/1000)
}

// DecText is a declination as ±dd:mm:ss.ss.
func DecText(deg float64) string {
	sign := "+"
	if deg < 0 {
		sign = "-"
	}
	cs := int64(math.Round(math.Abs(deg) * 360000))
	return fmt.Sprintf("%s%02d:%02d:%05.2f", sign, cs/360000, cs/6000%60, float64(cs%6000)/100)
}

func matches(c app.DiscoveryCandidate) []discoveries.Match {
	var ms []discoveries.Match
	if c.Matches != "" {
		_ = json.Unmarshal([]byte(c.Matches), &ms)
	}
	return ms
}

func light(c app.DiscoveryCandidate) []discoveries.LightPoint {
	var ls []discoveries.LightPoint
	if c.Light != "" {
		_ = json.Unmarshal([]byte(c.Light), &ls)
	}
	sort.Slice(ls, func(a, b int) bool { return ls[a].Time.Before(ls[b].Time) })
	return ls
}

func track(c app.DiscoveryCandidate) (discoveries.TrackRecord, bool) {
	var t discoveries.TrackRecord
	if c.Track == "" || json.Unmarshal([]byte(c.Track), &t) != nil || len(t.ADES) == 0 {
		return t, false
	}
	return t, true
}

func has(ms []discoveries.Match, catalog ...string) []discoveries.Match {
	var out []discoveries.Match
	for _, m := range ms {
		if slices.Contains(catalog, m.Catalog) {
			out = append(out, m)
		}
	}
	return out
}

func checked(run app.DiscoveryRun) (map[string]bool, map[string]bool) {
	c, f := map[string]bool{}, map[string]bool{}
	for _, s := range strings.Split(run.Checked, ",") {
		if s = strings.TrimSpace(s); s != "" {
			c[s] = true
		}
	}
	for _, s := range strings.Split(run.Failed, ",") {
		if s = strings.TrimSpace(s); s != "" {
			f[s] = true
		}
	}
	return c, f
}

// TNSSearchLink is the TNS search page for a position.
func TNSSearchLink(ra, dec float64) string {
	q := url.Values{"ra": {fmt.Sprintf("%.6f", ra)}, "decl": {fmt.Sprintf("%.6f", dec)}, "radius": {fmt.Sprintf("%g", SearchRadius)}, "coords_unit": {unitArcsec}}
	return "https://www.wis-tns.org/search?" + q.Encode()
}

// TNSObjectLink is the TNS page of a named object.
func TNSObjectLink(name string) string {
	n := strings.TrimSpace(name)
	for _, p := range []string{"AT", "SN", "TDE", "FRB"} {
		n = strings.TrimSpace(strings.TrimPrefix(n, p+" "))
	}
	if n == "" {
		return ""
	}
	return "https://www.wis-tns.org/object/" + url.PathEscape(n)
}

func checks(in Input) []Check {
	c := in.Cand
	ms := matches(c)
	done, failed := checked(in.Run)
	var out []Check
	switch {
	case failed["SkyBoT"] || !done["SkyBoT"]:
		out = append(out, Check{Key: keyAsteroid, State: CheckTodo, Title: titleAsteroid, Detail: "The known-asteroid check did not run for this night"})
	case len(has(ms, "SkyBoT")) > 0:
		m := has(ms, "SkyBoT")[0]
		out = append(out, Check{Key: keyAsteroid, State: CheckBad, Title: titleAsteroid, Detail: fmt.Sprintf("Matches %s, %.0f″ away", m.Name, m.SepArcsec)})
	default:
		out = append(out, Check{Key: keyAsteroid, State: CheckOK, Title: titleAsteroid, Detail: "Checked against SkyBoT at the night's middle; nothing close"})
	}
	varCats := []string{"VSX", "SIMBAD"}
	var varChecked []string
	for _, v := range varCats {
		if done[v] {
			varChecked = append(varChecked, v)
		}
	}
	switch {
	case !done["VSX"] || failed["VSX"]:
		out = append(out, Check{Key: keyVariable, State: CheckTodo, Title: titleVariable, Detail: "The variable star catalogue was not checked for this night"})
	case len(has(ms, varCats...)) > 0:
		m := has(ms, varCats...)[0]
		out = append(out, Check{Key: keyVariable, State: CheckBad, Title: titleVariable, Detail: fmt.Sprintf("Matches %s %s, %.1f″ away", m.Catalog, m.Name, m.SepArcsec)})
	default:
		out = append(out, Check{Key: keyVariable, State: CheckOK, Title: titleVariable, Detail: "Checked " + strings.Join(varChecked, " and ") + "; nothing within 5″"})
	}
	brokers := []string{"ALeRCE", "Fink"}
	var asked, down []string
	for _, b := range brokers {
		if failed[b] {
			down = append(down, b)
		} else if done[b] {
			asked = append(asked, b)
		}
	}
	switch {
	case len(has(ms, "ALeRCE", "Fink", "TNS")) > 0:
		m := has(ms, "ALeRCE", "Fink", "TNS")[0]
		out = append(out, Check{Key: keyAlerts, State: CheckBad, Title: titleAlerts, Detail: fmt.Sprintf("%s knows it as %s", m.Catalog, m.Name)})
	case len(down) > 0:
		out = append(out, Check{Key: keyAlerts, State: CheckTodo, Title: titleAlerts, Detail: strings.Join(down, " and ") + " could not be reached for this night"})
	case len(asked) == 0:
		out = append(out, Check{Key: keyAlerts, State: CheckTodo, Title: titleAlerts, Detail: "The alert brokers were not asked about this spot"})
	default:
		out = append(out, Check{Key: keyAlerts, State: CheckOK, Title: titleAlerts, Detail: "ZTF alert brokers " + strings.Join(asked, " and ") + "; nothing within 3″"})
	}
	tns := Check{Key: "tns", Title: "Search TNS by position", Link: TNSSearchLink(c.RA, c.Dec), LinkText: "Open the TNS search for this spot"}
	switch {
	case in.TNSError != "":
		tns.State, tns.Detail = CheckTodo, "The TNS search failed: "+in.TNSError
	case in.TNSFound == nil && !in.Settings.TNSKey:
		tns.State, tns.Detail = CheckTodo, "Needs your TNS key to check automatically"
	case in.TNSFound == nil:
		tns.State, tns.Detail = CheckTodo, "Not searched yet"
	case len(in.TNSFound) > 0:
		tns.State, tns.Detail = CheckBad, "Already in TNS: "+strings.Join(in.TNSFound, ", ")
	default:
		tns.State, tns.Detail = CheckOK, fmt.Sprintf("Searched TNS within %g″; nothing there", SearchRadius)
	}
	out = append(out, tns)
	out = append(out, secondNight(in.Followup))
	if c.Vetting == app.VetReal {
		out = append(out, Check{Key: "vetted", State: CheckOK, Title: "Looked at it yourself", Detail: "Marked Real change"})
	} else {
		out = append(out, Check{Key: "vetted", State: CheckTodo, Title: "Looked at it yourself", Detail: "Mark it Real change on the Discoveries page"})
	}
	return out
}

func secondNight(f *app.DiscoveryFollowup) Check {
	c := Check{Key: "second_night", Title: "Second night"}
	if f == nil {
		c.State, c.Detail = CheckTodo, "No follow-up was asked for; image the field again before reporting"
		return c
	}
	switch f.State {
	case app.FollowupConfirmed:
		c.State, c.Detail = CheckOK, "Seen again: "+f.Note
	case app.FollowupNotConfirmed:
		c.State, c.Detail = CheckBad, "Not seen again: "+f.Note
	case app.FollowupExpired:
		c.State, c.Detail = CheckBad, "The follow-up expired: "+f.Note
	case app.FollowupFailed:
		c.State, c.Detail = CheckBad, "The follow-up could not be scheduled: "+f.Note
	default:
		what := f.Object + ", " + f.Group
		if f.Subs > 0 {
			what = fmt.Sprintf("%s, %d × %gs %s", f.Object, f.Subs, f.Exposure, f.Filter)
		}
		c.State, c.Detail = CheckWait, fmt.Sprintf("Follow-up asked for: %s, until the night of %s", what, f.Until.Format("Jan 2"))
	}
	return c
}

// discovery is the first sub showing the find at 3 sigma.
func discovery(c app.DiscoveryCandidate) (discoveries.LightPoint, bool) {
	for _, p := range light(c) {
		if p.FluxErr > 0 && float64(c.Sign)*p.Flux >= 3*p.FluxErr {
			return p, true
		}
	}
	return discoveries.LightPoint{}, false
}

func filterWords(run app.DiscoveryRun) string {
	fs := []string{}
	for _, f := range strings.Split(run.Filters, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return notMeasured
	}
	if run.Group == discoveries.GroupBroadband {
		return "Clear (" + strings.Join(fs, ", ") + ")"
	}
	return strings.Join(fs, ", ")
}

// newLightSource says where a discovery magnitude comes from: the light
// added since the reference, with the spot's total light and what was
// there before as context.
func newLightSource(c app.DiscoveryCandidate) string {
	switch {
	case c.Kind == app.DiscoveryKindMover:
		return "The mover in the night's subs, Gaia G zero point"
	case c.NightMag != nil && c.RefMag != nil:
		return fmt.Sprintf("Light added since the reference, from the difference, Gaia G zero point; the spot's total is %.2f, %.2f before", *c.NightMag, *c.RefMag)
	}
	return "Night stack, Gaia G zero point; nothing measured there before"
}

func magErr(c app.DiscoveryCandidate) *float64 {
	if c.SNR > 0 {
		e := 1.0857 / c.SNR
		return &e
	}
	return nil
}

// uncertainty is the position error in arcseconds: the plate solution's
// fit to Gaia and the centroid, as the ADES rms is.
func uncertainty(c app.DiscoveryCandidate, run app.DiscoveryRun) (float64, bool) {
	if run.AstroRMS == nil || !(c.SNR > 0) || !(run.PixelScale > 0) {
		return 0, false
	}
	cen := run.HFRNight * 2 / math.Max(c.SNR, 1) * run.PixelScale
	return math.Hypot(*run.AstroRMS, cen), true
}

func host(c app.DiscoveryCandidate) (discoveries.Match, bool) {
	g := has(matches(c), "galaxy")
	if len(g) == 0 {
		return discoveries.Match{}, false
	}
	return g[0], true
}

// typeGuess is the TNS object type the class suggests, and its at_type
// name.
func typeGuess(c app.DiscoveryCandidate) (string, string) {
	switch c.Class {
	case discoveries.ClassSupernova:
		return "Supernova (PSN)", "PSN"
	case "nova_candidate":
		return "Nova (PNV)", "PNV"
	case discoveries.ClassNewVariable:
		return "Variable star; report it to VSX, not TNS", ""
	case discoveries.ClassUnknownMover:
		return "Moving object; report it to the Minor Planet Center", ""
	}
	return strings.ReplaceAll(c.Class, "_", " "), ""
}

// lastNonDetection is the most recent earlier night that did not show the
// find with a 3 sigma limit fainter than its discovery magnitude, or the
// deepest limit when none is that deep.
func lastNonDetection(spot []discoveries.SpotNight, mag *float64) (discoveries.SpotNight, bool) {
	var deep, all []discoveries.SpotNight
	for _, s := range spot {
		if s.Detected || s.Limit == nil {
			continue
		}
		all = append(all, s)
		if mag != nil && *s.Limit >= *mag {
			deep = append(deep, s)
		}
	}
	if len(deep) > 0 {
		return slices.MaxFunc(deep, func(a, b discoveries.SpotNight) int { return a.Night.Compare(b.Night) }), true
	}
	if len(all) > 0 {
		return slices.MaxFunc(all, func(a, b discoveries.SpotNight) int { return cmpFloat(*a.Limit, *b.Limit) }), true
	}
	return discoveries.SpotNight{}, false
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func field(key, label, value, source string) Field {
	return Field{Key: key, Label: label, Value: value, Source: source}
}

func missing(key, label, why string) Field {
	return Field{Key: key, Label: label, Value: "Not set", Source: why, Missing: true}
}

func unmeasured(key, label, why string) Field {
	return Field{Key: key, Label: label, Value: notMeasured, Source: why, Missing: true}
}

func tnsDraft(in Input) TNSDraft {
	c, run := in.Cand, in.Run
	d := TNSDraft{Applies: c.Kind == app.DiscoveryKindChange}
	unc, uok := uncertainty(c, run)
	if uok {
		src := fmt.Sprintf("Plate solution tied to Gaia, ±%.1f″", unc)
		d.Fields = append(d.Fields, field("ra", "Right ascension", RAText(c.RA), src), field("dec", "Declination", DecText(c.Dec), src))
	} else {
		d.Fields = append(d.Fields, field("ra", "Right ascension", RAText(c.RA), "Uncertainty not measured"),
			field("dec", "Declination", DecText(c.Dec), "Uncertainty not measured"))
	}
	if p, ok := discovery(c); ok {
		d.Fields = append(d.Fields, field("discovery_time", "Discovery time (UT)", p.Time.UTC().Format("2006-01-02 15:04:05"), "Middle of the first sub with the source at 3σ"))
	} else {
		d.Fields = append(d.Fields, unmeasured("discovery_time", "Discovery time (UT)", "No per-sub brightness for this candidate"))
	}
	filter := filterWords(run)
	if m := discoveries.NewLight(c); m != nil {
		v := fmt.Sprintf("%.2f", *m)
		if e := magErr(c); e != nil {
			v += fmt.Sprintf(" ± %.2f", *e)
		}
		d.Fields = append(d.Fields, field("mag", "Discovery magnitude", v, newLightSource(c)))
	} else {
		d.Fields = append(d.Fields, unmeasured("mag", "Discovery magnitude", "No zero point for this night"))
	}
	src := "TNS has no L filter; Clear is the match"
	if run.Group != discoveries.GroupBroadband {
		src = "Narrowband; TNS takes broadband magnitudes"
	}
	d.Fields = append(d.Fields, field("filter", "Filter", filter, src))
	if s, ok := lastNonDetection(in.Spot, discoveries.NewLight(c)); ok {
		d.Fields = append(d.Fields, field("non_detection", "Last time not seen", fmt.Sprintf("night of %s, fainter than %.2f", s.Night.Format(time.DateOnly), *s.Limit),
			fmt.Sprintf("%s, %d subs; forced photometry at this spot, 3σ", s.Filter, s.Subs)))
	} else {
		d.Fields = append(d.Fields, unmeasured("non_detection", "Last time not seen", "The earlier nights at this spot are not measured yet"))
	}
	if h, ok := host(c); ok {
		d.Fields = append(d.Fields, field("host", "Host galaxy", h.Name, fmt.Sprintf("From the catalogue, %.1f′ from the centre", h.SepArcsec/60)))
	} else {
		d.Fields = append(d.Fields, field("host", "Host galaxy", "none", "No catalogued galaxy at this spot"))
	}
	guess, _ := typeGuess(c)
	d.Fields = append(d.Fields, field("type", "Object type (your guess)", guess, "From the comparison's class"))
	if in.Instrument != "" {
		d.Fields = append(d.Fields, field("instrument", "Instrument", in.Instrument, "From your lights' headers"))
	} else {
		d.Fields = append(d.Fields, unmeasured("instrument", "Instrument", "No telescope or camera in the lights' headers"))
	}
	if in.Settings.Reporter != "" {
		d.Fields = append(d.Fields, field("reporter", "Reporter and group", in.Settings.Reporter, "From the stacker's settings"))
	} else {
		d.Fields = append(d.Fields, missing("reporter", "Reporter and group", "Your TNS name and group, set once in the stacker's settings"))
	}
	d.Earlier, d.EarlierNote = earlier(c, run, in.Spot)
	d.CopyText = copyText(d.Fields)
	d.CanSend, d.SendReason = canSend(in, d)
	if in.Saved != nil {
		d.Name = in.Saved.TNSName
		d.NameLink = TNSObjectLink(in.Saved.TNSName)
		d.SentAt = in.Saved.SentAt
		d.SentReply = in.Saved.SentReply
	}
	return d
}

func canSend(in Input, d TNSDraft) (bool, string) {
	s := in.Settings
	_, at := typeGuess(in.Cand)
	switch {
	case !d.Applies || at == "":
		return false, "TNS takes supernovae and novae; this is not one"
	case !s.TNSKey:
		return false, "Add your TNS bot key in the stacker's settings"
	case s.Reporter == "" || s.TNSGroupID == "" || s.TNSInstrumentID == "":
		return false, "Set your TNS reporter name, group id and instrument id in the stacker's settings"
	case in.Run.Group != discoveries.GroupBroadband:
		return false, "TNS takes broadband magnitudes; this find is narrowband"
	case in.Saved != nil && in.Saved.SentAt != nil:
		return false, "Already sent"
	}
	for _, f := range d.Fields {
		if f.Missing && f.Key != "reporter" {
			return false, f.Label + " is " + notMeasured
		}
	}
	return true, ""
}

func copyText(fs []Field) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s: %s\n", f.Label, f.Value)
	}
	return b.String()
}

func earlier(c app.DiscoveryCandidate, run app.DiscoveryRun, spot []discoveries.SpotNight) ([]EarlierNight, string) {
	filter := filterWords(run)
	hit := EarlierNight{Night: c.Night.Format(time.DateOnly), Filter: filter, Subs: run.NightSubs, Hit: true, Result: notMeasured}
	if m := discoveries.NewLight(c); m != nil {
		hit.Result = fmt.Sprintf("%.2f", *m)
		if e := magErr(c); e != nil {
			hit.Result += fmt.Sprintf(" ± %.2f", *e)
		}
	}
	out := make([]EarlierNight, 0, 1+len(spot))
	out = append(out, hit)
	nights := map[string]bool{}
	shown := 0
	for _, s := range spot {
		nights[s.Night.Format(time.DateOnly)] = true
		r := EarlierNight{Night: s.Night.Format(time.DateOnly), Filter: s.Filter, Subs: s.Subs}
		switch {
		case s.Detected && s.Mag != nil:
			r.Result = fmt.Sprintf("seen, %.2f ± %.2f", *s.Mag, *s.MagErr)
			shown++
		case s.Detected:
			r.Result = fmt.Sprintf("seen at %.1fσ", s.SNR)
			shown++
		case s.Limit != nil:
			r.Result = fmt.Sprintf("fainter than %.2f", *s.Limit)
		default:
			r.Result = "no zero point"
		}
		out = append(out, r)
	}
	if len(spot) == 0 {
		return out, "The earlier nights at this spot are not measured yet."
	}
	note := fmt.Sprintf("%d earlier nights measured at this spot, the most recent first; ", len(nights))
	if shown == 0 {
		note += "no detection on any. Limits are 3σ."
	} else {
		note += fmt.Sprintf("seen on %d of them. Limits are 3σ.", shown)
	}
	return out, note
}

func mpcDraft(in Input) MPCDraft {
	d := MPCDraft{Applies: in.Cand.Kind == app.DiscoveryKindMover, Code: in.Settings.MPCCode}
	if !d.Applies {
		d.Reason = "Only moving objects go to the Minor Planet Center"
		return d
	}
	t, ok := track(in.Cand)
	if !ok {
		d.Reason = "No stored detections for this moving object"
		return d
	}
	if d.Code == "" {
		d.Reason = "Set your observatory code in the stacker's settings; the MPC needs it"
	}
	o := Observer{MPCCode: d.Code, Reporter: in.Settings.Reporter, Telescope: in.Instrument}
	if d.Code == "" {
		o.MPCCode = "XXX"
	}
	d.PSV = ADESPSV(t, o)
	if x, err := ADESXML(t, o); err == nil {
		d.XML = x
	}
	return d
}

func vsxDraft(in Input) VSXDraft {
	c := in.Cand
	d := VSXDraft{Applies: c.Kind == app.DiscoveryKindChange, Position: RAText(c.RA) + " " + DecText(c.Dec)}
	for _, p := range light(c) {
		if p.Mag != nil {
			d.Points = append(d.Points, LightPoint{Time: p.Time, Filter: p.Filter, Mag: *p.Mag, MagErr: p.MagErr})
		}
	}
	for _, s := range in.Spot {
		if s.Detected && s.Mag != nil && !s.Mid.IsZero() {
			d.Points = append(d.Points, LightPoint{Time: s.Mid, Filter: s.Filter, Mag: *s.Mag, MagErr: s.MagErr})
		}
	}
	sort.Slice(d.Points, func(a, b int) bool { return d.Points[a].Time.Before(d.Points[b].Time) })
	if len(d.Points) == 0 {
		d.Range = notMeasured
	} else {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, p := range d.Points {
			lo, hi = math.Min(lo, p.Mag), math.Max(hi, p.Mag)
		}
		d.Range = fmt.Sprintf("%.2f to %.2f", lo, hi)
	}
	switch {
	case c.DeltaMag != nil && c.RefMag != nil:
		word := "brighter"
		if *c.DeltaMag > 0 {
			word = "fainter"
		}
		d.TypeGuess = fmt.Sprintf("Unknown type; %.2f magnitudes %s than its usual %.2f", math.Abs(*c.DeltaMag), word, *c.RefMag)
	case c.Sign > 0:
		d.TypeGuess = "Unknown type; nothing measured there before"
	default:
		d.TypeGuess = "Unknown type; it faded"
	}
	return d
}

// Build drafts the report.
func Build(in Input) Report {
	c := in.Cand
	r := Report{CandidateID: c.ID, Object: c.Object, Night: c.Night.Format(time.DateOnly), Class: c.Class, Kind: c.Kind,
		RA: RAText(c.RA), Dec: DecText(c.Dec)}
	if m := discoveries.NewLight(c); m != nil {
		v := fmt.Sprintf("%.2f", *m)
		if e := magErr(c); e != nil {
			v += fmt.Sprintf(" ± %.2f", *e)
		}
		r.Facts = append(r.Facts, field("brightness", "Brightness", v, filterWords(in.Run)+"; "+newLightSource(c)))
	} else if c.NightMag != nil {
		r.Facts = append(r.Facts, field("brightness", "Brightness", fmt.Sprintf("%.2f at the spot", *c.NightMag), filterWords(in.Run)+"; it faded, so no new light"))
	} else {
		r.Facts = append(r.Facts, unmeasured("brightness", "Brightness", "No zero point for this night"))
	}
	if c.SubsTested > 0 {
		r.Facts = append(r.Facts, field("subs", "Per sub", fmt.Sprintf("In %d of %d subs", c.PresentIn, c.SubsTested), "Per-sub difference at 3σ"))
	}
	if h, ok := host(c); ok {
		r.Facts = append(r.Facts, field("host", "Host", fmt.Sprintf("%s, %.1f′ from the centre", h.Name, h.SepArcsec/60), "Catalogued galaxy"))
	}
	if c.GaiaG != nil {
		r.Facts = append(r.Facts, field("star", "Star there", fmt.Sprintf("Gaia G %.2f", *c.GaiaG), "Gaia DR3"))
	} else if c.Kind == app.DiscoveryKindChange {
		r.Facts = append(r.Facts, field("star", "Star there", "None in Gaia", "Gaia DR3"))
	}
	r.Checks = checks(in)
	r.TNS = tnsDraft(in)
	r.MPC = mpcDraft(in)
	r.VSX = vsxDraft(in)
	r.Spot = Spot{State: SpotNone}
	if in.Saved != nil {
		r.Spot = Spot{State: in.Saved.SpotState, Error: in.Saved.SpotError, MeasuredAt: in.Saved.SpotAt}
		if r.Spot.State == "" {
			r.Spot.State = SpotNone
		}
	}
	return r
}
