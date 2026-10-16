package vetting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/memwatch"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
	"gorm.io/gorm"
)

// Bodies lists the known asteroids and comets within radius degrees at a
// time.
type Bodies func(ctx context.Context, ra, dec, radius float64, at time.Time) ([]discoveries.Body, error)

// Service vets discovery sources.
type Service struct {
	App     *gorm.DB
	Data    ownimage.Data
	Gaia    *starcat.Gaia
	Mirrors Mirrors
	Simbad  Simbad
	Bodies  Bodies
	Surveys *surveys.Fetcher
	Threads int
	// WorkDir holds the own image checks' stamps on disk; empty keeps
	// them in memory.
	WorkDir string
	Now     func() time.Time
	// Report, if set, hears each target's steps and progress.
	Report *activity.Reporter
	// Mem, if set, gives each target step's peak memory for the log.
	Mem *memwatch.Watch
}

// RunOptions narrow a run: Objects to those targets, IDs to those
// sources (due or not), Budget to a wall time after which no new target
// starts.
type RunOptions struct {
	Objects []string
	IDs     []int
	Budget  time.Duration
}

// Stats count what a run did.
type Stats struct {
	Sources int
	Done    int
	Failed  int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run vets every source due, one target at a time, while it holds the
// vetting lock; with another run going it returns ErrBusy. Sources a
// stopped run left behind go last.
func (s *Service) Run(ctx context.Context, o RunOptions) (Stats, error) {
	unlock, err := Lock(ctx, s.App)
	if err != nil {
		return Stats{}, err
	}
	defer unlock()
	start := s.now()
	stale, err := requeueStale(ctx, s.App, start)
	if err != nil {
		return Stats{}, err
	}
	if len(stale) > 0 {
		slog.Info("Vetting again the sources a stopped run left", "sources", len(stale))
	}
	dues, err := Pick(ctx, s.App, start, o.Objects, o.IDs)
	if err != nil {
		return Stats{}, err
	}
	byObject := map[string][]Due{}
	retry := map[string]bool{}
	var objects []string
	for _, d := range dues {
		obj := d.Summary.Source.Object
		if byObject[obj] == nil {
			objects = append(objects, obj)
		}
		byObject[obj] = append(byObject[obj], d)
		if stale[d.Summary.Source.ID] {
			retry[obj] = true
		}
	}
	sort.SliceStable(objects, func(i, j int) bool { return !retry[objects[i]] && retry[objects[j]] })
	var st Stats
	for i, obj := range objects {
		s.Report.Queue(len(objects)-i-1, "targets")
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		if o.Budget > 0 && s.now().Sub(start) > o.Budget {
			slog.Info("Vetting budget used up", "targets_left", len(objects))
			break
		}
		s.Mem.Step()
		done, failed := s.vetObject(ctx, obj, byObject[obj])
		st.Sources += len(byObject[obj])
		st.Done += done
		st.Failed += failed
	}
	return st, nil
}

// Spot is a position to vet by hand, with no stored source.
type Spot struct {
	Object string
	RA     float64
	Dec    float64
	Nights []time.Time
	Groups []string
	Sign   int
	Delta  float64
	// RunAt is when the comparison that flagged it ran; zero is now.
	RunAt time.Time
}

// VetSpot vets a position without storing anything, for checking the
// job against known answers.
func (s *Service) VetSpot(ctx context.Context, sp Spot) (*Report, error) {
	f, w, err := s.field(ctx, sp.Object)
	if err != nil {
		return nil, err
	}
	src := app.DiscoverySource{Object: sp.Object, Kind: "change", RA: sp.RA, Dec: sp.Dec}
	var members []app.DiscoveryCandidate
	x, y, _ := w.PixelOf(sp.RA, sp.Dec)
	for _, n := range sp.Nights {
		for _, g := range sp.Groups {
			d := sp.Delta
			members = append(members, app.DiscoveryCandidate{ID: -(len(members) + 1), Object: sp.Object, Night: n, Group: g, Kind: app.DiscoveryKindChange,
				Class: discoveries.ClassNewVariable, X: x, Y: y, RA: sp.RA, Dec: sp.Dec, Sign: sp.Sign, DeltaMag: &d})
		}
	}
	if len(members) > 0 {
		src.Night = members[0].Night
	}
	sum := summaryOf(src, members)
	runAt := sp.RunAt
	if runAt.IsZero() {
		runAt = s.now()
	}
	own, err := ownimage.Measure(ctx, s.Data, sp.Object, []ownimage.Spot{spotOf(sum, members, nil, runAt)}, ownimage.Options{Threads: s.threads(), WorkDir: s.WorkDir})
	var res *ownimage.Result
	if err == nil && len(own) == 1 {
		res = &own[0]
	} else if err != nil {
		slog.Warn("Own image checks failed", "error", err)
	}
	return s.vetSource(ctx, f, sum, nil, res)
}

func (s *Service) threads() int { return max(1, s.Threads) }

func (s *Service) field(ctx context.Context, object string) (surveys.Field, *discoveries.WCS, error) {
	w, wid, hei, err := s.Data.Frame(ctx, object)
	if err != nil {
		return surveys.Field{}, nil, fmt.Errorf("the target's frame: %w", err)
	}
	ra, dec := w.SkyOf(float64(wid)/2, float64(hei)/2)
	r := math.Hypot(float64(wid), float64(hei)) / 2 * w.PixelScale() / 3600
	return surveys.Field{Object: object, Slug: Slug(object), RA: ra, Dec: dec, Radius: r}, w, nil
}

// Slug is a target's name as a path segment.
func Slug(object string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(object) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func (s *Service) vetObject(ctx context.Context, object string, dues []Due) (int, int) {
	ids := make([]int, len(dues))
	for i, d := range dues {
		ids[i] = d.Summary.Source.ID
	}
	if err := markRunning(ctx, s.App, ids, s.now()); err != nil {
		slog.Error("Could not mark vetting as running", "object", object, "error", err)
		return 0, len(dues)
	}
	fail := func(err error) (int, int) {
		for _, d := range dues {
			if e := save(ctx, s.App, d.Summary.Source.ID, d.Hash, nil, err, s.now()); e != nil {
				slog.Error("Could not save the vetting", "source", d.Summary.Source.ID, "error", e)
			}
		}
		return 0, len(dues)
	}
	f, _, err := s.field(ctx, object)
	if err != nil {
		return fail(err)
	}
	s.Report.Set(object+": pulling survey data", 0, 0, "")
	if s.Surveys != nil {
		if err := s.Surveys.PrefetchProgress(ctx, f, func(done, total int) {
			s.Report.Set(object+": pulling survey data", int64(done), int64(total), "files")
		}); err != nil {
			slog.Warn("Could not pull the field's survey data; reading per star", "object", object, "error", err)
		}
	}
	var cands []app.DiscoveryCandidate
	if err := s.App.WithContext(ctx).Where("source_id IN ?", ids).Find(&cands).Error; err != nil {
		return fail(err)
	}
	byID := map[int][]app.DiscoveryCandidate{}
	runIDs := map[int]bool{}
	for _, c := range cands {
		byID[*c.SourceID] = append(byID[*c.SourceID], c)
		runIDs[c.RunID] = true
	}
	runs, err := s.runs(ctx, runIDs)
	if err != nil {
		return fail(err)
	}
	spots := make([]ownimage.Spot, 0, len(dues))
	for _, d := range dues {
		spots = append(spots, spotOf(d.Summary, byID[d.Summary.Source.ID], runs, s.now()))
	}
	pulled := memwatch.MiB(s.Mem.Step())
	slog.Info("Vetting a target", "object", object, "sources", len(dues))
	own, err := ownimage.Measure(ctx, s.Data, object, spots, ownimage.Options{Threads: s.threads(), WorkDir: s.WorkDir, Progress: func(done, total int) {
		s.Report.Set(object+": reading our own subs", int64(done), int64(total), "subs")
	}})
	if err != nil {
		slog.Warn("Own image checks failed", "object", object, "error", err)
	}
	ownBy := map[int]*ownimage.Result{}
	for i := range own {
		ownBy[own[i].SourceID] = &own[i]
	}
	measured := memwatch.MiB(s.Mem.Step())
	done, failed := 0, 0
	defer func() {
		if s.Mem != nil {
			slog.Info("Target vetted", "object", object, "done", done, "failed", failed,
				"pull_peak_mib", pulled, "own_images_peak_mib", measured, "sources_peak_mib", memwatch.MiB(s.Mem.Step()))
		}
	}()
	for i, d := range dues {
		s.Report.Set(object+": checking sources", int64(i), int64(len(dues)), "sources")
		if ctx.Err() != nil {
			return done, failed
		}
		id := d.Summary.Source.ID
		rep, err := s.vetSource(ctx, f, d.Summary, runs, ownBy[id])
		if rep != nil {
			rep.SourceID = id
		}
		if err != nil {
			failed++
			slog.Warn("Vetting failed", "source", id, "error", err)
		} else {
			done++
		}
		if e := save(ctx, s.App, id, d.Hash, rep, err, s.now()); e != nil {
			slog.Error("Could not save the vetting", "source", id, "error", e)
		}
	}
	return done, failed
}

func (s *Service) runs(ctx context.Context, ids map[int]bool) (map[int]app.DiscoveryRun, error) {
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	out := map[int]app.DiscoveryRun{}
	if len(list) == 0 {
		return out, nil
	}
	var runs []app.DiscoveryRun
	if err := s.App.WithContext(ctx).Where("id IN ?", list).Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, r := range runs {
		out[r.ID] = r
	}
	return out, nil
}

func spotOf(sum discoverysource.Summary, members []app.DiscoveryCandidate, runs map[int]app.DiscoveryRun, now time.Time) ownimage.Spot {
	sp := ownimage.Spot{SourceID: sum.Source.ID, Object: sum.Source.Object, Kind: sum.Source.Kind, RA: sum.Source.RA, Dec: sum.Source.Dec, GaiaG: sum.Source.GaiaG}
	for _, c := range members {
		n := ownimage.Night{Night: c.Night.UTC(), Group: c.Group, CandidateID: c.ID, RunID: c.RunID, X: c.X, Y: c.Y, Sign: c.Sign,
			DeltaMag: c.DeltaMag, SNR: c.SNR, PresentIn: c.PresentIn, SubsTested: c.SubsTested, RunAt: now}
		n.CutoutKey = c.CutoutKey
		if c.Light != "" {
			_ = json.Unmarshal([]byte(c.Light), &n.Light)
		}
		if r, ok := runs[c.RunID]; ok {
			if r.StartedAt != nil {
				n.RunAt = r.StartedAt.UTC()
			} else {
				n.RunAt = r.UpdatedAt.UTC()
			}
		}
		sp.Nights = append(sp.Nights, n)
	}
	sort.SliceStable(sp.Nights, func(i, j int) bool { return sp.Nights[i].Night.Before(sp.Nights[j].Night) })
	return sp
}

// star is the Gaia star at the spot and its close neighbours.
type star struct {
	found      bool
	target     surveys.Target
	bp, rp     float64
	neighbours []surveys.Neighbour
}

const (
	starArcsec      = 2.0
	neighbourArcsec = 10.0
)

func (s *Service) identify(ctx context.Context, ra, dec float64) (star, error) {
	out := star{target: surveys.Target{RA: ra, Dec: dec}}
	if s.Gaia == nil {
		return out, nil
	}
	stars, err := s.Gaia.Cone(ctx, ra, dec, neighbourArcsec/3600, nil)
	if err != nil {
		return out, err
	}
	sort.Slice(stars, func(i, j int) bool {
		return arcsec(ra, dec, stars[i].RA, stars[i].Dec) < arcsec(ra, dec, stars[j].RA, stars[j].Dec)
	})
	best := -1
	for i, g := range stars {
		gra, gdec := g.At(epochYear(s.now()))
		if arcsec(ra, dec, gra, gdec) <= starArcsec {
			if best < 0 || g.G < stars[best].G-1 {
				best = i
			}
		}
	}
	if best < 0 {
		return out, nil
	}
	g := stars[best]
	out.found = true
	out.bp, out.rp = float64(g.BP), float64(g.RP)
	out.target = surveys.Target{GaiaID: g.SourceID, RA: g.RA, Dec: g.Dec, PMRA: finite(g.PMRA), PMDec: finite(g.PMDec), G: float64(g.G)}
	for i, n := range stars {
		if i == best || math.IsNaN(float64(n.G)) || math.IsNaN(n.RA) || math.IsNaN(n.Dec) {
			continue
		}
		out.neighbours = append(out.neighbours, surveys.Neighbour{GaiaID: n.SourceID, RA: n.RA, Dec: n.Dec, G: float64(n.G), Sep: arcsec(g.RA, g.Dec, n.RA, n.Dec)})
	}
	out.target.Neighbours = out.neighbours
	return out, nil
}

func epochYear(t time.Time) float64 {
	return float64(t.Year()) + float64(t.YearDay())/365.25
}

// fetches are the survey answers for one star.
type fetches struct {
	ztf, atlas, tess, neowise surveys.Fetch
}

func (f fetches) all() []surveys.Fetch { return []surveys.Fetch{f.ztf, f.atlas, f.tess, f.neowise} }

func (s *Service) vetSource(ctx context.Context, f surveys.Field, sum discoverysource.Summary, runs map[int]app.DiscoveryRun, own *ownimage.Result) (*Report, error) {
	ra, dec := sum.Source.RA, sum.Source.Dec
	st, err := s.identify(ctx, ra, dec)
	if err != nil {
		slog.Warn("Could not read Gaia at the spot", "error", err)
	}
	known := catalogChecks(ctx, s.Mirrors, s.Simbad, ra, dec, st.target.GaiaID)
	rep := &Report{Version: Version, SourceID: sum.Source.ID, RanAt: s.now(), Star: starLine(known.Name, st)}
	flags := ownimage.Flags{}
	var curve *ownimage.Curve
	var ownChecks []check.Check
	if own != nil {
		flags, curve, ownChecks = own.Flags, own.Curve, own.Checks
	}
	artifact := flags.HotPixel || flags.RefContaminated || flags.Trail || flags.Edge
	var fx fetches
	if s.Surveys != nil && sum.Source.Kind != kindMover {
		fx.ztf = s.Surveys.ZTF(ctx, f, st.target)
		if !artifact && known.Listed == "" && worthMore(fx.ztf, curve, sum) {
			fx.neowise = s.Surveys.NEOWISE(ctx, f, st.target)
			fx.atlas = s.Surveys.ATLAS(ctx, f, st.target)
			fx.tess = s.Surveys.TESS(ctx, f, st.target)
		} else {
			why := "Not fetched: the checks above already explain the spot."
			fx.neowise = surveys.Fetch{Survey: lc.NEOWISE, Note: why}
			fx.atlas = surveys.Fetch{Survey: lc.ATLAS, Note: why}
			fx.tess = surveys.Fetch{Survey: lc.TESS, Note: why}
		}
	}
	var series []lc.Series
	for _, x := range fx.all() {
		series = append(series, x.Series...)
		if x.State == surveys.Pending {
			rep.Pending = append(rep.Pending, x.Survey)
		}
	}
	var ownSeries []lc.Series
	if curve != nil {
		ownSeries = curve.Series()
	}
	change := changeOf(sum)
	var pr *period.Result
	if sum.Source.Kind != kindMover && len(series) > 0 {
		opts := period.Options{ChangeMag: change.Mag}
		if !change.Fainter {
			opts.ChangeMag = -change.Mag
		}
		pr, err = period.Analyze(append(series, ownSeries...), opts)
		if err != nil && !errors.Is(err, period.ErrNoPeriod) {
			slog.Warn("Period search failed", "source", sum.Source.ID, "error", err)
		}
	}
	if pr != nil && !hasPeriod(pr.Ephemeris) {
		pr.Ephemeris = nil
	}
	var timing *period.Timing
	if pr != nil && pr.Ephemeris != nil && curve != nil {
		if t, ok := ownTiming(pr.Ephemeris, curve, sum); ok {
			timing = &t
		}
	}
	moving := s.movingCheck(ctx, sum, runs, fx.ztf)
	rep.Groups = groupsOf(sum, ownChecks, fx, pr, timing, known, moving)
	rep.Charts = chartsOf(sum, curve, fx, pr, change)
	if pr != nil {
		rep.Ephemeris = pr.Ephemeris
	}
	rep.Suggestion = suggest(sum, flags, curve, known, moving, fx, pr, timing, rep.Groups, change)
	return rep, nil
}

func starLine(name string, st star) string {
	if !st.found {
		if name != "" {
			return name
		}
		return "No catalogued star at the spot"
	}
	if name == "" {
		name = fmt.Sprintf("Gaia DR3 %d", st.target.GaiaID)
	}
	return fmt.Sprintf("%s · Gaia G %.2f", name, st.target.G)
}

// change is the reported change: Mag magnitudes, Fainter when the star
// dimmed.
type change struct {
	Mag     float64
	Fainter bool
	Night   time.Time
}

func changeOf(sum discoverysource.Summary) change {
	b := sum.Best
	c := change{Fainter: b.Sign < 0, Night: b.Night.UTC()}
	if b.DeltaMag != nil {
		c.Mag = math.Abs(*b.DeltaMag)
	}
	return c
}

func worthMore(ztf surveys.Fetch, curve *ownimage.Curve, sum discoverysource.Summary) bool {
	if ztf.State != surveys.Done || len(ztf.Series) == 0 {
		return true
	}
	if curve != nil && curve.ChangeSigma >= 5 {
		return true
	}
	return ztfVaries(ztf.Series) || sum.Source.Vetting != ""
}

func ztfVaries(ss []lc.Series) bool {
	for _, s := range ss {
		if len(s.Points) < 20 {
			continue
		}
		mags := make([]float64, 0, len(s.Points))
		for _, p := range s.Points {
			mags = append(mags, p.Mag)
		}
		sort.Float64s(mags)
		med := mags[len(mags)/2]
		devs := make([]float64, len(mags))
		for i, m := range mags {
			devs[i] = math.Abs(m - med)
		}
		sort.Float64s(devs)
		mad := 1.4826 * devs[len(devs)/2]
		hi := mags[len(mags)-1-len(mags)/200] - med
		lo := med - mags[len(mags)/200]
		if hi > max(0.08, 6*mad) || lo > max(0.08, 6*mad) {
			return true
		}
	}
	return false
}

func ownTiming(e *period.Ephemeris, curve *ownimage.Curve, sum discoverysource.Summary) (period.Timing, bool) {
	night := sum.Best.Night.UTC().Format(time.DateOnly)
	byFilter := map[string][]lc.Point{}
	for _, p := range curve.Points {
		if p.Night == night {
			byFilter[p.Filter] = append(byFilter[p.Filter], lc.Point{BJD: p.BJD, Mag: p.DMag, Err: p.Err})
		}
	}
	best, bestN := "", 0
	for f, pts := range byFilter {
		if len(pts) > bestN {
			best, bestN = f, len(pts)
		}
	}
	if bestN < 5 {
		return period.Timing{}, false
	}
	return period.TimeMinimum(e, byFilter[best], bandOf(e, best))
}

func bandOf(e *period.Ephemeris, filter string) string {
	want := map[string]string{"Luminance": ztfR, "Red": ztfR, "Green": ztfG, "Blue": ztfG, "H-a": ztfR, "S-II": ztfI, "O-III": ztfG}[filter]
	if want == "" {
		want = ztfR
	}
	for _, ec := range e.Eclipses {
		if _, ok := ec.Depth[want]; ok {
			return want
		}
	}
	return ""
}

func (s *Service) movingCheck(ctx context.Context, sum discoverysource.Summary, runs map[int]app.DiscoveryRun, ztf surveys.Fetch) check.Check {
	nights := map[string]time.Time{}
	for _, m := range sum.Members {
		at := m.Night.UTC().Add(28 * time.Hour)
		if r, ok := runs[m.RunID]; ok && r.MidTime != nil {
			at = r.MidTime.UTC()
		}
		nights[m.Night.UTC().Format(time.DateOnly)] = at
	}
	keys := make([]string, 0, len(nights))
	for k := range nights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if s.Bodies != nil {
		for _, k := range keys {
			bs, err := s.Bodies(ctx, sum.Source.RA, sum.Source.Dec, 60.0/3600, nights[k])
			if err != nil {
				return check.Skipped(check.Stays, "Not run: the known asteroid list could not be read.")
			}
			for _, b := range bs {
				sep := arcsec(sum.Source.RA, sum.Source.Dec, b.RA, b.Dec)
				if sep <= max(10, 3*b.ErrArc) {
					name := strings.TrimSpace(b.Name)
					if b.Number != "" {
						name = fmt.Sprintf("(%s) %s", b.Number, name)
					}
					return check.New(check.Stays, check.Fail, fmt.Sprintf("Known asteroid or comet %s passed %.0f arcsec away on %s.", name, sep, k))
				}
			}
		}
	}
	var parts []string
	if s.Bodies != nil {
		parts = append(parts, "No known asteroid or comet was near it")
	}
	if len(keys) > 1 {
		parts = append(parts, fmt.Sprintf("the change sat at the same spot on all %d nights", len(keys)))
	}
	if yrs := ztfYears(ztf); yrs >= 2 {
		parts = append(parts, fmt.Sprintf("the same star sits at this spot in %s years of ZTF images", numberWord(yrs)))
	}
	if len(parts) == 0 {
		return check.Skipped(check.Stays, "Not run: seen on one night only and no asteroid list was available.")
	}
	return check.New(check.Stays, check.Pass, sentence(joinWords(parts)))
}

func ztfYears(f surveys.Fetch) int {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range f.Series {
		for _, p := range s.Points {
			lo, hi = math.Min(lo, p.BJD), math.Max(hi, p.BJD)
		}
	}
	if hi <= lo {
		return 0
	}
	return int(math.Round((hi - lo) / 365.25))
}

func numberWord(n int) string {
	w := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if n >= 0 && n < len(w) {
		return w[n]
	}
	return fmt.Sprint(n)
}

// Prefetch pulls the survey data of each target's field once.
func (s *Service) Prefetch(ctx context.Context, objects []string) error {
	for _, obj := range objects {
		f, _, err := s.field(ctx, obj)
		if err != nil {
			return err
		}
		if err := s.Surveys.Prefetch(ctx, f); err != nil {
			return fmt.Errorf("%s: %w", obj, err)
		}
		slog.Info("Field survey data pulled", "object", obj)
	}
	return nil
}

func hasPeriod(e *period.Ephemeris) bool {
	return e != nil && e.Period > 0 && (e.Kind == period.KindEclipsing || e.Kind == period.KindSinusoidal)
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
