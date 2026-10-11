package sublength

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
)

const (
	ModeOff    = "off"
	ModeDryRun = "dry-run"
	ModeOn     = "on"
)

const (
	MinSkySubs      = 5
	SkyHistory      = 400 * 24 * time.Hour
	MinGradedSubs   = 100
	MaxOverheadGap  = 120.0
	SourceTarget    = "this target"
	SourceProject   = "the other panels and targets of its project"
	SourceSite      = "every target at this site"
	overheadBasisFm = "Mean of %d gaps of 0 to %g s between one sub's end and the next sub's start in Target Scheduler's records (longer gaps are slews, autofocus or waits, not per-sub overhead)"
)

type SiteFunc func(ctx context.Context) (sky.Site, error)

var ErrNoSite = errors.New("the observatory site is not known, so moon glare can't be told")

type Overhead struct {
	Seconds float64 `json:"seconds"`
	Gaps    int     `json:"gaps"`
	Basis   string  `json:"basis"`
}

type Row struct {
	TargetGUID string   `json:"target_guid"`
	Target     string   `json:"target"`
	Objects    []string `json:"objects"`
	Object     string   `json:"object"`
	Project    string   `json:"project"`
	Filter     string   `json:"filter"`
	Gain       int      `json:"gain"`
	Offset     float64  `json:"offset"`
	Bucket     Bucket   `json:"bucket"`
	Recommendation
}

type Proposal struct {
	Exposure float64  `json:"exposure"`
	Gain     float64  `json:"gain"`
	Offset   float64  `json:"offset"`
	SetTemp  float64  `json:"set_temp"`
	Rows     []string `json:"rows"`
	Basis    string   `json:"basis"`
}

type Result struct {
	ComputedAt  *time.Time           `json:"computed_at"`
	Mode        string               `json:"mode"`
	NewLengths  bool                 `json:"new_lengths"`
	Ladder      []float64            `json:"ladder"`
	DarkLengths map[string][]float64 `json:"dark_lengths"`
	Sensors     []Sensor             `json:"sensors"`
	Overhead    Overhead             `json:"overhead"`
	Rejection   Rejection            `json:"rejection"`
	Buckets     []BucketEdge         `json:"buckets"`
	Rows        []Row                `json:"rows"`
	Site        []Row                `json:"site"`
	Proposals   []Proposal           `json:"proposals"`
	Publish     Status               `json:"publish"`
	Reason      string               `json:"reason,omitempty"`
}

type BucketEdge struct {
	Bucket Bucket   `json:"bucket"`
	From   float64  `json:"glare_from"`
	To     *float64 `json:"glare_to"`
}

func bucketEdges() []BucketEdge {
	f := func(v float64) *float64 { return &v }
	return []BucketEdge{{BucketDark, 0, f(GlareLow)}, {BucketLow, GlareLow, f(GlareMid)}, {BucketMid, GlareMid, f(GlareBright)}, {BucketBright, GlareBright, nil}}
}

type Service struct {
	App        *gorm.DB
	Sched      *gorm.DB
	Site       SiteFunc
	Pedestal   float64
	Mode       string
	NewLengths bool
	Now        func() time.Time

	mu      sync.Mutex
	refresh sync.Mutex
	last    *Result
	status  Status
	dryRun  map[rowKey]TableRow
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) mode() string {
	switch s.Mode {
	case ModeOn, ModeDryRun:
		return s.Mode
	}
	return ModeOff
}

type planRow struct {
	GUID            string
	Name            string
	ProjectID       int
	Project         string
	RA              *float64
	Dec             *float64
	Filtername      string
	Gain            *float64
	Defaultexposure *float64
	Exposure        *float64
}

type planGroup struct {
	guid, target, project, filter string
	projectID                     int
	gain                          int
	ra, dec                       float64
	current                       float64
}

