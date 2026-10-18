package vetting

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

const (
	// knownRadius is how far, in degrees, known bodies are listed around
	// a track: wide enough for an orbit known to a few arcmin.
	knownRadius = 0.5
	// motionWide is the wider list used when too few asteroids are near.
	motionWide = 1.5
	// otherNightDays is how many nights either side are searched.
	otherNightDays = 3
	minNightRadius = 60.0
)

// trackSpotOf is the source's best track as the image checks read it.
func trackSpotOf(id int, members []app.DiscoveryCandidate) (ownimage.TrackSpot, bool) {
	t, ok := trackOf(members)
	if !ok {
		return ownimage.TrackSpot{}, false
	}
	s := ownimage.TrackSpot{SourceID: id, Night: t.cand.Night.UTC(), Group: t.cand.Group}
	for _, p := range t.points {
		tp := ownimage.TrackPoint{FrameID: p.FrameID, Filter: p.Filter, Time: p.Time, X: p.X, Y: p.Y, SNR: p.SNR, Flux: p.Flux}
		if p.Mag != nil {
			tp.Mag = *p.Mag
		}
		s.Points = append(s.Points, tp)
	}
	return s, true
}

// frameAngle is the track's direction on the frame, in degrees from +x
// toward +y.
func frameAngle(pts []discoveries.TrackPoint) float64 {
	if len(pts) < 2 {
		return 0
	}
	t0 := pts[0].Time
	ts := make([]float64, len(pts))
	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))
	ws := make([]float64, len(pts))
	for i, p := range pts {
		ts[i], xs[i], ys[i], ws[i] = p.Time.Sub(t0).Minutes(), p.X, p.Y, 1
	}
	vx, vy := polyFit(ts, xs, ws, 1)[1], polyFit(ts, ys, ws, 1)[1]
	return math.Atan2(vy, vx) * 180 / math.Pi
}

// moverVet is a moving object's report before its stack along its motion
// is judged, with what its suggestion reads.
type moverVet struct {
	rep   *Report
	flags ownimage.TrackFlags
	known *knownBody
	final bool
}

// stacked puts the stack check in the report and suggests again with it.
func (mv *moverVet) stacked(c check.Check, v ownimage.StackVerdict) *Report {
	for gi := range mv.rep.Groups {
		for ci := range mv.rep.Groups[gi].Checks {
			if mv.rep.Groups[gi].Checks[ci].ID == check.MoverStack {
				mv.rep.Groups[gi].Checks[ci] = c
			}
		}
	}
	if !mv.final {
		mv.rep.Suggestion = moverSuggest(mv.rep.Groups, mv.flags, mv.known, &v)
	}
	return mv.rep
}

// vetMover runs a moving object's checks, all but its stack along its
// motion, and suggests from them alone.
func (s *Service) vetMover(ctx context.Context, sum discoverysource.Summary, members []app.DiscoveryCandidate, runs map[int]app.DiscoveryRun,
	tr *ownimage.TrackResult,
) *moverVet {
	rep := &Report{Version: Version, SourceID: sum.Source.ID, RanAt: s.now(), Star: "Moving object"}
	t, ok := trackOf(members)
	if !ok {
		ids := moverIDs()
		cs := make([]check.Check, 0, len(ids))
		for _, id := range ids {
			cs = append(cs, check.Skipped(id, "Not measured. The track's detections are not stored."))
		}
		rep.Groups = []Group{{ID: check.GroupMover, Name: groupNames()[check.GroupMover], Checks: cs}}
		rep.Suggestion = Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: "The track's detections are not stored, so nothing could be checked."}
		return &moverVet{rep: rep, final: true}
	}
	if t.cand.NightMag != nil {
		rep.Star = fmt.Sprintf("Moving object · magnitude %.1f", *t.cand.NightMag)
	}
	run := runs[t.cand.RunID]
	img, flags := s.trackImageChecks(ctx, t, run, tr)
	if dw := depthWords(t, run); dw != "" {
		img[0].Evidence += dw
	}
	lists, err := s.trackBodies(ctx, t)
	known, kb := knownCheck(t, lists, knownRadius, err)
	var motion check.Check
	if err != nil || len(lists) == 0 {
		motion = check.Skipped(check.MoverMotion, "Not run: the known asteroid list could not be read.")
	} else {
		mid := lists[len(lists)/2]
		bodies, radius := mid.bodies, knownRadius
		if asteroidCount(bodies) < 5 {
			if wide, werr := s.Bodies(ctx, mid.ra, mid.dec, motionWide, mid.at); werr == nil {
				bodies, radius = wide, motionWide
			}
		}
		motion = motionCheck(t, bodies, radius)
	}
	nights := nightsCheck(s.otherNights(ctx, sum.Source.Object, t))
	stack := check.Skipped(check.MoverStack, stackSettled)
	checks := []check.Check{img[0], lineCheck(t), img[1], img[2], img[3], stack, known, motion, nights}
	rep.Groups = []Group{{ID: check.GroupMover, Name: groupNames()[check.GroupMover], Checks: checks}}
	rep.Charts = []Chart{trackChart(t)}
	rep.Suggestion = moverSuggest(rep.Groups, flags, kb, nil)
	return &moverVet{rep: rep, flags: flags, known: kb}
}

func moverIDs() []string {
	return []string{check.MoverFrames, check.MoverLine, check.MoverSensor, check.MoverShape, check.MoverSteady, check.MoverStack, check.MoverKnown, check.MoverMotion, check.MoverNights}
}

