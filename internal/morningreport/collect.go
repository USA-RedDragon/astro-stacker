package morningreport

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/autofocus"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Previews reads stack previews from the processed bucket.
type Previews interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// Collector gathers a night's report from the stacker's database, the
// scheduler database, the metrics store and the log store.
type Collector struct {
	App      *gorm.DB
	Sched    *gorm.DB
	Metrics  Metrics
	Logs     Logs
	Previews Previews
	Schedule Schedule
	// Site is the observatory's, for when the night was dark.
	Site func(ctx context.Context) (sky.Site, error)
}

// NINAQuery selects every NINA log line, to tell a quiet night from logs
// that never arrived.
const NINAQuery = `{job="observatory", app="nina"}`

const lightType = "LIGHT"

// rejectReason is why a light was left out, in plain words, or "" for a
// light that is kept or not decided yet.
func rejectReason(status string) string {
	switch status {
	case app.StackStatusLowScore:
		return "low quality score"
	case app.StackStatusUnmeasured:
		return "no stars measured"
	case app.StackStatusMoon:
		return "too close to the moon"
	case app.StackStatusRejected:
		return "rejected by the scheduler's grader"
	case app.StackStatusOffTarget:
		return "pointed off target"
	case app.StackStatusDuplicate:
		return "duplicate file"
	case app.StackStatusDead:
		return "could not be processed"
	}
	return ""
}

// kept is whether a light's status puts it in a master.
func kept(status string) bool {
	switch status {
	case app.StackStatusAdded, app.StackStatusNight, app.StackStatusRecalibrate:
		return true
	}
	return false
}

type light struct {
	Object      string
	Filter      string
	Exposure    *float64
	DateObs     time.Time
	SetTemp     *float64
	Status      *string
	ProcessedAt *time.Time
}

// Collect builds the report for a night. A source that fails leaves its part
// "not measured" and is logged; only a failure to read the lights at all is
// an error.
func (c Collector) Collect(ctx context.Context, night time.Time) (Report, error) {
	start, end := c.Schedule.Window(night)
	r := Report{Night: night, Start: start, End: end}

	lights, err := c.lights(ctx, start, end)
	if err != nil {
		return r, err
	}
	r.Subs = summarize(lights)
	spans := exposures(lights)
	r.Camera.Setpoints = setpoints(lights)
	c.dark(ctx, &r)
	imaged := Overlap(spans, r.DarkSpans)
	r.Imaged = &imaged

	c.safety(ctx, &r)
	nina := c.ninaLogs(ctx, r.Start, r.End)
	c.flap(ctx, &r, nina)
	c.dew(ctx, &r, spans)
	c.autofocus(ctx, &r, nina)
	c.guiding(ctx, &r, spans)
	c.guiderErrors(ctx, &r, nina)
	c.cooler(ctx, &r, spans)
	c.discoveries(ctx, &r)
	if r.Subs.Count > 0 {
		r.Images = c.images(ctx, r.Subs, night)
	}
	if c.App != nil {
		c.dust(ctx, &r)
	}
	return r, nil
}

// discoveries counts the night's new candidates by target, from the night
// comparisons finished so far.
func (c Collector) discoveries(ctx context.Context, r *Report) {
	var rows []struct {
		Object string
		N      int
	}
	if err := c.App.WithContext(ctx).Model(&app.DiscoveryRun{}).Select("object, SUM(new_candidates) AS n").
		Where("night >= ? AND night < ? AND state = ? AND new_candidates > 0", r.Night, r.Night.AddDate(0, 0, 1), app.DiscoveryDone).
		Group("object").Order("n DESC, object").Scan(&rows).Error; err != nil {
		slog.Warn("Morning report: could not count discoveries", "error", err)
		return
	}
	for _, row := range rows {
		r.Discoveries = append(r.Discoveries, Count{Label: row.Object, N: row.N})
	}
	if len(rows) == 0 {
		return
	}
	var novae []struct {
		Object string
		N      int
	}
	if err := c.App.WithContext(ctx).Model(&app.DiscoveryCandidate{}).Select("object, COUNT(*) AS n").
		Where("night >= ? AND night < ? AND class = ? AND vetting NOT IN ?", r.Night, r.Night.AddDate(0, 0, 1), discoveries.ClassNova, []string{app.VetArtifact, app.VetKnown}).
		Group("object").Scan(&novae).Error; err != nil {
		slog.Warn("Morning report: could not count possible novae", "error", err)
		return
	}
	for _, row := range novae {
		if r.Novae == nil {
			r.Novae = map[string]int{}
		}
		r.Novae[row.Object] = row.N
	}
}

