package binaries

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func TestDebugDumpsTheNight(t *testing.T) {
	t.Parallel()
	e := timingEphemeris()
	half := 0.03 * e.Period
	prim := e.T0 + 200*e.Period
	ph := &fakePhot{e: e, noise: 0.008, truth: map[float64]float64{prim: prim + 3.0/1440}}
	ph.subs = subsOver(prim, 4*half, 120, 1)
	now := lc.UTCOfBJD(prim+5, testRA, testDec)
	svc := &Service{Photometry: ph, Loc: chicago(t), Now: func() time.Time { return now }}
	st := timingStar(e)
	evs := events(0, st, ph.subs, nil, now)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	dbg := svc.debugEvent(context.Background(), st, evs[0], ph.subs)
	if len(dbg.Points) != len(evs[0].subs) || len(dbg.NightPoints) != len(ph.subs) || len(dbg.Tries) != 1 || len(dbg.Scan[redFilter]) == 0 {
		t.Fatalf("points %d of %d, night %d of %d, tries %d, scan %d", len(dbg.Points), len(evs[0].subs), len(dbg.NightPoints), len(ph.subs),
			len(dbg.Tries), len(dbg.Scan[redFilter]))
	}
	if !dbg.Tries[0].OK || math.Abs(float64(dbg.Tries[0].OCMinutes)-3) > 1 || !strings.HasPrefix(dbg.Note, "Timed in "+redFilter+": 3") {
		t.Errorf("try %+v, note %q", dbg.Tries[0], dbg.Note)
	}
	deepest := dbg.Scan[redFilter][0]
	for _, r := range dbg.Scan[redFilter] {
		if r.Depth > deepest.Depth {
			deepest = r
		}
	}
	if math.Abs(float64(deepest.Minutes)-3) > float64(dbg.HalfMinutes)/scanSteps {
		t.Errorf("the scan's deepest dip is at %.1f minutes, want 3", float64(deepest.Minutes))
	}
	if _, err := json.Marshal(dbg); err != nil {
		t.Fatal(err)
	}
}