func (s *Service) plans(ctx context.Context) ([]planGroup, error) {
	for _, t := range []string{"exposureplan", "exposuretemplate", "target", "project"} {
		if has, err := tables.Has(ctx, s.Sched, t); err != nil || !has {
			return nil, err
		}
	}
	var rows []planRow
	if err := s.Sched.WithContext(ctx).Table("exposureplan e").
		Select(`coalesce(t.guid, '') AS guid, t.name AS name, t.projectid AS project_id, p.name AS project, t.ra AS ra, t.dec AS dec, ` +
			`et.filtername AS filtername, et.gain AS gain, et.defaultexposure AS defaultexposure, e.exposure AS exposure`).
		Joins(`JOIN exposuretemplate et ON et."Id" = e."exposureTemplateId"`).
		Joins(`JOIN target t ON t."Id" = e.targetid`).
		Joins(`JOIN project p ON p."Id" = t.projectid`).
		Where(`p.state IN (0, 1) AND coalesce(t.active, 1) = 1 AND coalesce(e.enabled, 1) = 1`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load exposure plans: %w", err)
	}
	type key struct {
		guid, filter string
		gain         int
	}
	groups := map[key]*planGroup{}
	for _, r := range rows {
		if r.GUID == "" || r.Gain == nil || *r.Gain < 0 {
			continue
		}
		cur := 0.0
		if r.Exposure != nil && *r.Exposure > 0 {
			cur = *r.Exposure
		} else if r.Defaultexposure != nil {
			cur = *r.Defaultexposure
		}
		k := key{r.GUID, r.Filtername, int(math.Round(*r.Gain))}
		g := groups[k]
		if g == nil {
			g = &planGroup{guid: r.GUID, target: r.Name, project: r.Project, projectID: r.ProjectID, filter: r.Filtername, gain: k.gain}
			if r.RA != nil && r.Dec != nil {
				g.ra, g.dec = *r.RA*15, *r.Dec
			} else {
				g.ra = math.NaN()
			}
			groups[k] = g
		}
		g.current = math.Max(g.current, cur)
	}
	out := make([]planGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].project != out[j].project {
			return out[i].project < out[j].project
		}
		if out[i].target != out[j].target {
			return out[i].target < out[j].target
		}
		if out[i].filter != out[j].filter {
			return out[i].filter < out[j].filter
		}
		return out[i].gain < out[j].gain
	})
	return out, nil
}

type tsImage struct {
	start    time.Time
	exposure float64
	median   float64
	gain     float64
	offset   float64
	detected float64
}

func parseStart(v string) (time.Time, bool) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, strings.TrimSuffix(v, "Z")+"Z"); err == nil {
			return t, true
		}
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func lengthRejectReasons() []string {
	return []string{"HFR", "Guiding RMS", "Star Count", "Eccentricity", "FWHM"}
}

func (s *Service) images(ctx context.Context) (map[string]tsImage, Overhead, []Point, error) {
	var rows []struct {
		GradingStatus int
		Rejectreason  *string
		Metadata      string
	}
	if has, err := tables.Has(ctx, s.Sched, "acquiredimage"); err != nil || !has {
		return nil, Overhead{}, nil, err
	}
	if err := s.Sched.WithContext(ctx).Table("acquiredimage").
		Select(`"gradingStatus" AS grading_status, rejectreason, metadata`).Scan(&rows).Error; err != nil {
		return nil, Overhead{}, nil, fmt.Errorf("load acquired images: %w", err)
	}
	out := make(map[string]tsImage, len(rows))
	var all []tsImage
	pts := map[float64]*Point{}
	for _, r := range rows {
		m, err := quality.ParseMetadata(r.Metadata)
		if err != nil || m.FileName == "" {
			continue
		}
		im := tsImage{exposure: math.Round(float64(m.ExposureDuration)), median: float64(m.ADUMedian), gain: float64(m.Gain),
			offset: float64(m.Offset), detected: float64(m.DetectedStars)}
		if t, ok := parseStart(m.ExposureStartTime); ok {
			im.start = t
			all = append(all, im)
		}
		out[m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:]] = im
		if r.GradingStatus == quality.GradingPending || !(im.exposure > 0) {
			continue
		}
		p := pts[im.exposure]
		if p == nil {
			p = &Point{Exposure: im.exposure}
			pts[im.exposure] = p
		}
		p.Graded++
		if r.GradingStatus == quality.GradingRejected && r.Rejectreason != nil {
			for _, part := range strings.Split(*r.Rejectreason, ",") {
				if slices.Contains(lengthRejectReasons(), strings.TrimSpace(part)) {
					p.Rejected++
					break
				}
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].start.Before(all[j].start) })
	var sum float64
	var n int
	for i := 1; i < len(all); i++ {
		gap := all[i].start.Sub(all[i-1].start).Seconds() - all[i-1].exposure
		if gap >= 0 && gap < MaxOverheadGap {
			sum += gap
			n++
		}
	}
	oh := Overhead{Gaps: n, Basis: fmt.Sprintf(overheadBasisFm, n, MaxOverheadGap)}
	if n > 0 {
		oh.Seconds = math.Round(sum/float64(n)*10) / 10
	}
	var points []Point
	for _, p := range pts {
		if p.Graded >= MinGradedSubs {
			points = append(points, *p)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Exposure < points[j].Exposure })
	return out, oh, points, nil
}

