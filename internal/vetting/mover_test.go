package vetting

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

const (
	trkRA  = 150.0
	trkDec = 10.0
)

// synthTrack moves at rate arcsec a minute toward pa, with bend arcsec
// per minute squared across the track, sampled every 6 minutes.
func synthTrack(rate, pa, bend float64) track {
	const n = 8
	t0 := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	mag := 18.0
	rec := discoveries.TrackRecord{}
	for i := range n {
		m := float64(i) * 6
		along := rate * m
		across := bend * (m - float64(n-1)*3) * (m - float64(n-1)*3)
		s, c := math.Sin(pa*math.Pi/180), math.Cos(pa*math.Pi/180)
		xi, eta := along*s+across*c, along*c-across*s
		ra := trkRA + xi/3600/math.Cos(trkDec*math.Pi/180)
		dec := trkDec + eta/3600
		at := t0.Add(time.Duration(m * float64(time.Minute)))
		rec.Points = append(rec.Points, discoveries.TrackPoint{FrameID: i + 1, Time: at, RA: ra, Dec: dec, X: 100 + float64(i), Y: 50, SNR: 8, Mag: &mag})
		rec.ADES = append(rec.ADES, discoveries.ADES{FrameID: i + 1, RMSRA: 0.3})
	}
	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	t, _ := trackOf([]app.DiscoveryCandidate{{ID: 1, RunID: 1, Track: string(b), NightMag: &mag, Night: t0.Truncate(24 * time.Hour)}})
	return t
}

func TestStraightTrackPassesAndCurvedFails(t *testing.T) {
	t.Parallel()
	tr := synthTrack(0.25, 255, 0)
	if math.Abs(tr.rate-0.25) > 1e-3 || math.Abs(tr.pa-255) > 0.1 {
		t.Fatalf("rate %.4f pa %.2f, want 0.25 and 255", tr.rate, tr.pa)
	}
	c := lineCheck(tr)
	if c.Status != check.Pass || !strings.Contains(c.Evidence, "west-southwest (255 degrees)") {
		t.Errorf("straight track: %s %s", c.Status, c.Evidence)
	}
	bent := synthTrack(0.25, 255, 0.01)
	c = lineCheck(bent)
	if c.Status != check.Fail {
		t.Errorf("a track bending by %.1f arcsec passed: %s", 0.01*21*21, c.Evidence)
	}
}

func intp(v int) *int { return &v }

func bodyAt(tr track, i int, offset, rate, pa float64, u *int, number string) discoveries.Body {
	p := tr.points[i]
	return discoveries.Body{Number: number, Name: "Test " + number, Kind: "asteroid", RA: p.RA, Dec: p.Dec + offset/3600, V: 18.2,
		DRA: rate * 60 * math.Sin(pa*math.Pi/180), DDec: rate * 60 * math.Cos(pa*math.Pi/180), U: u}
}

func listsFor(tr track, bodies func(i int) []discoveries.Body) []epochBodies {
	idx := []int{0, len(tr.points) / 2, len(tr.points) - 1}
	out := make([]epochBodies, 0, len(idx))
	for _, i := range idx {
		p := tr.points[i]
		out = append(out, epochBodies{at: p.Time, ra: p.RA, dec: p.Dec, bodies: bodies(i)})
	}
	return out
}

func TestKnownMatchAllowsForOrbitUncertainty(t *testing.T) {
	t.Parallel()
	tr := synthTrack(0.25, 255, 0)
	cases := []struct {
		name   string
		offset float64
		rate   float64
		pa     float64
		u      *int
		match  bool
	}{
		{"numbered, 3 arcsec off", 3, 0.25, 255, intp(0), true},
		{"numbered, 60 arcsec off", 60, 0.25, 255, intp(0), false},
		{"poor orbit, 200 arcsec off, same motion", 200, 0.26, 257, intp(4), true},
		{"poor orbit, 200 arcsec off, other motion", 200, 0.5, 200, intp(4), false},
	}
	for _, c := range cases {
		lists := listsFor(tr, func(i int) []discoveries.Body {
			return []discoveries.Body{bodyAt(tr, i, c.offset, c.rate, c.pa, c.u, "1234")}
		})
		ch, kb := knownCheck(tr, lists, knownRadius, nil)
		if (kb != nil) != c.match || (ch.Status == check.Fail) != c.match {
			t.Errorf("%s: match %v, check %s %s", c.name, kb != nil, ch.Status, ch.Evidence)
		}
	}
	ch, _ := knownCheck(tr, listsFor(tr, func(int) []discoveries.Body { return nil }), knownRadius, nil)
	if ch.Status != check.Pass || !strings.Contains(ch.Evidence, "within 30 arcmin") {
		t.Errorf("nothing near: %s %s", ch.Status, ch.Evidence)
	}
}

