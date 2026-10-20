package vetting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
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
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
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
	// PS1 lists Pan-STARRS sources and Alerts ZTF alert objects, for new
	// light where no star was.
	PS1    PS1
	Alerts Alerts
	// Reddening gives a Gaia star's GSP-Phot reddening, which bounds how
	// hot it can be for the dark companion test.
	Reddening GaiaReddening
	// ReadOnly leaves the field caches as they are: a preview pulls no
	// survey fields.
	ReadOnly bool
	Threads  int
	// WorkDir holds the own image checks' stamps on disk; empty keeps
	// them in memory.
	WorkDir string
	Now     func() time.Time
	// Report, if set, hears each target's steps and progress.
	Report *activity.Reporter
	// Mem, if set, gives each target step's peak memory for the log.
	Mem *memwatch.Watch
	// StackCap is the most moving objects a run stacks along their
	// motion; 0 is stackPerRun.
	StackCap int
	// Build names this build, stamped on each report it writes; a run
	// stops when another build is announced while it goes.
	Build    string
	line     *atlasLine
	stacked  int
	started  time.Time
	replaced bool
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
// vetting lock; with another run going it returns ErrBusy. Sources asked
// for by hand go first, in the order asked, and sources a stopped run
// left behind go last. Past the budget no new target starts, and a
// target under way starts no new source after its first; the rest wait
// for the next run. It first announces its build, and stops, leaving the
// rest for the newer build, when a newer one is announced while it goes.
func (s *Service) Run(ctx context.Context, o RunOptions) (Stats, error) {
	if err := Announce(ctx, s.App, s.Build, s.now()); err != nil {
		return Stats{}, err
	}
	unlock, err := Lock(ctx, s.App)
	if err != nil {
		return Stats{}, err
	}
	defer unlock()
	s.stacked = 0
	s.replaced = false
	start := s.now()
	s.started = start
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
	var stop time.Time
	if o.Budget > 0 {
		stop = start.Add(o.Budget)
	}
	bs := batches(dues, stale)
	line := newATLASLine(dues)
	s.line = line
	if s.Surveys != nil {
		s.Surveys.ATLASAhead = line.ahead
	}
	var st Stats
	for i, b := range bs {
		s.Report.Queue(len(bs)-i-1, "targets")
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		if !stop.IsZero() && s.now().After(stop) {
			slog.Info("Vetting budget used up", "targets_left", len(bs)-i)
			break
		}
		if s.replaced {
			slog.Warn("A newer vetting build was announced; leaving the rest to it", "targets_left", len(bs)-i)
			break
		}
		s.Mem.Step()
		done, failed := s.vetObject(ctx, b.object, b.dues, stop)
		st.Sources += done + failed
		st.Done += done
		st.Failed += failed
		if s.Surveys != nil {
			if err := s.Surveys.ATLASServe(ctx); err != nil {
				slog.Warn("Could not give ATLAS room to the stars in line", "error", err)
			}
		}
	}
	return st, nil
}

// batch is the sources of one target a run vets together.
type batch struct {
	object string
	dues   []Due
	rank   int
	asked  time.Time
	place  surveys.Priority
}

const (
	rankAsked = iota
	rankDue
	rankStale
)