type lightRow struct {
	ID       int
	Key      string
	Object   string
	Filter   string
	Exposure *float64
	Gain     *float64
	Offset   *float64
	SetTemp  *float64
	Camera   string
	DateObs  *time.Time
	MountRA  *float64
	MountDec *float64
	SkyADU   *float64
	Status   *string
}

type sub struct {
	object, filter string
	gain           int
	rate           float64
	bucket         Bucket
	id             int
	exposure       float64
	detected       float64
}

func skippedStatus() []string {
	return []string{app.StackStatusOffTarget, app.StackStatusDead, app.StackStatusFailed, app.StackStatusDuplicate}
}

func (s *Service) lights(ctx context.Context, images map[string]tsImage, peds quality.Pedestals, site sky.Site,
	pos map[string][2]float64) ([]sub, map[int]lightRow, error) {
	var rows []lightRow
	if err := s.App.WithContext(ctx).Table("frames f").
		Select(`f.id, f.key, f.object, f.filter, f.exposure, f.gain, f."offset", f.set_temp, f.camera, f.date_obs, f.mount_ra, f.mount_dec, f.sky_adu, sf.status`).
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.type = ? AND f.index_error IS NULL AND f.date_obs IS NOT NULL AND f.date_obs >= ?", "LIGHT", s.now().Add(-SkyHistory)).
		Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("load lights: %w", err)
	}
	var out []sub
	byID := make(map[int]lightRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
		if (r.Status != nil && slices.Contains(skippedStatus(), *r.Status)) || strings.Contains(path.Base(r.Key), "_cal.") {
			continue
		}
		if r.Exposure == nil || !(*r.Exposure > 0) || r.Gain == nil {
			continue
		}
		sen, ok := SensorAt(r.Camera, *r.Gain)
		if !ok {
			continue
		}
		offset := 0.0
		if r.Offset != nil {
			offset = *r.Offset
		}
		ped := peds.At(*r.Gain, offset).ADU
		im, hasTS := images[path.Base(r.Key)]
		var adu float64
		switch {
		case hasTS && im.median > 0:
			adu = im.median
		case r.SkyADU != nil:
			adu = *r.SkyADU
		default:
			continue
		}
		above := adu - ped
		if !(above > 0) {
			continue
		}
		su := sub{object: r.Object, filter: frameheader.NormalizeFilter(r.Filter), gain: int(math.Round(*r.Gain)),
			rate: above * sen.EPerADU / *r.Exposure, id: r.ID, exposure: *r.Exposure, bucket: BucketDark}
		if hasTS {
			su.detected = im.detected
		}
		p, ok := pos[r.Object]
		if !ok && r.MountRA != nil && r.MountDec != nil {
			p, ok = [2]float64{*r.MountRA, *r.MountDec}, true
		}
		if !ok {
			continue
		}
		su.bucket = BucketOf(Glare(*r.DateObs, p[0], p[1], site.Latitude, site.Longitude))
		out = append(out, su)
	}
	return out, byID, nil
}

func (s *Service) photometry(ctx context.Context, ids []int) (map[int]measure.Photometry, error) {
	out := map[int]measure.Photometry{}
	for i := 0; i < len(ids); i += 1000 {
		var rows []struct {
			ID         int
			Photometry *string
		}
		if err := s.App.WithContext(ctx).Table("frames").Select("id, photometry").
			Where("id IN ? AND photometry IS NOT NULL", ids[i:min(i+1000, len(ids))]).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load photometry: %w", err)
		}
		for _, r := range rows {
			var ph measure.Photometry
			if r.Photometry != nil && json.Unmarshal([]byte(*r.Photometry), &ph) == nil && len(ph.Flux) == len(ph.Saturated) {
				out[r.ID] = ph
			}
		}
	}
	return out, nil
}

type darkKey struct{ gain, offset float64 }