// dark finds the window's dark spans, or the whole window when the site is
// unknown.
func (c Collector) dark(ctx context.Context, r *Report) {
	r.DarkSpans = []Span{{From: r.Start, To: r.End}}
	if c.Site == nil {
		r.DarkMissing = missing("dark", fmt.Errorf("no observatory site"))
		return
	}
	site, err := c.Site(ctx)
	if err != nil {
		r.DarkMissing = missing("dark", err)
		return
	}
	r.DarkSpans = DarkSpans(site, r.Start, r.End)
	d := Total(r.DarkSpans)
	r.Dark = &d
}

// ninaLogs is nil when NINA logged in [start, end), else why its log can't
// be read: a query that failed, or no NINA lines at all.
func (c Collector) ninaLogs(ctx context.Context, start, end time.Time) error {
	last, err := c.Logs.Last(ctx, NINAQuery, start, end)
	if err != nil {
		return err
	}
	if last == nil {
		return fmt.Errorf("no NINA log lines in the window")
	}
	return nil
}

func missing(part string, err error) string {
	slog.Warn("Morning report: part not measured", "part", part, "error", err)
	return err.Error()
}

func (c Collector) lights(ctx context.Context, start, end time.Time) ([]light, error) {
	var rows []light
	err := c.App.WithContext(ctx).Table("frames f").
		Select("f.object, f.filter, f.exposure, f.date_obs, f.set_temp, sf.status, sf.processed_at").
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.type = ? AND f.index_error IS NULL AND f.date_obs >= ? AND f.date_obs < ?", lightType, start.UTC(), end.UTC()).
		Order("f.date_obs").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read the night's lights: %w", err)
	}
	return rows, nil
}

func summarize(lights []light) *Subs {
	s := &Subs{}
	byTarget := map[string]map[string]*FilterSubs{}
	reasons := map[string]int{}
	var exposure float64
	for _, l := range lights {
		s.Count++
		if l.Exposure != nil {
			exposure += *l.Exposure
		}
		if byTarget[l.Object] == nil {
			byTarget[l.Object] = map[string]*FilterSubs{}
		}
		fs := byTarget[l.Object][l.Filter]
		if fs == nil {
			fs = &FilterSubs{Filter: l.Filter}
			byTarget[l.Object][l.Filter] = fs
		}
		status := ""
		if l.Status != nil {
			status = *l.Status
		}
		switch {
		case kept(status):
			s.Kept++
			fs.Kept++
			if status == app.StackStatusNight {
				fs.ByNight++
			}
			if l.ProcessedAt != nil && l.ProcessedAt.After(fs.LastProcessed) {
				fs.LastProcessed = *l.ProcessedAt
			}
		case rejectReason(status) != "":
			s.Rejected++
			fs.Rejected++
			reasons[rejectReason(status)]++
		default:
			s.Pending++
			fs.Pending++
		}
	}
	s.Exposure = time.Duration(exposure * float64(time.Second))
	for obj, filters := range byTarget {
		t := TargetSubs{Object: obj}
		for _, fs := range filters {
			t.Filters = append(t.Filters, *fs)
		}
		sort.Slice(t.Filters, func(i, j int) bool {
			a, b := filterRank(t.Filters[i].Filter), filterRank(t.Filters[j].Filter)
			if a != b {
				return a < b
			}
			return t.Filters[i].Filter < t.Filters[j].Filter
		})
		s.Targets = append(s.Targets, t)
	}
	total := func(t TargetSubs) (n int) {
		for _, f := range t.Filters {
			n += f.Kept + f.Rejected + f.Pending
		}
		return n
	}
	sort.Slice(s.Targets, func(i, j int) bool {
		a, b := total(s.Targets[i]), total(s.Targets[j])
		if a != b {
			return a > b
		}
		return s.Targets[i].Object < s.Targets[j].Object
	})
	for label, n := range reasons {
		s.Reasons = append(s.Reasons, Count{Label: label, N: n})
	}
	sort.Slice(s.Reasons, func(i, j int) bool {
		if s.Reasons[i].N != s.Reasons[j].N {
			return s.Reasons[i].N > s.Reasons[j].N
		}
		return s.Reasons[i].Label < s.Reasons[j].Label
	})
	return s
}