// batches groups the sources due by target, one batch per target so its
// subs are read once, and those a stopped run left in a batch of their
// own. Targets with a source asked for by hand go first, earliest asked
// first, and inside a batch those sources go first; then the other
// targets, those whose best placed source stands first in line for ATLAS
// first and, inside a batch, sources by their place in that line; then
// what stopped runs left.
func batches(dues []Due, stale map[int]bool) []batch {
	type key struct {
		object string
		stale  bool
	}
	at := map[key]int{}
	var out []batch
	for _, d := range dues {
		k := key{d.Summary.Source.Object, stale[d.Summary.Source.ID]}
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, batch{object: k.object, rank: rankDue})
			if k.stale {
				out[i].rank = rankStale
			}
		}
		if len(out[i].dues) == 0 || atlasPlace(d) < out[i].place {
			out[i].place = atlasPlace(d)
		}
		out[i].dues = append(out[i].dues, d)
		if !k.stale && d.AskedAt != nil {
			out[i].rank = rankAsked
			if out[i].asked.IsZero() || d.AskedAt.Before(out[i].asked) {
				out[i].asked = *d.AskedAt
			}
		}
	}
	for _, b := range out {
		sort.SliceStable(b.dues, func(i, j int) bool {
			a, c := b.dues[i].AskedAt, b.dues[j].AskedAt
			if a != nil || c != nil {
				return a != nil && (c == nil || a.Before(*c))
			}
			return atlasPlace(b.dues[i]) < atlasPlace(b.dues[j])
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		if out[i].rank == rankAsked {
			return out[i].asked.Before(out[j].asked)
		}
		return out[i].place < out[j].place
	})
	return out
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
	own, err := ownimage.Measure(ctx, s.Data, sp.Object, []ownimage.Spot{s.withStar(ctx, spotOf(sum, members, nil, runAt), members)}, ownimage.Options{Threads: s.threads(), WorkDir: s.WorkDir})
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

func (s *Service) vetObject(ctx context.Context, object string, dues []Due, stop time.Time) (int, int) {
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
			s.line.visit(d)
			if e := save(ctx, s.App, d.Summary.Source.ID, d.Hash, s.stampOf(d), nil, err, s.now()); e != nil {
				slog.Error("Could not save the vetting", "source", d.Summary.Source.ID, "error", e)
			}
		}
		return 0, len(dues)
	}
	w, err := s.prepare(ctx, object, dues)
	if err != nil {
		return fail(err)
	}
	s.stackMovers(ctx, w, dues, false)
	done, failed := 0, 0
	defer func() {
		if s.Mem != nil {
			checked := s.Mem.Step()
			slog.Info("Target vetted", "object", object, "done", done, "failed", failed,
				"pull_rss_mib", memwatch.MiB(w.pulled.RSS), "pull_live_mib", memwatch.MiB(w.pulled.Live),
				"own_images_rss_mib", memwatch.MiB(w.measured.RSS), "own_images_live_mib", memwatch.MiB(w.measured.Live),
				"sources_rss_mib", memwatch.MiB(checked.RSS), "sources_live_mib", memwatch.MiB(checked.Live))
		}
	}()
	for i, d := range dues {
		s.Report.Set(object+": checking sources", int64(i), int64(len(dues)), "sources")
		if ctx.Err() != nil {
			return done, failed
		}
		if i > 0 && !stop.IsZero() && s.now().After(stop) {
			left := make([]int, 0, len(dues)-i)
			for _, r := range dues[i:] {
				left = append(left, r.Summary.Source.ID)
			}
			if err := release(ctx, s.App, left, s.now()); err != nil {
				slog.Error("Could not hand back the sources left", "object", object, "error", err)
			}
			slog.Info("Vetting budget used up part way through a target", "object", object, "sources_left", len(left))
			return done, failed
		}
		id := d.Summary.Source.ID
		s.line.visit(d)
		rep, err := s.checkSource(surveys.WithPriority(ctx, atlasPlace(d)), w, d.Summary)
		if s.supersededNow(ctx) {
			left := make([]int, 0, len(dues)-i)
			for _, r := range dues[i:] {
				left = append(left, r.Summary.Source.ID)
			}
			if err := release(ctx, s.App, left, s.now()); err != nil {
				slog.Error("Could not hand back the sources left", "object", object, "error", err)
			}
			slog.Warn("A newer vetting build was announced; not saving this build's verdicts", "object", object, "sources_left", len(left))
			return done, failed
		}
		if err != nil {
			failed++
			slog.Warn("Vetting failed", "source", id, "error", err)
		} else {
			done++
		}
		if e := save(ctx, s.App, id, d.Hash, s.stampOf(d), rep, err, s.now()); errors.Is(e, errNewerVet) {
			slog.Warn("Not overwriting a newer pipeline's vetting", "source", id, "pipeline", pipelineOf(d.Summary))
		} else if e != nil {
			slog.Error("Could not save the vetting", "source", id, "error", e)
		}
	}
	return done, failed
}