func (s *Service) darkLengths(ctx context.Context) (map[darkKey][]float64, error) {
	var rows []struct {
		Exposure float64
		Gain     float64
		Offset   float64
		SetTemp  float64
		N        int
	}
	if err := s.App.WithContext(ctx).Table("frames").
		Select(`exposure, gain, "offset", set_temp, count(*) AS n`).
		Where(`type = ? AND index_error IS NULL AND light_leak IS NULL AND cal_check = ? AND exposure IS NOT NULL AND gain IS NOT NULL AND "offset" IS NOT NULL AND set_temp IS NOT NULL`,
			"DARK", "clean").
		Group(`exposure, gain, "offset", set_temp`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load dark sets: %w", err)
	}
	out := map[darkKey][]float64{}
	for _, r := range rows {
		k := darkKey{r.Gain, r.Offset}
		if r.N >= calmatch.FullDarkSet && !slices.Contains(out[k], r.Exposure) {
			out[k] = append(out[k], r.Exposure)
		}
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out, nil
}

func lastSetup(rows map[int]lightRow) map[int]lightRow {
	out := map[int]lightRow{}
	for _, r := range rows {
		if r.Gain == nil || r.DateObs == nil || r.Offset == nil || r.SetTemp == nil {
			continue
		}
		if _, ok := SensorAt(r.Camera, *r.Gain); !ok {
			continue
		}
		g := int(math.Round(*r.Gain))
		if cur, ok := out[g]; !ok || r.DateObs.After(*cur.DateObs) {
			out[g] = r
		}
	}
	return out
}

type world struct {
	plans       []planGroup
	subs        []sub
	phots       map[int]measure.Photometry
	ranks       []int
	setups      map[int]lightRow
	darks       map[darkKey][]float64
	guidObjects map[string][]string
	objProject  map[string]int
	overhead    Overhead
	rejection   Rejection
}

type targetPositions struct {
	byObject    map[string][2]float64
	guidObjects map[string][]string
	objProject  map[string]int
}

func (s *Service) positions(ctx context.Context) (targetPositions, error) {
	targets, err := tslink.Targets(ctx, s.Sched)
	if err != nil {
		return targetPositions{}, err
	}
	objTargets, err := tslink.ObjectTargets(ctx, s.App, targets)
	if err != nil {
		return targetPositions{}, err
	}
	tp := targetPositions{byObject: map[string][2]float64{}, guidObjects: tslink.TargetObjects(objTargets), objProject: map[string]int{}}
	byGUID := map[string]tslink.Target{}
	for _, t := range targets {
		byGUID[t.GUID] = t
	}
	for obj, guid := range objTargets {
		if t, ok := byGUID[guid]; ok {
			tp.byObject[obj] = [2]float64{t.RA, t.Dec}
			tp.objProject[obj] = t.ProjectID
		}
	}
	return tp, nil
}

func (s *Service) site(ctx context.Context) (sky.Site, error) {
	if s.Site == nil {
		return sky.Site{}, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return sky.Site{}, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	return site, nil
}

func (s *Service) load(ctx context.Context, res *Result, plans []planGroup) (*world, error) {
	tp, err := s.positions(ctx)
	if err != nil {
		return nil, err
	}
	images, oh, points, err := s.images(ctx)
	if err != nil {
		return nil, err
	}
	res.Overhead, res.Rejection = oh, FitRejection(points)
	peds, err := quality.LoadPedestals(ctx, s.App, s.Pedestal)
	if err != nil {
		return nil, err
	}
	site, err := s.site(ctx)
	if err != nil {
		return nil, err
	}
	subs, lightRows, err := s.lights(ctx, images, peds, site, tp.byObject)
	if err != nil {
		return nil, err
	}
	darks, err := s.darkLengths(ctx)
	if err != nil {
		return nil, err
	}
	planned := map[string]bool{}
	for _, p := range plans {
		for _, o := range tp.guidObjects[p.guid] {
			planned[o] = true
		}
	}
	var photoIDs []int
	for _, su := range subs {
		if planned[su.object] && su.detected > 0 {
			photoIDs = append(photoIDs, su.id)
		}
	}
	phots, err := s.photometry(ctx, photoIDs)
	if err != nil {
		return nil, err
	}
	return &world{plans: plans, subs: subs, phots: phots, ranks: measure.Ranks(), setups: lastSetup(lightRows), darks: darks,
		guidObjects: tp.guidObjects, objProject: tp.objProject, overhead: oh, rejection: res.Rejection}, nil
}

func (w *world) stars(p planGroup, filter string, objs []string) []Stars {
	var own, project []Stars
	for _, su := range w.subs {
		if su.filter != filter || su.gain != p.gain || su.detected <= 0 {
			continue
		}
		ph, ok := w.phots[su.id]
		if !ok || len(ph.Flux) != len(w.ranks) {
			continue
		}
		st := Stars{Exposure: su.exposure, Ranks: w.ranks, Flux: ph.Flux, Saturated: ph.Saturated, Detected: int(su.detected)}
		if slices.Contains(objs, su.object) {
			own = append(own, st)
		} else if w.objProject[su.object] == p.projectID {
			project = append(project, st)
		}
	}
	if len(own) == 0 {
		return project
	}
	return own
}

func (w *world) sky(p planGroup, filter string, objs []string, b Bucket) ([]float64, string) {
	levels := []struct {
		src   string
		match func(sub) bool
	}{
		{SourceTarget, func(su sub) bool { return slices.Contains(objs, su.object) }},
		{SourceProject, func(su sub) bool { return w.objProject[su.object] == p.projectID }},
		{SourceSite, func(sub) bool { return true }},
	}
	for _, l := range levels {
		var rates []float64
		for _, su := range w.subs {
			if su.filter == filter && su.gain == p.gain && su.bucket == b && l.match(su) {
				rates = append(rates, su.rate)
			}
		}
		if len(rates) >= MinSkySubs {
			return rates, l.src
		}
	}
	return nil, ""
}

func (w *world) setup(gain int) (camera string, offset, setTemp float64) {
	offset, setTemp = 50, math.NaN()
	if su, ok := w.setups[gain]; ok {
		return su.Camera, *su.Offset, *su.SetTemp
	}
	return "", offset, setTemp
}

func (w *world) planRows(p planGroup, proposals map[string]*Proposal) []Row {
	filter := frameheader.NormalizeFilter(p.filter)
	objs := w.guidObjects[p.guid]
	camera, offset, setTemp := w.setup(p.gain)
	sen, ok := SensorAt(camera, float64(p.gain))
	if !ok {
		return nil
	}
	stars := w.stars(p, filter, objs)
	obj := ""
	if len(objs) > 0 {
		obj = objs[0]
	}
	var out []Row
	for _, b := range Buckets() {
		rates, src := w.sky(p, filter, objs, b)
		if rates == nil {
			continue
		}
		rec, ok := Recommend(Input{Sensor: sen, Sky: median(rates), SkySubs: len(rates), SkySource: src, Current: p.current,
			Overhead: w.overhead.Seconds, Rejection: w.rejection, Stars: stars, DarkLengths: w.darks[darkKey{float64(p.gain), offset}]})
		if !ok {
			continue
		}
		out = append(out, Row{TargetGUID: p.guid, Target: p.target, Objects: objs, Object: obj, Project: p.project, Filter: p.filter, Gain: p.gain,
			Offset: offset, Bucket: b, Recommendation: rec})
		if rec.Ideal > 0 && rec.Ideal != rec.Exposure {
			k := fmt.Sprintf("%g|%d|%g", rec.Ideal, p.gain, offset)
			pr := proposals[k]
			if pr == nil {
				pr = &Proposal{Exposure: rec.Ideal, Gain: float64(p.gain), Offset: offset, SetTemp: setTemp}
				proposals[k] = pr
			}
			pr.Rows = append(pr.Rows, fmt.Sprintf("%s %s %s (+%.1f%% SNR per hour)", p.target, p.filter, b, rec.IdealGain*100))
		}
	}
	return out
}

func (s *Service) Compute(ctx context.Context) (*Result, error) {
	now := s.now().UTC()
	res := &Result{ComputedAt: &now, Mode: s.mode(), NewLengths: s.NewLengths, Ladder: Ladder(), Sensors: knownSensors(), Buckets: bucketEdges(),
		DarkLengths: map[string][]float64{}, Rows: []Row{}, Site: []Row{}, Proposals: []Proposal{}}
	if s.App == nil || s.Sched == nil {
		res.Reason = "no scheduler database"
		return res, nil
	}
	plans, err := s.plans(ctx)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		res.Reason = "no exposure plans on active targets with a fixed gain"
		return res, nil
	}
	w, err := s.load(ctx, res, plans)
	if err != nil {
		return nil, err
	}
	proposals := map[string]*Proposal{}
	for _, p := range plans {
		_, offset, _ := w.setup(p.gain)
		res.DarkLengths[strconv.Itoa(p.gain)] = w.darks[darkKey{float64(p.gain), offset}]
		res.Rows = append(res.Rows, w.planRows(p, proposals)...)
	}
	res.Site = siteRows(plans, w.subs, w.phots, w.ranks, w.setups, w.darks, w.overhead, w.rejection)
	for _, pr := range proposals {
		pr.Basis = fmt.Sprintf("%g s at gain %g has no full dark set; it would gain 5%% or more SNR per hour over the length in use for %d target/filter/glare rows", pr.Exposure, pr.Gain, len(pr.Rows))
		res.Proposals = append(res.Proposals, *pr)
	}
	sort.Slice(res.Proposals, func(i, j int) bool {
		if res.Proposals[i].Gain != res.Proposals[j].Gain {
			return res.Proposals[i].Gain < res.Proposals[j].Gain
		}
		return res.Proposals[i].Exposure < res.Proposals[j].Exposure
	})
	return res, nil
}

func (s *Service) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	r := *s.last
	return &r
}

