package binaries

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries/minima"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

const redFilter = "Red"

// lostPhot is fakePhot with the star lost in every frame within half of
// mid.
type lostPhot struct {
	*fakePhot
	mid, half float64
}

func (l lostPhot) Curve(ctx context.Context, obj string, spot ownimage.Spot, subs []ownimage.Sub) (*ownimage.Curve, error) {
	c, err := l.fakePhot.Curve(ctx, obj, spot, subs)
	if err != nil {
		return nil, err
	}
	kept := c.Points[:0]
	for _, p := range c.Points {
		if math.Abs(p.BJD-l.mid) >= l.half {
			kept = append(kept, p)
		}
	}
	c.Points = kept
	return c, nil
}

func flatCurve(filters ...string) *ownimage.Curve {
	c := &ownimage.Curve{}
	for i := range 120 {
		f := filters[i%len(filters)]
		c.Points = append(c.Points, ownimage.Point{FrameID: i, Filter: f, BJD: 2461000.4 + float64(i)*120/86400,
			DMag: 0.02 * float64(1+i%len(filters)) * math.Sin(float64(i)*12.9898), Err: 0.02 * float64(1+i%len(filters))})
	}
	return c
}

func TestUntimedSaysEachFiltersReason(t *testing.T) {
	t.Parallel()
	e := timingEphemeris()
	d := minima.Dip{Mid: 2461000.4 + 60*120.0/86400, Half: dipHalf(e, 0), Shape: dipShape(e, 0)}
	_, _, why, ok := bestMinimum(flatCurve(redFilter, "Green"), d)
	if ok {
		t.Fatal("a flat night was timed")
	}
	if strings.Contains(why, minima.ReasonNoFit) || !strings.Contains(why, "mag") {
		t.Errorf("reason %q", why)
	}
	for _, f := range []string{redFilter, "Green"} {
		if !strings.Contains(why, "in "+f+", ") {
			t.Errorf("reason %q does not name %s", why, f)
		}
	}
}

func TestUntimedCountsFramesWithoutMeasurement(t *testing.T) {
	t.Parallel()
	e := timingEphemeris()
	half := 0.03 * e.Period
	prim := e.T0 + 200*e.Period
	ph := lostPhot{&fakePhot{e: e, noise: 0.008, subs: subsOver(prim, 1.8*half, 150, 1)}, prim, half}
	now := lc.UTCOfBJD(prim+5, testRA, testDec)
	svc := &Service{Photometry: ph, Loc: chicago(t), Now: func() time.Time { return now }}
	st := timingStar(e)
	evs := events(0, st, ph.subs, nil, now)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	row, err := svc.timeEvent(context.Background(), st, evs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Note, "Not timed: ") || !strings.Contains(row.Note, "gave no measurement of the star") || strings.Contains(row.Note, minima.ReasonNoFit) {
		t.Errorf("note %q", row.Note)
	}
}