func (s *Service) stampOf(d Due) stamp {
	return stamp{pipeline: pipelineOf(d.Summary), build: s.Build}
}

// supersededNow says newer vetting code was announced since the run
// started; once it has, it stays so for the rest of the run.
func (s *Service) supersededNow(ctx context.Context) bool {
	if s.replaced {
		return true
	}
	ann, ok, err := announced(ctx, s.App)
	if err != nil {
		slog.Warn("Could not read the announced vetting build", "error", err)
		return false
	}
	s.replaced = ok && superseded(ann, s.Build, s.started)
	return s.replaced
}

// objectWork is what one target's sources are checked from: their
// candidates, runs and own image measurements.
type objectWork struct {
	object string
	f      surveys.Field
	byID   map[int][]app.DiscoveryCandidate
	runs   map[int]app.DiscoveryRun
	own    map[int]*ownimage.Result
	tracks map[int]*ownimage.TrackResult
	// movers are the moving objects' reports before their stacks are
	// judged, stacks the stacks made, stackAsked those asked for and
	// stackLater those left for a later run.
	movers     map[int]*moverVet
	stacks     map[int]*ownimage.TrackStack
	stackAsked map[int]bool
	stackLater map[int]bool
	pulled     memwatch.Peaks
	measured   memwatch.Peaks
}

// prepare pulls the target's survey data and measures its sources in
// its own subs: changes at their spots, moving objects along their
// tracks.
func (s *Service) prepare(ctx context.Context, object string, dues []Due) (*objectWork, error) {
	f, _, err := s.field(ctx, object)
	if err != nil {
		return nil, err
	}
	w := &objectWork{object: object, f: f, own: map[int]*ownimage.Result{}, tracks: map[int]*ownimage.TrackResult{},
		movers: map[int]*moverVet{}, stacks: map[int]*ownimage.TrackStack{}, stackAsked: map[int]bool{}, stackLater: map[int]bool{}}
	s.Report.Set(object+": pulling survey data", 0, 0, "")
	if s.Surveys != nil && !s.ReadOnly {
		pctx := ctx
		if slices.ContainsFunc(dues, func(d Due) bool { return refreshed(d.Summary) }) {
			pctx = surveys.Fresh(ctx)
		}
		if err := s.Surveys.PrefetchProgress(pctx, f, func(done, total int) {
			s.Report.Set(object+": pulling survey data", int64(done), int64(total), "files")
		}); err != nil {
			slog.Warn("Could not pull the field's survey data; reading per star", "object", object, "error", err)
		}
	}
	ids := make([]int, len(dues))
	for i, d := range dues {
		ids[i] = d.Summary.Source.ID
	}
	var cands []app.DiscoveryCandidate
	if err := s.App.WithContext(ctx).Where("source_id IN ?", ids).Find(&cands).Error; err != nil {
		return nil, err
	}
	w.byID = map[int][]app.DiscoveryCandidate{}
	runIDs := map[int]bool{}
	for _, c := range cands {
		w.byID[*c.SourceID] = append(w.byID[*c.SourceID], c)
		runIDs[c.RunID] = true
	}
	if w.runs, err = s.runs(ctx, runIDs); err != nil {
		return nil, err
	}
	spots := make([]ownimage.Spot, 0, len(dues))
	var tracks []ownimage.TrackSpot
	for _, d := range dues {
		if d.Summary.Source.Kind == kindMover {
			if t, ok := trackSpotOf(d.Summary.Source.ID, w.byID[d.Summary.Source.ID]); ok {
				tracks = append(tracks, t)
			}
			continue
		}
		spots = append(spots, s.withStar(ctx, spotOf(d.Summary, w.byID[d.Summary.Source.ID], w.runs, s.now()), w.byID[d.Summary.Source.ID]))
	}
	w.pulled = s.Mem.Step()
	slog.Info("Vetting a target", "object", object, "sources", len(dues), "moving", len(tracks))
	if len(spots) > 0 {
		own, err := ownimage.Measure(ctx, s.Data, object, spots, ownimage.Options{Threads: s.threads(), WorkDir: s.WorkDir, Progress: func(done, total int) {
			s.Report.Set(object+": reading our own subs", int64(done), int64(total), "subs")
		}})
		if err != nil {
			slog.Warn("Own image checks failed", "object", object, "error", err)
		}
		for i := range own {
			w.own[own[i].SourceID] = &own[i]
		}
	}
	if len(tracks) > 0 {
		res, err := ownimage.MeasureTracks(ctx, s.Data, object, tracks, ownimage.Options{Threads: s.threads(), Progress: func(done, total int) {
			s.Report.Set(object+": reading the frames along moving objects", int64(done), int64(total), "images")
		}})
		if err != nil {
			slog.Warn("Moving object image checks failed", "object", object, "error", err)
		}
		for i := range res {
			w.tracks[res[i].SourceID] = &res[i]
		}
	}
	w.measured = s.Mem.Step()
	return w, nil
}