func (s *Service) Proposed(context.Context) []Proposal {
	if s == nil || !s.NewLengths {
		return nil
	}
	r := s.Latest()
	if r == nil {
		return nil
	}
	return r.Proposals
}

func (r *Result) For(object, guid string) Result {
	out := *r
	if object == "" && guid == "" {
		return out
	}
	out.Rows = []Row{}
	for _, row := range r.Rows {
		if (guid != "" && row.TargetGUID == guid) || (object != "" && (row.Target == object || slices.Contains(row.Objects, object))) {
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

func siteRows(plans []planGroup, subs []sub, phots map[int]measure.Photometry, ranks []int, setups map[int]lightRow,
	darks map[darkKey][]float64, oh Overhead, rej Rejection) []Row {
	type key struct {
		filter string
		gain   int
	}
	current := map[key]float64{}
	names := map[key]string{}
	var keys []key
	for _, p := range plans {
		k := key{frameheader.NormalizeFilter(p.filter), p.gain}
		if _, ok := current[k]; !ok {
			keys = append(keys, k)
			names[k] = p.filter
		}
		current[k] = math.Max(current[k], p.current)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].filter != keys[j].filter {
			return keys[i].filter < keys[j].filter
		}
		return keys[i].gain < keys[j].gain
	})
	out := []Row{}
	for _, k := range keys {
		setup, ok := setups[k.gain]
		if !ok {
			continue
		}
		sen, ok := SensorAt(setup.Camera, float64(k.gain))
		if !ok {
			continue
		}
		var stars []Stars
		for _, su := range subs {
			if ph, ok := phots[su.id]; ok && su.filter == k.filter && su.gain == k.gain && su.detected > 0 && len(ph.Flux) == len(ranks) {
				stars = append(stars, Stars{Exposure: su.exposure, Ranks: ranks, Flux: ph.Flux, Saturated: ph.Saturated, Detected: int(su.detected)})
			}
		}
		for _, b := range Buckets() {
			var rates []float64
			for _, su := range subs {
				if su.filter == k.filter && su.gain == k.gain && su.bucket == b {
					rates = append(rates, su.rate)
				}
			}
			if len(rates) < MinSkySubs {
				continue
			}
			rec, ok := Recommend(Input{Sensor: sen, Sky: median(rates), SkySubs: len(rates), SkySource: SourceSite, Current: current[k],
				Overhead: oh.Seconds, Rejection: rej, Stars: stars, DarkLengths: darks[darkKey{float64(k.gain), *setup.Offset}]})
			if !ok {
				continue
			}
			out = append(out, Row{Filter: names[k], Gain: k.gain, Offset: *setup.Offset, Bucket: b, Recommendation: rec})
		}
	}
	return out
}

func (r *Result) SiteLengths(filter string, gain *int) []Row {
	var out []Row
	if r == nil {
		return out
	}
	f := rigsource.CanonicalFilter(filter)
	for _, row := range r.Site {
		if rigsource.CanonicalFilter(row.Filter) == f && (gain == nil || *gain == row.Gain) {
			out = append(out, row)
		}
	}
	return out
}
