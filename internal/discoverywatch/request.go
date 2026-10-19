package discoverywatch

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	// NightsAsked is how many nights a watch asks imaging for, spread over
	// SpanDays from the first night still to come.
	NightsAsked = 5
	SpanDays    = 21
	// MinutesPerNight is the exposure a watch asks for on each night.
	MinutesPerNight = 60
	// Author signs the scheduler commands.
	Author = "discovery watch"
	// ProjectPrefix starts every watch project's name.
	ProjectPrefix = "Watch: "
	// projectPriorityNormal is Target Scheduler's normal project priority.
	projectPriorityNormal = 1
)

// PlanNights is the nights a watch asks for from now: the first night still
// to come, then NightsAsked-1 more spread evenly to the end of SpanDays.
func PlanNights(now time.Time, loc *time.Location) []time.Time {
	first := discoveryfollowup.FirstNight(now, loc)
	out := make([]time.Time, NightsAsked)
	for i := range out {
		out[i] = first.AddDate(0, 0, i*(SpanDays-1)/(NightsAsked-1))
	}
	return out
}

// NightWindow is one night by its evening date: from local noon that day
// to local noon the next.
func NightWindow(night time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	start := time.Date(night.Year(), night.Month(), night.Day(), 12, 0, 0, 0, loc)
	return start, discoveryfollowup.WindowEnd(night, loc)
}

func uuidFrom(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// NightGUID is the project of one watched night.
func NightGUID(sourceID, batch int, night time.Time) string {
	return uuidFrom(fmt.Sprintf("discovery-watch:%d:%d:%s", sourceID, batch, night.Format(time.DateOnly)))
}

// Subs is how many subs of exposure seconds fill MinutesPerNight.
func Subs(exposure float64) int {
	if exposure <= 0 {
		return 1
	}
	return max(1, int(math.Ceil(MinutesPerNight*60/exposure)))
}

// Draft is the Target Scheduler project of one watched night: the field's
// own target position, framing and name, so its subs join the field's
// night comparisons, one plan in the filter group the find was detected
// in for MinutesPerNight of exposure, a normal project priority and a
// normal event window over the night. A normal window only fills gaps: the
// plugin ranks it below the targets planned for the night and below high
// windows such as eclipse requests. A nil payload comes with the reason.
func Draft(snap *planning.Snapshot, origGUID, object string, n *app.DiscoveryWatchNight, group string) (*schedcmd.ProjectCreatePayload, string) {
	var proj planning.Project
	var tgt planning.Target
	found := false
	for _, p := range snap.Projects {
		if strings.HasPrefix(p.Name, ProjectPrefix) || strings.HasPrefix(p.Name, discoveryfollowup.ProjectPrefix) || strings.HasPrefix(p.Name, binaries.RequestPrefix) {
			continue
		}
		for _, t := range p.Targets {
			if (origGUID != "" && t.GUID == origGUID) || (origGUID == "" && t.Name == object && !found) {
				proj, tgt, found = p, t, true
			}
		}
	}
	if !found || tgt.RAHours == nil || tgt.Dec == nil {
		return nil, "no Target Scheduler target with a position images " + object
	}
	if tgt.BodyKind != "" {
		return nil, object + " follows a moving body; its field can't be imaged for this star"
	}
	plan, ok := discoveryfollowup.PickPlan(tgt.Plans, group)
	if !ok {
		return nil, object + " has no exposure plan in the " + group + " filters"
	}
	if plan.Exposure <= 0 {
		return nil, "the " + plan.Filter + " plan of " + object + " has no exposure length"
	}
	settings, err := snap.SettingsFor(plan.TemplateID, proj.MinimumAltitude)
	if err != nil {
		return nil, err.Error()
	}
	discoveryfollowup.KeepLimits(&settings, proj)
	tmplID, tmplName, filter := plan.TemplateID, plan.Template, plan.Filter
	if t, ok := snap.ResolveFilter(proj.ProfileID, plan.Filter); ok {
		tmplID, tmplName, filter = t.ID, t.Name, snap.PlanFilter(t)
	} else if t, ok := discoveryfollowup.TemplateByID(snap, plan.TemplateID); ok {
		tmplName, filter = t.Name, snap.PlanFilter(t)
	}
	if filter == "" {
		filter = plan.Filter
	}
	rotation := 0.0
	if tgt.Rotation != nil {
		rotation = *tgt.Rotation
	}
	n.ProjectGUID = NightGUID(n.SourceID, n.Batch, n.Night)
	n.TargetGUID = uuidFrom("discovery-watch-target:" + n.ProjectGUID)
	n.Filter, n.Exposure, n.Subs, n.Minutes = filter, plan.Exposure, Subs(plan.Exposure), MinutesPerNight
	day := n.Night.Format(time.DateOnly)
	payload := &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: n.ProjectGUID, Name: fmt.Sprintf(ProjectPrefix+"%s (%s, %s)", object, day, group),
			Description: fmt.Sprintf("Looks again at a find in %s the vet calls real but cannot settle, on the night of %s: %d × %gs %s, in gaps between planned targets only. Made by the stacker; it is closed once the night has passed.",
				object, day, n.Subs, n.Exposure, filter),
			Priority: projectPriorityNormal, State: 1, MinimumTime: MinutesPerNight / 2, MinimumAltitude: proj.MinimumAltitude, ProjectSettings: settings},
		Targets: []schedcmd.NewTarget{{GUID: n.TargetGUID, Name: tgt.Name, RAHours: *tgt.RAHours, Dec: *tgt.Dec, Rotation: rotation,
			Window: &schedcmd.EventWindow{Start: n.Start.UTC(), End: n.End.UTC(), Priority: schedcmd.EventPriorityNormal, EventID: "discovery-watch-" + n.ProjectGUID},
			Plans: []schedcmd.NewPlan{{GUID: uuidFrom("discovery-watch-plan:" + n.ProjectGUID), TemplateID: int64(tmplID), TemplateName: tmplName,
				Filter: filter, Exposure: n.Exposure, Desired: n.Subs}}}},
		RuleWeights: planning.WeightsFor(snap.GlobalWeights.Weights, false),
	}
	return payload, ""
}
