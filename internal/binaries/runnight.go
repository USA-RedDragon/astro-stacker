package binaries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// nightsBack is how far back the nights of runs are judged when the
	// live card did not store them.
	nightsBack = 3 * 24 * time.Hour
	// nightQuiet is how long after its last sub a night's imaging of a
	// run counts as ended when the run is no longer followed.
	nightQuiet = time.Hour
	// storedKeep is how long after its last sub a stored night stays on
	// the Now page once another night's imaging starts.
	storedKeep = 6 * time.Hour
)

// NightOf is the evening's civil date in loc of a sub taken at t: the date
// turns at local noon.
func NightOf(t time.Time, loc *time.Location) string {
	return t.In(loc).Add(-12 * time.Hour).Format(time.DateOnly)
}

// endRun stores the nights of a run the live card stops following.
func (s *Service) endRun(ctx context.Context, run *liveRun) {
	if run == nil || run.tracker == nil || !run.dirty {
		return
	}
	s.LiveTask.Start("Storing the night of "+run.view.Name, 1, "runs")
	err := s.storeRun(ctx, run)
	s.LiveTask.Step(1)
	s.LiveTask.Finish(err)
	if err != nil {
		slog.Warn("Storing a run's night", "project", run.guid, "error", err)
		return
	}
	run.dirty = false
}

