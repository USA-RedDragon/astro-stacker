package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

const (
	appNamespace   = "astro-processing"
	minioNamespace = "minio"
	stepStarting   = "starting"
	calDarks       = "darks"
)

// catalogMirror is one catalog-mirror CronJob.
type catalogMirror struct {
	use, cronJob, label string
}

func catalogMirrorJobs() []catalogMirror {
	return []catalogMirror{
		{"vsx", "vsx-mirror", "VSX catalogue mirror"},
		{"gaia-vari", "gaia-vari-mirror", "Gaia variable stars mirror"},
		{"atlas-var", "atlas-var-mirror", "ATLAS variable stars mirror"},
		{"ztf-var", "ztf-var-mirror", "ZTF variable stars mirror"},
		{"asassn-var", "asassn-var-mirror", "ASAS-SN variable stars mirror"},
	}
}

func jobWatches() []activity.Watch {
	mirrors := catalogMirrorJobs()
	w := make([]activity.Watch, 0, 4+len(mirrors))
	w = append(w, []activity.Watch{
		{Kind: activity.KindDiscoveries, Namespace: appNamespace, Label: "app.kubernetes.io/name=discoveries-backfill"},
		{Kind: activity.KindVetting, Namespace: appNamespace, CronJob: "discoveries-vetting-vet"},
		{Kind: activity.KindTESS, Namespace: appNamespace, CronJob: "discoveries-vetting-tess"},
		{Kind: activity.KindSmallBodies, Namespace: appNamespace, CronJob: "astro-small-bodies-update"},
	}...)
	for _, m := range mirrors {
		w = append(w, activity.Watch{Kind: activity.KindMirrorPrefix + m.use, Namespace: minioNamespace, CronJob: m.cronJob})
	}
	return w
}

// activitySources reads the work done outside this process, and the
// discoveries runner and observatory calibration inside it.
type activitySources struct {
	DB          *gorm.DB
	Sched       *gorm.DB
	S3          *minio.Client
	Processed   string
	Survey      string
	Jobs        *activity.Jobs
	Discoveries func() discoveries.Live
	Monitor     *observatory.Monitor
	// BackfillDays is the server runner's backfill window.
	BackfillDays int
	SmallBodies  string
	HTTP         *http.Client
	Now          func() time.Time

	pickMu   sync.Mutex
	pickAt   time.Time
	pickKey  string
	pickObjs []string
}

func (a *activitySources) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *activitySources) register(reg *activity.Registry) {
	if a.DB != nil {
		reg.AddSource(activity.Source{Name: "discoveries", Every: 15 * time.Second, Read: a.discoveries})
		reg.AddSource(activity.Source{Name: "vetting", Every: 20 * time.Second, Read: a.vetting})
	}
	if a.S3 != nil {
		reg.AddSource(activity.Source{Name: "tess", Every: 30 * time.Second, Read: a.tess})
		if a.Survey != "" {
			reg.AddSource(activity.Source{Name: "mirrors", Every: 30 * time.Second, Read: a.mirrors})
		}
	}
	reg.AddSource(activity.Source{Name: "small-bodies", Every: 30 * time.Second, Read: a.smallBodies})
	if a.Monitor != nil {
		reg.AddSource(activity.Source{Name: "calibration", Every: 5 * time.Second, Read: a.calibration})
	}
}

