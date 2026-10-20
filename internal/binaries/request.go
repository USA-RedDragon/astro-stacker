package binaries

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// RequestAuthor signs the scheduler commands of eclipse requests.
const RequestAuthor = "eclipse planner"

// RequestPrefix starts every eclipse request project's name.
const RequestPrefix = "Eclipse: "

const (
	// overheadSeconds is the time between subs counted when sizing a
	// request: download, dither and settle.
	overheadSeconds = 15
	// maxSubs caps the subs one request asks for.
	maxSubs = 400
	// samplesPerEclipse is how many subs at least one eclipse duration
	// should hold, to time its middle.
	samplesPerEclipse = 20
)

// SubLength is the sub length for a star of Gaia magnitude g in a broadband
// filter: long enough for a clear measurement, short enough not to
// saturate a bright star and to sample the eclipse samplesPerEclipse times,
// and no longer than the field's own plan.
func SubLength(g float64, duration time.Duration, planExposure float64) float64 {
	var s float64
	switch {
	case g <= 0:
		s = 120
	case g < 12:
		s = 30
	case g < 14:
		s = 60
	case g < 16:
		s = 120
	default:
		s = 180
	}
	if duration > 0 {
		s = math.Min(s, duration.Seconds()/samplesPerEclipse)
	}
	if planExposure > 0 {
		s = math.Min(s, planExposure)
	}
	s = math.Max(10, math.Round(s/10)*10)
	return s
}

// pickFilter is the plan whose filter best matches ZTF r: Red first, then
// Luminance, then any broadband plan, enabled before disabled.
func pickFilter(plans []planning.Plan) (planning.Plan, bool) {
	rank := func(p planning.Plan) int {
		if discoveries.GroupOf(p.Filter) != discoveries.GroupBroadband {
			return -1
		}
		r := 1
		switch strings.ToLower(p.Filter) {
		case "red", "r":
			r += 4
		case "luminance", "l":
			r += 2
		}
		if p.Enabled {
			r += 8
		}
		return r
	}
	best, bestRank := planning.Plan{}, -1
	for _, p := range plans {
		if r := rank(p); r > bestRank {
			best, bestRank = p, r
		}
	}
	return best, bestRank >= 0
}

