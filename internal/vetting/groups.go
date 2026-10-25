package vetting

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func groupNames() map[string]string {
	return map[string]string{
		check.GroupImage:     "Image checks",
		check.GroupOwn:       "Your own measurements",
		check.GroupSurveys:   "Survey history",
		check.GroupCatalog:   "Catalogs",
		check.GroupMoving:    "Moving object check",
		check.GroupMover:     "Moving object",
		check.GroupTransient: "New light at the spot",
	}
}

func pick(have []check.Check, ids []string, missing string) []check.Check {
	by := map[string]check.Check{}
	for _, c := range have {
		by[c.ID] = c
	}
	out := make([]check.Check, 0, len(ids))
	for _, id := range ids {
		if c, ok := by[id]; ok {
			if c.Name == "" {
				c.Name = check.Names()[id]
			}
			out = append(out, c)
			continue
		}
		out = append(out, check.Skipped(id, missing))
	}
	return out
}

func groupsOf(sum discoverysource.Summary, own []check.Check, fx fetches, pr *period.Result, timing *period.Timing, known Known, moving check.Check) []Group {
	names := groupNames()
	g := func(id string, cs []check.Check) Group { return Group{ID: id, Name: names[id], Checks: cs} }
	out := []Group{
		g(check.GroupImage, pick(own, []string{check.HotPixel, check.RefClean, check.Trail, check.PSF, check.Edge}, "Not measured yet.")),
		g(check.GroupOwn, pick(own, []string{check.OwnLC, check.OwnChange, check.OwnQuiet}, "Not measured yet.")),
	}
	if sum.Source.Kind != kindMover {
		out = append(out, g(check.GroupSurveys, surveyRows(fx, pr, timing)))
		out = append(out, g(check.GroupCatalog, pick(known.Checks, []string{check.VSX, check.GaiaVari, check.SurveyVar, check.TNS, check.SIMBAD}, "Not run yet.")))
	}
	out = append(out, g(check.GroupMoving, []check.Check{moving}))
	return out
}

func surveyRows(fx fetches, pr *period.Result, timing *period.Timing) []check.Check {
	var have []check.Check
	if pr != nil {
		have = pr.Checks
	}
	ids := []string{check.ZTF, check.Period, check.TESS, check.ATLAS, check.NEOWISE}
	if pr != nil && pr.Ephemeris != nil {
		ids = []string{check.ZTF, check.Fold, check.Half, check.Double, check.Echo, check.Compare, check.TESS, check.OwnTiming, check.ATLAS, check.NEOWISE}
		have = append(have, timingCheck(pr.Ephemeris, timing))
	}
	if pr == nil || pr.Ephemeris == nil {
		if !hasCheck(have, check.Period) {
			have = append(have, check.Skipped(check.Period, "No period to search: no survey light curve of the star."))
		}
	}
	rows := pick(have, ids, "Not run yet.")
	for i, r := range rows {
		var f surveys.Fetch
		switch r.ID {
		case check.ZTF:
			f = fx.ztf
		case check.TESS:
			f = fx.tess
		case check.ATLAS:
			f = fx.atlas
		case check.NEOWISE:
			f = fx.neowise
		default:
			continue
		}
		if c, ok := fetchRow(r.ID, f, hasCheck(have, r.ID)); ok {
			rows[i] = c
		}
	}
	return rows
}

func hasCheck(cs []check.Check, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

func fetchRow(id string, f surveys.Fetch, analysed bool) (check.Check, bool) {
	note := func(def string) string {
		n := strings.TrimSpace(f.Note)
		if n == "" {
			return def
		}
		n = strings.ToUpper(n[:1]) + n[1:]
		if !strings.HasSuffix(n, ".") {
			n += "."
		}
		return n
	}
	switch f.State {
	case "":
		return check.Skipped(id, note("Not fetched yet.")), true
	case surveys.Pending:
		if analysed && len(f.Series) > 0 {
			return check.Check{}, false
		}
		return check.Skipped(id, "Not fetched yet. The request is queued and a later run fills this in."), true
	case surveys.Failed:
		return check.Skipped(id, note("Could not be reached this time.")), true
	case surveys.Uncovered:
		return check.Skipped(id, note("Never looked at this spot.")), true
	case surveys.Done:
		if len(f.Series) == 0 {
			return check.Skipped(id, note("No measurements at this spot.")), true
		}
		if !analysed {
			return check.Skipped(id, fmt.Sprintf("%s points fetched, not enough to test.", thousands(points(f.Series)))), true
		}
	}
	return check.Check{}, false
}

func points(ss []lc.Series) int {
	n := 0
	for _, s := range ss {
		n += len(s.Points)
	}
	return n
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func timingCheck(e *period.Ephemeris, t *period.Timing) check.Check {
	if t == nil {
		return check.Skipped(check.OwnTiming, "Your frames did not cover a predicted dip well enough to time it.")
	}
	off := math.Abs(t.Offset)
	when := "early"
	if t.Offset > 0 {
		when = "late"
	}
	ev := fmt.Sprintf("Your minimum came %.1f min %s, plus or minus %.1f min.", off, when, t.OffsetErr)
	if off < 0.05 {
		ev = fmt.Sprintf("Your minimum came right on time, plus or minus %.1f min.", t.OffsetErr)
	}
	if d, err := time.Parse(time.DateOnly, t.Night); err == nil {
		which := "deeper"
		if t.Dip != 1 {
			which = "shallower"
		}
		what := "frames"
		if t.Diff {
			what = "difference images"
		}
		ev = fmt.Sprintf("Your %s %s of %s cover the %s dip, %.2f mag deep. %s", t.Filter, what, dateWords(d), which, t.Depth, ev)
	}
	limit := math.Max(3*t.OffsetErr, 0.01*e.Period*24*60)
	if off > limit {
		return check.New(check.OwnTiming, check.Fail, ev+" That is more than the timing allows.")
	}
	return check.New(check.OwnTiming, check.Pass, ev)
}

func dateWords(t time.Time) string { return t.Format("Jan 2") }