func nightText(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

func (a *activitySources) discoveries(ctx context.Context) ([]activity.Item, error) {
	now := a.now()
	days := a.BackfillDays
	if days <= 0 {
		days = 30
	}
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days)
	var stored map[string]bodies.Target
	if a.Sched != nil {
		var err error
		if stored, err = bodies.TargetsByName(ctx, a.Sched); err != nil {
			slog.Warn("Could not load the moving-body targets for the discoveries progress", "error", err)
		}
	}
	p, err := discoveries.ReadProgress(ctx, a.DB, now, since, stored)
	if err != nil {
		return nil, err
	}
	it := activity.Item{Kind: activity.KindDiscoveries, Label: "Night comparisons", State: activity.StateIdle,
		Done: p.Settled, Total: p.Nights, Unit: "nights", QueueUnit: "nights"}
	if p.Old > 0 {
		it.Label = "Discoveries re-check"
	}
	running := p.Running
	var live discoveries.Live
	if a.Discoveries != nil {
		live = a.Discoveries()
	}
	if len(running) > 0 {
		it.State = activity.StateRunning
		r := running[0]
		it.Current = fmt.Sprintf("%s, night of %s", r.Object, nightText(r.Night))
		if r.Stage != "" {
			it.Current += ": " + r.Stage
		}
		if p.RunningNights > 1 {
			it.Current += fmt.Sprintf(" (%d nights at once)", p.RunningNights)
		}
	} else if live.State == "running" {
		it.State = activity.StateRunning
		night := live.Night
		if t, err := time.Parse(time.DateOnly, live.Night); err == nil {
			night = nightText(t)
		}
		it.Current = live.Object + ", night of " + night
		if live.Stage != "" {
			it.Current += ": " + live.Stage
		}
	}
	info := a.Jobs.Info(activity.KindDiscoveries)
	unsettled := p.Nights - p.Settled - p.RunningNights
	left, settling := p.Due, p.Settling
	if info.Running {
		left, settling = unsettled, 0
	}
	if left > 0 {
		it.Queued = int(left)
		if it.State == activity.StateIdle {
			it.State = activity.StateQueued
		}
	}
	var warn []string
	if p.GaveUp > 0 {
		warn = append(warn, fmt.Sprintf("%d %s failed every attempt", p.GaveUp, plural(int(p.GaveUp), "night", "nights")))
	}
	if settling > 0 {
		warn = append(warn, fmt.Sprintf("%d %s for stacking to settle", settling, plural(int(settling), "night waits", "nights wait")))
	}
	noSubs := p.NoSubs
	if info.Running {
		noSubs = 0
	}
	if noSubs > 0 {
		warn = append(warn, fmt.Sprintf("%d %s no accepted subs to compare", noSubs, plural(int(noSubs), "night has", "nights have")))
	}
	if rest := unsettled - left - settling - noSubs; rest > 0 && !info.Running {
		warn = append(warn, fmt.Sprintf("%d older %s for an archive re-check", rest, plural(int(rest), "night waits", "nights wait")))
	}
	it.Warning = strings.Join(warn, "; ")
	info.Apply(&it)
	return []activity.Item{it}, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// vetGroup is one target a vetting run marked running together.
type vetGroup struct {
	Object  string
	Started time.Time
	Total   int64
	Done    int64
	Updated time.Time
}

func (a *activitySources) vetting(ctx context.Context) ([]activity.Item, error) {
	now := a.now()
	info := a.Jobs.Info(activity.KindVetting)
	queuedObjs, err := a.vetQueue(ctx, now)
	if err != nil {
		return nil, err
	}
	var reports []app.JobProgress
	if err := a.DB.WithContext(ctx).Where("kind = ? AND updated_at > ?", activity.KindVetting, now.Add(-activity.StaleAfter)).
		Order("worker").Find(&reports).Error; err != nil {
		return nil, err
	}
	var groups []vetGroup
	if err := a.DB.WithContext(ctx).Raw(`SELECT s.object, v.started_at AS started, count(*) AS total,
		count(*) FILTER (WHERE v.state <> ?) AS done, max(v.updated_at) AS updated
		FROM discovery_vets v JOIN discovery_sources s ON s.id = v.source_id
		WHERE v.started_at > ? GROUP BY s.object, v.started_at HAVING bool_or(v.state = ?) ORDER BY v.started_at`,
		vetting.StateRunning, now.Add(-12*time.Hour), vetting.StateRunning).Scan(&groups).Error; err != nil {
		return nil, err
	}
	running := map[string]bool{}
	var items []activity.Item
	base := activity.Item{Kind: activity.KindVetting, Label: "Vetting", State: activity.StateIdle, QueueUnit: "targets"}
	for _, r := range reports {
		rep, err := activity.ParseReport([]byte(r.Report))
		if err != nil {
			continue
		}
		it := base
		it.ID = activity.KindVetting + ":" + r.Worker
		if !it.FromReport(rep, now) {
			continue
		}
		obj, _, _ := strings.Cut(rep.Current, ": ")
		running[obj] = true
		items = append(items, it)
	}
	if info.Running {
		live := liveGroups(groups, info.Workers-len(items))
		for _, g := range live {
			if running[g.Object] {
				continue
			}
			running[g.Object] = true
			it := base
			it.ID = activity.KindVetting + ":" + g.Object
			it.State = activity.StateRunning
			it.Current = g.Object + ": checking sources"
			if g.Done == 0 {
				it.Current = g.Object + ": getting ready"
			}
			it.Done, it.Total, it.Unit = g.Done, g.Total, "sources"
			st := g.Started
			it.Started = &st
			items = append(items, it)
		}
	}
	waiting := 0
	for _, o := range queuedObjs {
		if !running[o] {
			waiting++
		}
	}
	if len(items) == 0 {
		it := base
		if waiting > 0 {
			it.State = activity.StateQueued
			it.Current = strings.Join(firstN(queuedObjs, 3), ", ")
		}
		items = append(items, it)
	}
	for i := range items {
		items[i].Queued, items[i].QueueUnit = waiting, "targets"
		info.Apply(&items[i])
		if items[i].State == activity.StateRunning && items[i].Current == "" {
			items[i].Current = stepStarting
		}
	}
	if info.Running && len(items) == 1 && items[0].Total == 0 && items[0].Current == stepStarting {
		items[0].Current = "choosing the sources to check"
	}
	return items, nil
}

// liveGroups is the n groups updated last: a worker that died leaves its
// target's sources marked running, and each live worker holds one target,
// whose rows change as it saves them.
func liveGroups(groups []vetGroup, n int) []vetGroup {
	if n <= 0 {
		return nil
	}
	out := append([]vetGroup(nil), groups...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) > n {
		out = out[:n]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string(nil), s[:n]...), fmt.Sprintf("%d more", len(s)-n))
}