// storeRun judges and stores every night of the run's measured subs.
func (s *Service) storeRun(ctx context.Context, run *liveRun) error {
	subs, err := s.LiveData.Subs(ctx, run.object)
	if err != nil {
		return fmt.Errorf("subs of %s: %w", run.object, err)
	}
	raw, kept, _ := run.tracker.Points()
	groups := s.byNight(subs, run.from, run.to)
	var errs []error
	for night, got := range groups {
		if err := s.saveNight(ctx, run, night, raw, kept, len(got)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// byNight groups by night the subs whose middle falls between from and to.
func (s *Service) byNight(subs []ownimage.Sub, from, to time.Time) map[string][]ownimage.Sub {
	out := map[string][]ownimage.Sub{}
	for _, sub := range subs {
		if mid := subMid(sub); !mid.Before(from) && !mid.After(to) {
			k := NightOf(mid, s.loc())
			out[k] = append(out[k], sub)
		}
	}
	return out
}

// saveNight stores the run's points of one night with their verdict.
func (s *Service) saveNight(ctx context.Context, run *liveRun, night string, raw []ownimage.LivePoint, kept, subs int) error {
	var mine []ownimage.LivePoint
	for _, p := range raw {
		if NightOf(p.Time, s.loc()) == night {
			mine = append(mine, p)
		}
	}
	row := s.nightRow(ctx, run, night, mine, kept, subs)
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_guid"}, {Name: "night"}}, UpdateAll: true}).
		Create(&row).Error
}

// nightRow judges one night's points of a run as the live card does.
func (s *Service) nightRow(ctx context.Context, run *liveRun, night string, raw []ownimage.LivePoint, kept, subs int) app.RunNight {
	now := s.now()
	v := liveView(run, raw, kept, now)
	pts, _, _ := mainFilter(raw)
	row := app.RunNight{ProjectGUID: run.guid, Night: night, SourceID: v.SourceID, ProjectName: v.ProjectName, Object: run.object,
		Filter: v.Filter, Subs: subs, Measured: len(pts), Comps: kept, JudgedAt: now, UpdatedAt: now}
	if len(pts) > 0 {
		row.First, row.Last = pts[0].Time, pts[len(pts)-1].Time
		row.Zero = pts[0].Mag - v.Points[0].Mag
		row.AgreeLow, row.AgreeHigh = agreeRange(pts)
	}
	for _, p := range v.Points {
		row.Points = append(row.Points, app.RunPoint(p))
	}
	switch {
	case run.dip != nil:
		row.Kind, row.DepthLow, row.DepthHigh = app.RunNightDip, run.dip.DepthLow, run.dip.DepthHigh
		res := judgeResult(app.DipResult{}, pts, run.goal, s.loc())
		row.State, row.Note, row.Smallest, row.Depth = res.State, res.Note, res.Smallest, res.Depth
		if d := v.Dip; d != nil {
			row.Run, row.DipStart = d.Run, d.DipStart
			if d.OverMin != nil {
				row.RunMinutes = *d.OverMin
			}
			for _, b := range d.Bins {
				row.Bins = append(row.Bins, app.RunBin(b))
			}
		}
	default:
		row.Kind, row.State, row.Note = app.RunNightEclipse, v.State, v.Why
		if run.model != nil {
			row.DepthLow, row.DepthHigh = run.model.depth, run.model.depth
		}
	}
	if v.Scatter != nil {
		row.Scatter = *v.Scatter
	}
	s.darkOf(ctx, run, &row)
	v.Stored, v.Night, v.LowFrom = true, night, row.LowFrom
	if card, err := json.Marshal(v); err == nil {
		row.Card = string(card)
	}
	return row
}

// agreeRange is the fewest and most nearby stars that agreed in one sub.
func agreeRange(pts []LivePoint) (int, int) {
	lo, hi := math.MaxInt, 0
	for _, p := range pts {
		if p.Present > 0 {
			lo, hi = min(lo, p.Agree), max(hi, p.Agree)
		}
	}
	if hi == 0 {
		return 0, 0
	}
	return lo, hi
}

// darkOf finds the night's dark and high enough time around the subs and
// when the star dropped below the planner's height limit while subs were
// still taken.
func (s *Service) darkOf(ctx context.Context, run *liveRun, row *app.RunNight) {
	if s.Site == nil || row.Measured == 0 {
		return
	}
	site, err := s.Site(ctx)
	if err != nil {
		return
	}
	ra, dec := run.spot.RA, run.spot.Dec
	var best *TimeSpan
	overlap := func(sp TimeSpan) time.Duration {
		return max(0, minTime(sp.End, row.Last).Sub(maxTime(sp.Start, row.First)))
	}
	for _, sp := range DarkSpans(site, ra, dec, row.First.Add(-6*time.Hour), row.Last.Add(time.Hour)) {
		if best == nil || overlap(sp) > overlap(*best) {
			b := sp
			best = &b
		}
	}
	if best != nil && overlap(*best) > 0 {
		from, to := maxTime(best.Start, run.from), minTime(best.End, run.to)
		row.DarkStart, row.DarkEnd = &from, &to
	}
	prev := site.Altitude(ra, dec, row.First)
	for t := row.First.Truncate(dipStep).Add(dipStep); t.Before(row.Last); t = t.Add(dipStep) {
		alt := site.Altitude(ra, dec, t)
		if alt < MinAltitude && prev >= MinAltitude {
			lo := t
			row.LowFrom = &lo
			break
		}
		prev = alt
	}
}

// storedCard is the live card of the latest stored night, unless another
// night's imaging has started since.
func (s *Service) storedCard(ctx context.Context) (LiveCurve, bool) {
	var rows []app.RunNight
	if err := s.DB.WithContext(ctx).Where("card <> ''").Order("last DESC").Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return LiveCurve{}, false
	}
	row := rows[0]
	if s.Current != nil {
		if cur := s.Current(); cur != nil && cur.ProjectGUID != "" && s.now().Sub(row.Last) > storedKeep {
			return LiveCurve{}, false
		}
	}
	var v LiveCurve
	if json.Unmarshal([]byte(row.Card), &v) != nil {
		return LiveCurve{}, false
	}
	v.Stored, v.Night = true, row.Night
	return v, true
}

// following says the live card follows project guid now.
func (s *Service) following(guid string) bool {
	s.liveMu.Lock()
	run := s.live
	s.liveMu.Unlock()
	if run == nil || run.guid != guid || s.Current == nil {
		return false
	}
	cur := s.Current()
	return cur != nil && cur.ProjectGUID == guid
}

// nightJob is a run with nights whose imaging ended and is not stored.
type nightJob struct {
	guid   string
	name   string
	watch  *app.DipWatch
	req    *app.EclipseRequest
	nights map[string]int
}

// span is the run's star and imaging span.
func (j nightJob) span() (int, time.Time, time.Time) {
	if j.watch != nil {
		return j.watch.SourceID, j.watch.Start, j.watch.End
	}
	return j.req.SourceID, j.req.Start, j.req.End
}

// judgeNights measures and stores the nights of the last nightsBack of
// dip windows and eclipse requests whose imaging ended and the live card
// did not store, as after a restart, shown in the processing indicator.
func (s *Service) judgeNights(ctx context.Context) error {
	if s.LiveData == nil {
		return nil
	}
	jobs, err := s.nightJobs(ctx)
	if len(jobs) == 0 {
		return err
	}
	errs := make([]error, 0, len(jobs)+1)
	errs = append(errs, err)
	total := 0
	for _, j := range jobs {
		total += len(j.nights)
	}
	s.DipTask.Start("Judging last night's dip and eclipse runs", int64(total), "nights")
	var runErrs []error
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		s.DipTask.SetCurrent(j.name)
		if err := s.judgeJob(ctx, j); err != nil {
			runErrs = append(runErrs, fmt.Errorf("project %s: %w", j.guid, err))
		}
		s.DipTask.Step(int64(len(j.nights)))
	}
	s.DipTask.Finish(errors.Join(runErrs...))
	return errors.Join(append(errs, runErrs...)...)
}

func (s *Service) nightJobs(ctx context.Context) ([]nightJob, error) {
	db := s.DB.WithContext(ctx)
	now := s.now()
	since := now.Add(-nightsBack)
	var watches []app.DipWatch
	if err := db.Where("\"end\" > ? AND start < ?", since, now).Find(&watches).Error; err != nil {
		return nil, err
	}
	var reqs []app.EclipseRequest
	if err := db.Where("kind <> ? AND project_guid <> '' AND \"end\" > ? AND start < ?", KindDip, since, now).Find(&reqs).Error; err != nil {
		return nil, err
	}
	var cands []nightJob
	for i := range watches {
		cands = append(cands, nightJob{guid: watches[i].ProjectGUID, watch: &watches[i]})
	}
	for i := range reqs {
		cands = append(cands, nightJob{guid: reqs[i].ProjectGUID, req: &reqs[i]})
	}
	var out []nightJob
	var errs []error
	for _, j := range cands {
		if s.following(j.guid) {
			continue
		}
		src, from, to := j.span()
		var source app.DiscoverySource
		if err := db.First(&source, src).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		subs, err := s.LiveData.Subs(ctx, source.Object)
		if err != nil {
			errs = append(errs, fmt.Errorf("subs of %s: %w", source.Object, err))
			continue
		}
		var stored []app.RunNight
		if err := db.Select("night", "subs").Where("project_guid = ?", j.guid).Find(&stored).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		j.name = fmt.Sprintf("%d in %s", src, source.Object)
		j.nights = map[string]int{}
		for night, got := range s.byNight(subs, from, to) {
			last := slices.MaxFunc(got, func(a, b ownimage.Sub) int { return subMid(a).Compare(subMid(b)) })
			if now.Sub(subMid(last)) < nightQuiet || slices.ContainsFunc(stored, func(r app.RunNight) bool { return r.Night == night && r.Subs == len(got) }) {
				continue
			}
			j.nights[night] = len(got)
		}
		if len(j.nights) > 0 {
			out = append(out, j)
		}
	}
	return out, errors.Join(errs...)
}

// judgeJob measures a run's unstored nights from scratch and stores them.
func (s *Service) judgeJob(ctx context.Context, j nightJob) error {
	cur := &LiveTarget{ProjectGUID: j.guid}
	var run *liveRun
	var err error
	if j.watch != nil {
		run, err = s.dipRun(ctx, cur, *j.watch)
	} else {
		run, err = s.eclipseRun(ctx, cur, *j.req)
	}
	if err != nil {
		return err
	}
	if run.tracker == nil {
		return nil
	}
	subs, err := s.LiveData.Subs(ctx, run.object)
	if err != nil {
		return fmt.Errorf("subs of %s: %w", run.object, err)
	}
	groups := s.byNight(subs, run.from, run.to)
	var errs []error
	for night, n := range j.nights {
		for _, sub := range groups[night] {
			if err := run.tracker.Add(ctx, s.LiveData, sub); err != nil {
				return fmt.Errorf("sub %d: %w", sub.FrameID, err)
			}
		}
		raw, kept, _ := run.tracker.Points()
		if err := s.saveNight(ctx, run, night, raw, kept, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// storedPoints are a window's subs as the stored nights measured them, in
// raw magnitudes, when the nights hold every sub in one filter.
func (s *Service) storedPoints(ctx context.Context, sourceID int, w dipSpan, subs int) ([]LivePoint, bool) {
	var rows []app.RunNight
	if err := s.DB.WithContext(ctx).Where("source_id = ? AND kind = ? AND first <= ? AND last >= ?", sourceID, app.RunNightDip, w.end, w.start).
		Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, false
	}
	var out []LivePoint
	n := 0
	for _, r := range rows {
		if r.Filter != rows[0].Filter {
			return nil, false
		}
		for _, p := range r.Points {
			if w.holds(p.Time) {
				p.Mag += r.Zero
				out = append(out, LivePoint(p))
			}
		}
		n += r.Subs
	}
	if n != subs || len(out) == 0 {
		return nil, false
	}
	slices.SortFunc(out, func(a, b LivePoint) int { return a.Time.Compare(b.Time) })
	return out, true
}