func exposures(lights []light) []Span {
	out := make([]Span, 0, len(lights))
	for _, l := range lights {
		if l.Exposure == nil || *l.Exposure <= 0 {
			continue
		}
		out = append(out, Span{From: l.DateObs, To: l.DateObs.Add(time.Duration(*l.Exposure * float64(time.Second)))})
	}
	return out
}

func setpoints(lights []light) []Setpoint {
	var out []Setpoint
	for _, l := range lights {
		if l.SetTemp == nil || math.IsNaN(*l.SetTemp) {
			continue
		}
		v := math.Round(*l.SetTemp*10) / 10
		i := slices.IndexFunc(out, func(s Setpoint) bool { return s.C == v })
		if i < 0 {
			out = append(out, Setpoint{C: v})
			i = len(out) - 1
		}
		out[i].Subs++
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Subs > out[j].Subs })
	return out
}

func (c Collector) safety(ctx context.Context, r *Report) {
	safe, err := c.Metrics.Range(ctx, `max(observatory_safetymonitor_is_safe)`, r.Start, r.End)
	if err != nil {
		r.SafeMissing = missing("safety", err)
		return
	}
	conn, err := c.Metrics.Range(ctx, `max(observatory_safetymonitor_connected)`, r.Start, r.End)
	if err != nil {
		r.SafeMissing = missing("safety", err)
		return
	}
	if len(safe) == 0 || len(conn) == 0 {
		r.SafeMissing = missing("safety", fmt.Errorf("no safety monitor readings in the window"))
		return
	}
	s, cn := SafeTime(safe, conn, r.DarkSpans)
	r.Safe, r.SafetyConnected = &s, &cn
}

func (c Collector) flap(ctx context.Context, r *Report, nina error) {
	if nina != nil {
		r.FlapMissing = missing("flap", nina)
		r.UnsafeMissing = r.FlapMissing
		return
	}
	last, err := c.Logs.Last(ctx, CoverQuery, r.Start.Add(-7*24*time.Hour), r.Start)
	if err != nil {
		r.FlapMissing = missing("flap", err)
		r.UnsafeMissing = r.FlapMissing
		return
	}
	lines, err := c.Logs.Range(ctx, FlapQuery, r.Start.Add(-shutdownGrace), r.End)
	if err != nil {
		r.FlapMissing = missing("flap", err)
		r.UnsafeMissing = r.FlapMissing
		return
	}
	seed := SeedOpen(last)
	f := FlapTimeline(seed, lines, r.Start, r.End)
	if f.Moves == 0 && !seed && r.Subs != nil && r.Subs.Count > 0 {
		r.FlapMissing = missing("flap", fmt.Errorf("lights were taken but the log has no flap steps"))
		r.UnsafeMissing = r.FlapMissing
		return
	}
	open := Overlap(f.OpenSpans, r.DarkSpans)
	r.FlapOpen = &open
	r.Unsafe, r.Restarts = f.Unsafe, MergeRestarts(f.Restarts)
}