// checkSource vets one source of a prepared target.
func (s *Service) checkSource(ctx context.Context, w *objectWork, sum discoverysource.Summary) (*Report, error) {
	id := sum.Source.ID
	var rep *Report
	var err error
	if sum.Source.Kind == kindMover {
		rep = s.moverReport(ctx, w, sum)
	} else {
		rep, err = s.vetSource(ctx, w.f, sum, w.runs, w.own[id])
	}
	if rep != nil {
		rep.SourceID = id
	}
	return rep, err
}

// Preview vets the sources ids as a run would and hands each report to
// each as it is ready, writing nothing to the database.
func (s *Service) Preview(ctx context.Context, ids []int, each func(*Report) error) error {
	list, err := summaries(ctx, s.App, nil, ids)
	if err != nil {
		return err
	}
	byObject := map[string][]Due{}
	var objects []string
	for _, sum := range list {
		o := sum.Source.Object
		if _, ok := byObject[o]; !ok {
			objects = append(objects, o)
		}
		byObject[o] = append(byObject[o], Due{Summary: sum, Hash: hashOf(sum)})
	}
	for _, o := range objects {
		w, err := s.prepare(ctx, o, byObject[o])
		if err != nil {
			return fmt.Errorf("%s: %w", o, err)
		}
		s.stackMovers(ctx, w, byObject[o], true)
		for _, d := range byObject[o] {
			rep, err := s.checkSource(ctx, w, d.Summary)
			if err != nil {
				return fmt.Errorf("source %d: %w", d.Summary.Source.ID, err)
			}
			if err := each(rep); err != nil {
				return err
			}
		}
	}
	return nil
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

// withStar names in a spot whose source has no star the Gaia star the vet
// takes for it, so its own frames measure the star the rest of the vet
// judges. 28076 (Abell 85, 2026-01-17), a 0.41 mag fade where the vet took
// the G 14.08 star the detection's reference magnitude matched, 3.8"
// from the spot, was judged as that star while its own frames found no
// star within 2" and measured nothing.
func (s *Service) withStar(ctx context.Context, sp ownimage.Spot, members []app.DiscoveryCandidate) ownimage.Spot {
	if sp.GaiaG != nil {
		return sp
	}
	st, err := s.identify(ctx, sp.RA, sp.Dec, refMagOf(members))
	if err != nil || !st.found {
		return sp
	}
	g := st.target.G
	sp.GaiaG = &g
	return sp
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
	plx, plxE  float64
	neighbours []surveys.Neighbour
}

// Star matching: a spot takes the nearest Gaia star within starArcsec
// whose G is within starMagMatch of the spot's reference magnitude, and
// without a reference magnitude the nearest star within closeArcsec
// unless one there is over a magnitude brighter. Difference image
// centroids of blended stars sit a pixel or more off the star.
const (
	starArcsec      = 4.0
	closeArcsec     = 2.0
	starMagMatch    = 1.5
	neighbourArcsec = 10.0
)

// pickStar is the index in stars, nearest first, of the star the vet
// takes for the spot at ra, dec in year, or -1.
func pickStar(stars []starcat.GaiaStar, ra, dec, year, refMag float64) int {
	best := -1
	if !math.IsNaN(refMag) {
		bestSep := math.Inf(1)
		for i, g := range stars {
			gra, gdec := g.At(year)
			sep := arcsec(ra, dec, gra, gdec)
			if sep > starArcsec || math.Abs(float64(g.G)-refMag) > starMagMatch {
				continue
			}
			if sep < bestSep {
				best, bestSep = i, sep
			}
		}
		if best >= 0 {
			return best
		}
	}
	for i, g := range stars {
		gra, gdec := g.At(year)
		if arcsec(ra, dec, gra, gdec) <= closeArcsec && (best < 0 || g.G < stars[best].G-1) {
			best = i
		}
	}
	return best
}

// refMagOf is the median reference magnitude of a source's changes, NaN
// when none has one.
func refMagOf(members []app.DiscoveryCandidate) float64 {
	var v []float64
	for _, m := range members {
		if m.RefMag != nil && !math.IsNaN(*m.RefMag) && m.Kind == app.DiscoveryKindChange {
			v = append(v, *m.RefMag)
		}
	}
	if len(v) == 0 {
		return math.NaN()
	}
	sort.Float64s(v)
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

func (s *Service) identify(ctx context.Context, ra, dec, refMag float64) (star, error) {
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
	best := pickStar(stars, ra, dec, epochYear(s.now()), refMag)
	if best < 0 {
		return out, nil
	}
	g := stars[best]
	out.found = true
	out.bp, out.rp = float64(g.BP), float64(g.RP)
	out.plx, out.plxE = finite(g.Parallax), finite(float64(g.ParallaxError))
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
	if refreshed(sum) {
		ctx = surveys.Fresh(ctx)
	}
	ra, dec := sum.Source.RA, sum.Source.Dec
	st, err := s.identify(ctx, ra, dec, refMagOf(sum.Members))
	if err != nil {
		slog.Warn("Could not read Gaia at the spot", "error", err)
	}
	cra, cdec := ra, dec
	if st.found {
		cra, cdec = starcat.Propagate(st.target.RA, st.target.Dec, st.target.PMRA, st.target.PMDec, epochYear(s.now())-starcat.GaiaEpoch)
	}
	known := catalogChecks(ctx, s.Mirrors, s.Simbad, cra, cdec, st.target.GaiaID)
	rep := &Report{Version: Version, SourceID: sum.Source.ID, RanAt: s.now(), Star: starLine(known.Name, st)}
	flags := ownimage.Flags{}
	var curve *ownimage.Curve
	var ownChecks []check.Check
	if own != nil {
		flags, curve, ownChecks = own.Flags, own.Curve, own.Checks
	}
	if !st.found && isTransient(sum) {
		rep.FadesRead, rep.FadeNightsRead = true, true
		s.vetTransient(ctx, f, sum, rep, own, ownChecks, known, s.movingCheck(ctx, sum, runs, surveys.Fetch{}), flags)
		return rep, nil
	}
	artifact := flags.HotPixel || flags.RefContaminated || flags.Trail || flags.Edge
	var fx fetches
	more := false
	fetchMore := func() {
		more = true
		fx.neowise = s.Surveys.NEOWISE(ctx, f, st.target)
		fx.atlas = s.Surveys.ATLAS(ctx, f, st.target)
		fx.tess = s.Surveys.TESS(ctx, f, st.target)
	}
	if s.Surveys != nil && sum.Source.Kind != kindMover {
		fx.ztf = s.Surveys.ZTF(ctx, f, st.target)
		if refreshed(sum) || (!artifact && known.Listed == "" && worthMore(fx.ztf, curve, sum)) {
			fetchMore()
		} else {
			why := "Not fetched: the checks above already explain the spot."
			fx.neowise = surveys.Fetch{Survey: lc.NEOWISE, Note: why}
			fx.atlas = surveys.Fetch{Survey: lc.ATLAS, Note: why}
			fx.tess = surveys.Fetch{Survey: lc.TESS, Note: why}
		}
	}
	var ownSeries []lc.Series
	if curve != nil {
		ownSeries = curve.Series()
	}
	change := changeOf(sum)
	colour := s.colourOf(ctx, st)
	analyzeAll := func() (*period.Result, []lc.Series) {
		var series []lc.Series
		for _, x := range fx.all() {
			series = append(series, x.Series...)
		}
		if sum.Source.Kind == kindMover || len(series) == 0 {
			return nil, series
		}
		opts := period.Options{ChangeMag: change.Mag, G: finite(st.target.G), Parallax: st.plx, ParallaxErr: st.plxE, Colour: colour}
		if !change.Fainter {
			opts.ChangeMag = -change.Mag
		}
		opts.Comparisons = func() [][]lc.Series { return s.comparisons(ctx, f, st) }
		opts.Neighbours = func() [][]lc.Series { return s.fadeNeighbours(ctx, f, st) }
		opts.TESSComparisons = func() []period.TESSComp { return s.tessComparisons(ctx, f, st, fx.tess.Sectors) }
		pr, err := period.Analyze(append(series, ownSeries...), opts)
		if err != nil && !errors.Is(err, period.ErrNoPeriod) {
			slog.Warn("Period search failed", "source", sum.Source.ID, "error", err)
		}
		return pr, series
	}
	pr, series := analyzeAll()
	if !more && s.Surveys != nil && !artifact && known.Listed == "" && surveyVaries(pr) != "" {
		fetchMore()
		pr, series = analyzeAll()
	}
	for _, x := range fx.all() {
		if x.State == surveys.Pending {
			rep.Pending = append(rep.Pending, x.Survey)
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
	rep.FadesRead = fx.ztf.State == surveys.Done || fx.ztf.State == surveys.Uncovered
	rep.FadeNightsRead = rep.FadesRead
	if pr != nil {
		rep.Fades = pr.Fades
		rep.ZTFFadeNights = eveningDates(pr.SteadyFadeNights)
		rep.Ephemeris = pr.Ephemeris
		rep.Survey, rep.SurveyDoubled = surveySummaries(series, pr.Ephemeris)
	}
	skews, err := runSkews(ctx, s.App, sum.Members)
	if err != nil {
		slog.Warn("Could not count the changes of the source's runs", "source", sum.Source.ID, "error", err)
	}
	sug := onBadNights(suggest(sum, flags, curve, known, moving, fx, pr, timing, rep.Groups, change), badNights(sum.Members, skews, runs), rep.Groups)
	rep.Suggestion = blendOf(st, change, refMagOf(sum.Members)).notSure(sug)
	return rep, nil
}

// surveySummaries measures an eclipsing ephemeris's dips in the survey
// light curves, and for a star with one dip per cycle the same at twice
// its period.
func surveySummaries(series []lc.Series, e *period.Ephemeris) (*eclfacts.Summary, *eclfacts.Summary) {
	if e == nil || e.Kind != period.KindEclipsing {
		return nil, nil
	}
	var dbl *eclfacts.Summary
	if d := eclfacts.Doubled(e); d != nil {
		dbl = eclfacts.Summarize(series, d)
	}
	return eclfacts.Summarize(series, e), dbl
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
	for _, s := range period.AlignSrc(ss, nil) {
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

// colourOf is the star's Gaia BP-RP with its GSP-Phot reddening, or nil
// when either is missing.
func (s *Service) colourOf(ctx context.Context, st star) *period.Colour {
	bprp := st.bp - st.rp
	if s.Reddening == nil || !st.found || st.target.GaiaID == 0 || math.IsNaN(bprp) || math.IsInf(bprp, 0) || st.bp == 0 || st.rp == 0 {
		return nil
	}
	r, err := s.Reddening(ctx, st.target.GaiaID)
	if err != nil {
		slog.Warn("Gaia reddening lookup failed", "gaia", st.target.GaiaID, "error", err)
		return nil
	}
	if !r.Found {
		return nil
	}
	return &period.Colour{BPRP: bprp, EBPRPUpper: r.EBPRPUpper}
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
