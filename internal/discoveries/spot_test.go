package discoveries

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestSpotNightsSeesTheNewSourceOnlyWhereItIs(t *testing.T) {
	t.Parallel()
	fx := newRunnerFixture(t)
	f := fx.f
	ra, dec := f.WCS.SkyOf(f.NewX, f.NewY)
	last := f.Nights[len(f.Nights)-1]
	got, err := fx.r.SpotNights(context.Background(), synthObject, ra, dec, SpotOptions{Before: last.AddDate(0, 0, 1), PerNight: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(f.Nights) {
		t.Fatalf("measured %d nights: %+v", len(got), got)
	}
	for _, sn := range got {
		if sn.Subs != 4 || sn.Limit == nil || !(sn.FluxErr > 0) || sn.Mid.IsZero() {
			t.Errorf("night %s: %+v", sn.Night.Format("Jan 2"), sn)
		}
		if sn.Night.Equal(last) != sn.Detected {
			t.Errorf("night %s detected %v at %.1f sigma", sn.Night.Format("Jan 2"), sn.Detected, sn.SNR)
		}
		if sn.Night.Equal(last) && (sn.Mag == nil || math.Abs(*sn.Mag-(f.zp-2.5*math.Log10(0.6*1000))) > 0.6) {
			t.Errorf("magnitude %v on the night it shows", sn.Mag)
		}
	}
	if !got[0].Night.Equal(last) {
		t.Errorf("not newest first: %v", got[0].Night)
	}
	got, err = fx.r.SpotNights(context.Background(), synthObject, ra, dec, SpotOptions{Before: last, Nights: 2, PerNight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Detected || got[1].Detected {
		t.Errorf("before the find: %+v", got)
	}
	if _, err := fx.r.SpotNights(context.Background(), synthObject, ra+5, dec, SpotOptions{Before: last}); !errors.Is(err, ErrNoSpot) {
		t.Errorf("off the frame: %v", err)
	}
}