func (c Collector) dew(ctx context.Context, r *Report, spans []Span) {
	margin, err := c.Metrics.Range(ctx, `max(observatory_weather_temperature_celsius) - max(observatory_weather_dew_point_celsius)`, r.Start, r.End)
	if err != nil {
		r.DewMissing = missing("dew", err)
		return
	}
	tracking, err := c.Metrics.Range(ctx, `max(observatory_mount_tracking_enabled)`, r.Start, r.End)
	if err != nil {
		r.DewMissing = missing("dew", err)
		return
	}
	heater, err := c.Metrics.Range(ctx, `max({__name__=~"observatory_switch_dew_heater_[abc]_percent"})`, r.Start, r.End)
	if err != nil {
		r.DewMissing = missing("dew", err)
		return
	}
	r.Dew = DewStats(margin, tracking, heater)
	if r.Dew == nil {
		r.DewMissing = missing("dew", fmt.Errorf("no temperature and dew point readings in the window"))
		return
	}
	r.Dew.ImagingLow, r.Dew.ImagingHigh = Range(During(margin, spans))
}

// autofocus reads the runs from NINA's log, which has every run; the
// scheduler database's records, which started later, are the fallback.
func (c Collector) autofocus(ctx context.Context, r *Report, nina error) {
	err := nina
	if err == nil {
		var lines []Line
		if lines, err = c.Logs.Range(ctx, AutofocusQuery, r.Start, r.End); err == nil {
			r.Autofocus = AutofocusRuns(lines)
			return
		}
	}
	missing("autofocus log", err)
	runs, err := autofocus.LoadBetween(ctx, c.Sched, r.Start, r.End, autofocus.MaxLimit)
	if err != nil {
		r.AutofocusMissing = missing("autofocus", err)
		return
	}
	if !runs.Available {
		r.AutofocusMissing = missing("autofocus", fmt.Errorf("no %s table in the scheduler database", autofocus.Table))
		return
	}
	var out []AutofocusRun
	for _, a := range runs.Runs {
		if !a.FinishedAt.Before(r.End) {
			continue
		}
		out = append(out, AutofocusRun{Started: a.StartedAt, Finished: a.FinishedAt, Success: a.Success, Error: a.Error, Target: a.TargetName})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Finished.Before(out[j].Finished) })
	markBackToBack(out)
	r.Autofocus = out
}

func (c Collector) guiding(ctx context.Context, r *Report, spans []Span) {
	if len(spans) == 0 {
		r.GuidingMissing = "no lights"
		return
	}
	pts, err := c.Metrics.Range(ctx, `max(observatory_guider_rms_error_total_arcseconds)`, r.Start, r.End)
	if err != nil {
		r.GuidingMissing = missing("guiding", err)
		return
	}
	r.Guiding = GuidingStats(During(pts, spans))
	if r.Guiding == nil {
		r.GuidingMissing = missing("guiding", fmt.Errorf("no guiding readings while lights exposed"))
	}
}

func (c Collector) guiderErrors(ctx context.Context, r *Report, nina error) {
	if nina != nil {
		r.GuiderErrorsMissing = missing("guider errors", nina)
		return
	}
	lines, err := c.Logs.Range(ctx, GuiderQuery, r.Start, r.End)
	if err != nil {
		r.GuiderErrorsMissing = missing("guider errors", err)
		return
	}
	r.GuiderErrors = GuiderBursts(lines)
}

func (c Collector) cooler(ctx context.Context, r *Report, spans []Span) {
	if len(spans) == 0 {
		return
	}
	pts, err := c.Metrics.Range(ctx, `max(observatory_camera_cooler_power_percent)`, r.Start, r.End)
	if err != nil {
		missing("cooler", err)
		return
	}
	r.Camera.CoolerMin, r.Camera.CoolerMax = Range(During(pts, spans))
}