// vetQueue is the targets with sources due for vetting, read at most every
// two minutes.
func (a *activitySources) vetQueue(ctx context.Context, now time.Time) ([]string, error) {
	a.pickMu.Lock()
	defer a.pickMu.Unlock()
	if !a.pickAt.IsZero() && now.Sub(a.pickAt) < 2*time.Minute {
		return a.pickObjs, nil
	}
	var key string
	if err := a.DB.WithContext(ctx).Raw(`SELECT concat_ws('|',
		(SELECT count(*) FROM discovery_sources), (SELECT max(updated_at) FROM discovery_sources),
		(SELECT count(*) FROM discovery_vets), (SELECT max(updated_at) FROM discovery_vets),
		(SELECT count(*) FROM discovery_candidates))`).Row().Scan(&key); err != nil {
		return nil, err
	}
	if key == a.pickKey && now.Sub(a.pickAt) < 30*time.Minute {
		a.pickAt = now
		return a.pickObjs, nil
	}
	a.pickAt = now
	dues, err := vetting.Pick(ctx, a.DB, now, nil, nil)
	if err != nil {
		return nil, err
	}
	a.pickKey = key
	seen := map[string]bool{}
	var objs []string
	for _, d := range dues {
		o := d.Summary.Source.Object
		if !seen[o] {
			seen[o] = true
			objs = append(objs, o)
		}
	}
	a.pickAt, a.pickObjs = now, objs
	return objs, nil
}

func (a *activitySources) getObject(ctx context.Context, bucket, key string) ([]byte, error) {
	o, err := a.S3.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(io.LimitReader(o, 4<<20))
}

func isNoSuchKey(err error) bool {
	return err != nil && minio.ToErrorResponse(err).Code == "NoSuchKey"
}

type tessQueued struct {
	key    string
	when   time.Time
	req    surveys.TESSRequest
	gaiaID string
}