func asteroidCount(bs []discoveries.Body) int {
	n := 0
	for _, b := range bs {
		if b.Kind != kindPlanet && b.Kind != kindComet {
			n++
		}
	}
	return n
}

// trackImageChecks are the frames, sensor, shape and steadiness checks,
// in that order.
func (s *Service) trackImageChecks(ctx context.Context, t track, run app.DiscoveryRun, tr *ownimage.TrackResult) ([]check.Check, ownimage.TrackFlags) {
	snrs := make([]float64, len(t.points))
	for i, p := range t.points {
		snrs[i] = p.SNR
	}
	if tr == nil || len(tr.Subs) == 0 {
		why := "Not measured yet. Its frames could not be read."
		if tr != nil && tr.Note != "" {
			why = "Not measured yet. " + tr.Note
		}
		res := &ownimage.TrackResult{}
		cs, f := ownimage.TrackChecks(res, len(t.points), snrs, t.rate, run.PixelScale, frameAngle(t.points))
		for i := 1; i < len(cs); i++ {
			cs[i] = check.Skipped(cs[i].ID, why)
		}
		return cs, f
	}
	scale := run.PixelScale
	if !(scale > 0) {
		if w, _, _, err := s.Data.Frame(ctx, t.cand.Object); err == nil {
			scale = w.PixelScale()
		}
	}
	return ownimage.TrackChecks(tr, len(t.points), snrs, t.rate, scale, frameAngle(t.points))
}

// trackBodies lists the known bodies around the track at its first,
// middle and last detections.
func (s *Service) trackBodies(ctx context.Context, t track) ([]epochBodies, error) {
	if s.Bodies == nil {
		return nil, nil
	}
	idx := []int{0, len(t.points) / 2, len(t.points) - 1}
	var out []epochBodies
	seen := map[int]bool{}
	for _, i := range idx {
		if seen[i] {
			continue
		}
		seen[i] = true
		p := t.points[i]
		bs, err := s.Bodies(ctx, p.RA, p.Dec, knownRadius, p.Time)
		if err != nil {
			return nil, err
		}
		out = append(out, epochBodies{at: p.Time, ra: p.RA, dec: p.Dec, bodies: bs})
	}
	return out, nil
}

// otherNights predicts where the track's straight path puts the object
// on the target's nights within 3 days and looks there for a moving
// object found that night.
func (s *Service) otherNights(ctx context.Context, object string, t track) []otherNight {
	if s.Data == nil {
		return nil
	}
	subs, err := s.Data.Subs(ctx, object)
	if err != nil {
		slog.Warn("Could not list the target's subs for the other nights", "object", object, "error", err)
		return nil
	}
	w, width, height, err := s.Data.Frame(ctx, object)
	if err != nil {
		return nil
	}
	own := t.cand.Night.UTC().Format(time.DateOnly)
	mids := map[string][]time.Time{}
	for _, sb := range subs {
		k := sb.Night.UTC().Format(time.DateOnly)
		d := sb.Night.Sub(t.cand.Night.UTC()).Hours() / 24
		if k == own || math.Abs(d) > otherNightDays+0.5 {
			continue
		}
		mids[k] = append(mids[k], sb.Start.Add(time.Duration(sb.Exposure/2*float64(time.Second))))
	}
	keys := make([]string, 0, len(mids))
	for k := range mids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []otherNight
	for _, k := range keys {
		ts := mids[k]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		night, _ := time.Parse(time.DateOnly, k)
		on := otherNight{night: night, at: ts[len(ts)/2]}
		on.ra, on.dec = t.fit.at(on.at)
		if x, y, ok := w.PixelOf(on.ra, on.dec); ok && x >= 0 && y >= 0 && x < float64(width) && y < float64(height) {
			on.covered = true
		}
		moved := t.rate * math.Abs(on.at.Sub(t.fit.t0).Minutes())
		on.radius = math.Max(minNightRadius, 0.05*moved)
		if on.covered {
			s.lookOnNight(ctx, object, t, &on)
		}
		out = append(out, on)
	}
	return out
}

func (s *Service) lookOnNight(ctx context.Context, object string, t track, on *otherNight) {
	var runs []app.DiscoveryRun
	if err := s.App.WithContext(ctx).Where("object = ? AND night = ? AND state = ?", object, on.night, app.DiscoveryDone).Find(&runs).Error; err != nil {
		return
	}
	for _, r := range runs {
		on.compared = true
		if r.SubDepth != nil && (on.depth == nil || *r.SubDepth > *on.depth) {
			v := *r.SubDepth
			on.depth = &v
		}
	}
	if !on.compared {
		return
	}
	var cands []app.DiscoveryCandidate
	if err := s.App.WithContext(ctx).Where("object = ? AND night = ? AND kind = ?", object, on.night, kindMover).Find(&cands).Error; err != nil {
		return
	}
	best := math.Inf(1)
	for i, c := range cands {
		var rec discoveries.TrackRecord
		if json.Unmarshal([]byte(c.Track), &rec) != nil || len(rec.Points) == 0 {
			continue
		}
		mid := rec.Points[len(rec.Points)/2]
		ra, dec := t.fit.at(mid.Time)
		sep := arcsec(ra, dec, mid.RA, mid.Dec)
		if sep > on.radius || !sameMotion(t.rate, t.pa, rec.RateArcmin, rec.PA) {
			continue
		}
		if sep < best {
			best = sep
			on.found, on.sep = &cands[i], sep
		}
	}
}