func TestMotionComparedWithAsteroidsAround(t *testing.T) {
	t.Parallel()
	tr := synthTrack(0.25, 255, 0)
	like := make([]discoveries.Body, 0, 12)
	for k := range 12 {
		like = append(like, bodyAt(tr, 0, 600+float64(k), 0.22+0.01*float64(k%5), 250+float64(k%4), intp(0), ""))
	}
	if c := motionCheck(tr, like, knownRadius); c.Status != check.Pass {
		t.Errorf("moves like its neighbours: %s %s", c.Status, c.Evidence)
	}
	odd := synthTrack(0.18, 20, 0)
	if c := motionCheck(odd, like, knownRadius); c.Status != check.Fail {
		t.Errorf("moves unlike its neighbours: %s %s", c.Status, c.Evidence)
	}
	fast := synthTrack(1.5, 255, 0)
	if c := motionCheck(fast, like, knownRadius); c.Status != check.Pass || !strings.Contains(c.Evidence, "passing near the Earth") {
		t.Errorf("faster than all: %s %s", c.Status, c.Evidence)
	}
	if c := motionCheck(tr, like[:3], knownRadius); c.Status != check.NotRun {
		t.Errorf("too few to compare: %s %s", c.Status, c.Evidence)
	}
}

func moverGroups(statuses map[string]check.Status) []Group {
	ids := moverIDs()
	cs := make([]check.Check, 0, len(ids))
	for _, id := range ids {
		st, ok := statuses[id]
		if !ok {
			st = check.Pass
		}
		cs = append(cs, check.New(id, st, id+" evidence."))
	}
	return []Group{{ID: check.GroupMover, Checks: cs}}
}

func TestMoverSuggestions(t *testing.T) {
	t.Parallel()
	s := moverSuggest(moverGroups(nil), ownimage.TrackFlags{}, nil)
	if s.State != app.VetReal || !strings.Contains(s.Say, "Minor Planet Center") || s.Confidence != High {
		t.Errorf("all pass: %+v", s)
	}
	s = moverSuggest(moverGroups(map[string]check.Status{check.MoverSensor: check.Fail, check.MoverShape: check.Fail}), ownimage.TrackFlags{HotPixel: true, NotStar: true}, nil)
	if s.State != app.VetArtifact || s.Confidence != High || !strings.Contains(s.Say, "hot pixel") {
		t.Errorf("hot pixel: %+v", s)
	}
	s = moverSuggest(moverGroups(map[string]check.Status{check.MoverSteady: check.Fail}), ownimage.TrackFlags{Flash: true}, nil)
	if s.State != app.VetArtifact || !strings.Contains(s.Say, "flash") {
		t.Errorf("flash: %+v", s)
	}
	kb := &knownBody{body: discoveries.Body{Number: "433", Name: "Eros"}, offset: 2}
	s = moverSuggest(moverGroups(map[string]check.Status{check.MoverKnown: check.Fail}), ownimage.TrackFlags{}, kb)
	if s.State != app.VetKnown || s.Say != "Known asteroid or comet: (433) Eros" || s.Confidence != High {
		t.Errorf("known: %+v", s)
	}
	s = moverSuggest(moverGroups(map[string]check.Status{check.MoverShape: check.NotRun}), ownimage.TrackFlags{}, nil)
	if s.State != app.VetUnsure || !strings.Contains(s.Why, "star-shaped for its speed") {
		t.Errorf("shape not measured: %+v", s)
	}
	s = moverSuggest(moverGroups(map[string]check.Status{check.MoverNights: check.NotRun}), ownimage.TrackFlags{}, nil)
	if s.State != app.VetReal || s.Confidence != Medium {
		t.Errorf("no other night to look on: %+v", s)
	}
	g := moverGroups(map[string]check.Status{check.MoverNights: check.NotRun})
	g[0].Checks[len(g[0].Checks)-1].Evidence = notSeen + " Your frames of 2026-03-02 cover where it would have been."
	s = moverSuggest(g, ownimage.TrackFlags{}, nil)
	if s.State != app.VetReal || s.Confidence != Low || !strings.Contains(s.Why, "2026-03-02") {
		t.Errorf("not seen where another night covered it: %+v", s)
	}
}