func (a *activitySources) tess(ctx context.Context) ([]activity.Item, error) {
	info := a.Jobs.Info(activity.KindTESS)
	var queue []tessQueued
	for o := range a.S3.ListObjects(ctx, a.Processed, minio.ListObjectsOptions{Prefix: surveys.TESSRequestPrefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		if !strings.HasSuffix(o.Key, ".json") {
			continue
		}
		b, err := a.getObject(ctx, a.Processed, o.Key)
		if err != nil {
			continue
		}
		var raw struct {
			GaiaID surveys.GaiaID `json:"gaia_id"`
			Field  surveys.Field  `json:"field"`
			When   time.Time      `json:"requested_at"`
		}
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		when := raw.When
		if when.IsZero() {
			when = o.LastModified
		}
		q := tessQueued{key: o.Key, when: when, gaiaID: strconv.FormatInt(int64(raw.GaiaID), 10)}
		q.req.Field = raw.Field
		queue = append(queue, q)
	}
	sort.Slice(queue, func(i, j int) bool {
		if !queue[i].when.Equal(queue[j].when) {
			return queue[i].when.Before(queue[j].when)
		}
		return queue[i].key < queue[j].key
	})
	it := activity.Item{Kind: activity.KindTESS, Label: "TESS light curves", State: activity.StateIdle, Unit: "sectors", QueueUnit: "stars"}
	if len(queue) > 0 {
		q := queue[0]
		name := q.req.Field.Object
		if name == "" {
			name = q.req.Field.Slug
		}
		it.State = activity.StateQueued
		it.Current = name + ", Gaia " + q.gaiaID
		it.Queued = len(queue)
		if info.Running {
			it.Queued--
		}
		key := surveys.DefaultPrefix + surveys.FieldSlug(q.req.Field) + "/tess/" + q.gaiaID + "/status.json"
		if b, err := a.getObject(ctx, a.Processed, key); err == nil {
			var st surveys.TESSStatus
			if json.Unmarshal(b, &st) == nil {
				finished := map[int]bool{}
				for _, s := range st.SectorsDone {
					finished[s] = true
				}
				for k := range st.SectorsFailed {
					if n, err := strconv.Atoi(k); err == nil {
						finished[n] = true
					}
				}
				var next []int
				for _, s := range st.SectorsAvailable {
					if !finished[s] {
						next = append(next, s)
					}
				}
				it.Total = int64(len(st.SectorsAvailable))
				it.Done = it.Total - int64(len(next))
				if len(next) > 0 && info.Running {
					sort.Ints(next)
					it.Current = fmt.Sprintf("%s, sector %d", name, next[0])
				}
			}
		} else if !isNoSuchKey(err) {
			slog.Debug("Could not read a TESS status", "key", key, "error", err)
		}
		if info.Running && it.Total == 0 {
			it.Current = name + ": looking up its sectors"
		}
	}
	info.Apply(&it)
	if info.Running && it.Current == "" {
		it.Current = "reading the queue"
	}
	return []activity.Item{it}, nil
}

func (a *activitySources) mirrors(ctx context.Context) ([]activity.Item, error) {
	now := a.now()
	mirrors := catalogMirrorJobs()
	out := make([]activity.Item, 0, len(mirrors))
	for _, m := range mirrors {
		kind := activity.KindMirrorPrefix + m.use
		info := a.Jobs.Info(kind)
		it := activity.Item{Kind: kind, Label: m.label, State: activity.StateIdle}
		b, err := a.getObject(ctx, a.Survey, mirrorProgressKey(m.use))
		if err == nil {
			if rep, err := activity.ParseReport(b); err == nil {
				if !it.FromReport(rep, now) && rep.State == activity.ReportFailed && !info.Running {
					t := rep.Updated
					it.LastFailed, it.LastError = &t, rep.Error
				}
			}
		}
		info.Apply(&it)
		if info.Running && it.Current == "" {
			it.Current = stepStarting
		}
		out = append(out, it)
	}
	return out, nil
}

func (a *activitySources) smallBodies(ctx context.Context) ([]activity.Item, error) {
	now := a.now()
	info := a.Jobs.Info(activity.KindSmallBodies)
	it := activity.Item{Kind: activity.KindSmallBodies, Label: "Asteroid and comet catalogue update", State: activity.StateIdle}
	if a.SmallBodies != "" && info.Running {
		rep, err := a.smallBodiesReport(ctx)
		if err == nil {
			it.FromReport(rep, now)
		}
	}
	info.Apply(&it)
	if info.Running && it.Current == "" {
		it.Current = stepStarting
	}
	return []activity.Item{it}, nil
}

func (a *activitySources) smallBodiesReport(ctx context.Context) (activity.Report, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(a.SmallBodies, "/")+"/progress", nil)
	if err != nil {
		return activity.Report{}, err
	}
	cl := a.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := cl.Do(req)
	if err != nil {
		return activity.Report{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return activity.Report{}, fmt.Errorf("small bodies progress: %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return activity.Report{}, err
	}
	return activity.ParseReport(b)
}

var errNoCalibration = errors.New("no calibration")

func (a *activitySources) calibration(context.Context) ([]activity.Item, error) {
	it, err := calibrationItem(a.Monitor.View(), a.now())
	if errors.Is(err, errNoCalibration) {
		return []activity.Item{{Kind: activity.KindCalibration, Label: "Observatory calibration", State: activity.StateIdle}}, nil
	}
	return []activity.Item{it}, err
}

// calibrationItem is the observatory's calibration run as an item.
func calibrationItem(v observatory.View, now time.Time) (activity.Item, error) {
	it := activity.Item{Kind: activity.KindCalibration, Label: "Observatory calibration", State: activity.StateIdle}
	if v.Status == nil || v.Calibration == nil || v.Reachable != observatory.Online {
		return it, errNoCalibration
	}
	c := v.Calibration
	if c.UpdatedAt != nil && now.Sub(*c.UpdatedAt) > 30*time.Minute {
		return it, errNoCalibration
	}
	it.Started = c.Since
	it.QueueUnit = "sets"
	for _, q := range c.Queue {
		if q.Status == "skipped" || q.Status == "done" {
			continue
		}
		if q.Remaining == nil || *q.Remaining > 0 {
			it.Queued++
		}
	}
	switch c.Kind {
	case "cooling":
		it.State = activity.StateRunning
		it.Current = "cooling the camera"
		if cl := c.Cooling; cl != nil && cl.From != nil && cl.Target != nil && c.Camera != nil && c.Camera.Temperature != nil {
			span := *cl.From - *cl.Target
			if span > 0 {
				done := math.Min(math.Max(*cl.From-*c.Camera.Temperature, 0), span)
				it.Done, it.Total, it.Unit = int64(math.Round(done*10)), int64(math.Round(span*10)), "tenths"
				it.Detail = fmt.Sprintf("now %s, target %s", celsius(*c.Camera.Temperature), celsius(*cl.Target))
			}
		}
	case calDarks, "bias", "flats":
		it.State = activity.StateRunning
		it.Current = calibrationSetText(c.Kind, c.Set)
		if s := c.Set; s != nil && s.FramesTotal != nil && *s.FramesTotal > 0 {
			done := 0
			if s.FramesDone != nil {
				done = *s.FramesDone
			}
			it.Done, it.Total, it.Unit = int64(done), int64(*s.FramesTotal), "frames"
		}
	default:
		return it, errNoCalibration
	}
	return it, nil
}

func celsius(v float64) string {
	s := strconv.FormatFloat(math.Abs(v), 'f', 1, 64)
	if v < 0 {
		return "−" + s + " °C"
	}
	return s + " °C"
}

func calibrationSetText(kind string, s *observatory.CalibrationSet) string {
	name := map[string]string{calDarks: "Darks", "bias": "Bias frames", "flats": "Flats"}[kind]
	if s == nil {
		return name
	}
	var parts []string
	if s.Filter != "" {
		parts = append(parts, s.Filter)
	}
	if s.Exposure != nil && kind == calDarks {
		parts = append(parts, strconv.FormatFloat(*s.Exposure, 'f', -1, 64)+" s")
	}
	if s.TargetName != "" {
		parts = append(parts, "for "+s.TargetName)
	}
	if len(parts) == 0 {
		return name
	}
	return name + ", " + strings.Join(parts, ", ")
}