func uuidFrom(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// RequestGUID is the project of one attempt to image an eclipse.
func RequestGUID(sourceID int, kind string, mid time.Time, attempt int) string {
	seed := fmt.Sprintf("eclipse-request:%d:%s:%s", sourceID, kind, mid.UTC().Format(time.RFC3339))
	if attempt > 0 {
		seed += fmt.Sprintf(":%d", attempt)
	}
	return uuidFrom(seed)
}

// requestField is the field a request images: the project and target
// that image object, its plan in the filter nearest ZTF r and the plan's
// settings, or why there is none.
type requestField struct {
	proj     planning.Project
	tgt      planning.Target
	plan     planning.Plan
	settings schedcmd.ProjectSettings
}

func fieldOf(snap *planning.Snapshot, origGUID, object string) (requestField, string) {
	var f requestField
	found := false
	for _, p := range snap.Projects {
		if strings.HasPrefix(p.Name, RequestPrefix) || strings.HasPrefix(p.Name, discoveryfollowup.ProjectPrefix) {
			continue
		}
		for _, t := range p.Targets {
			if (origGUID != "" && t.GUID == origGUID) || (origGUID == "" && t.Name == object && !found) {
				f.proj, f.tgt, found = p, t, true
			}
		}
	}
	if !found || f.tgt.RAHours == nil || f.tgt.Dec == nil {
		return f, "no Target Scheduler target with a position images " + object
	}
	if f.tgt.BodyKind != "" {
		return f, object + " follows a moving body; its field can't be imaged for this star"
	}
	plan, ok := pickFilter(f.tgt.Plans)
	if !ok {
		return f, object + " has no broadband exposure plan"
	}
	f.plan = plan
	settings, err := snap.SettingsFor(plan.TemplateID, f.proj.MinimumAltitude)
	if err != nil {
		return f, err.Error()
	}
	discoveryfollowup.KeepLimits(&settings, f.proj)
	f.settings = settings
	return f, ""
}

// template is the exposure template, its name and the filter of the
// field's plan in the project's profile.
func (f requestField) template(snap *planning.Snapshot) (int, string, string) {
	plan := f.plan
	tmplID, tmplName, filter := plan.TemplateID, plan.Template, plan.Filter
	if t, ok := snap.ResolveFilter(f.proj.ProfileID, plan.Filter); ok {
		tmplID, tmplName, filter = t.ID, t.Name, snap.PlanFilter(t)
	} else if t, ok := discoveryfollowup.TemplateByID(snap, plan.TemplateID); ok {
		tmplName, filter = t.Name, snap.PlanFilter(t)
	}
	return tmplID, tmplName, filter
}

func (f requestField) rotation() float64 {
	if f.tgt.Rotation != nil {
		return *f.tgt.Rotation
	}
	return 0
}

// DraftRequest is the Target Scheduler project that images one eclipse:
// the field's own target position and framing, one plan in the filter
// nearest ZTF r, and a high-priority window from start to end so the
// plugin images it only then. A nil payload comes with the reason.
func DraftRequest(snap *planning.Snapshot, origGUID string, r *app.EclipseRequest, name, object string, g float64, duration time.Duration, loc *time.Location) (*schedcmd.ProjectCreatePayload, string) {
	f, why := fieldOf(snap, origGUID, object)
	if why != "" {
		return nil, why
	}
	proj, tgt, plan, settings := f.proj, f.tgt, f.plan, f.settings
	exposure := SubLength(g, duration, plan.Exposure)
	window := r.End.Sub(r.Start)
	subs := min(maxSubs, max(1, int(math.Ceil(window.Seconds()/(exposure+overheadSeconds)))))
	tmplID, tmplName, filter := f.template(snap)
	rotation := f.rotation()
	kind := "deeper eclipse"
	if r.Kind != KindPrimary {
		kind = "second eclipse"
	}
	local := r.Mid.In(loc).Format("2006-01-02 3:04 PM MST")
	r.ProjectGUID, r.TargetGUID, r.Filter, r.Exposure, r.Subs = RequestGUID(r.SourceID, r.Kind, r.Mid, r.Attempt), uuidFrom("eclipse-request-target:"+RequestGUID(r.SourceID, r.Kind, r.Mid, r.Attempt)), filter, exposure, subs
	minutes := int(math.Ceil(window.Minutes()))
	payload := &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: r.ProjectGUID, Name: fmt.Sprintf(RequestPrefix+"%s (%s, %s)", name, local, kind),
			Description: fmt.Sprintf("Images the %s of %s in %s, predicted for %s, from %s to %s UTC: %d × %gs %s. Made by the eclipse planner; it is closed once the window has passed.",
				kind, name, object, local, r.Start.UTC().Format("Jan 2 15:04"), r.End.UTC().Format("Jan 2 15:04"), subs, exposure, filter),
			Priority: 2, State: 1, MinimumTime: min(minutes, 30), MinimumAltitude: proj.MinimumAltitude, ProjectSettings: settings},
		Targets: []schedcmd.NewTarget{{GUID: r.TargetGUID, Name: tgt.Name, RAHours: *tgt.RAHours, Dec: *tgt.Dec, Rotation: rotation,
			Window: &schedcmd.EventWindow{Start: r.Start.UTC(), End: r.End.UTC(), Priority: schedcmd.EventPriorityHigh, EventID: "eclipse-" + r.ProjectGUID},
			Plans: []schedcmd.NewPlan{{GUID: uuidFrom("eclipse-request-plan:" + r.ProjectGUID), TemplateID: int64(tmplID), TemplateName: tmplName,
				Filter: filter, Exposure: exposure, Desired: subs}}}},
		RuleWeights: planning.WeightsFor(snap.GlobalWeights.Weights, false),
	}
	return payload, ""
}

