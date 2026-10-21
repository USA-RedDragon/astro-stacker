//go:build vettingdev

package ownimage

import (
	"context"
	"testing"
	"time"
)

func TestDevLive15523(t *testing.T) {
	ctx := context.Background()
	d := newDev(t)
	spot, err := d.sourceSpot(ctx, 15523)
	if err != nil {
		t.Fatal(err)
	}
	spot.Nights = nil
	l, err := NewLive(ctx, d, spot.Object, spot)
	if err != nil {
		t.Fatal(err)
	}
	subs, err := d.Subs(ctx, spot.Object)
	if err != nil {
		t.Fatal(err)
	}
	from, to := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC), time.Date(2026, 10, 21, 2, 55, 0, 0, time.UTC)
	for _, s := range subs {
		if s.Start.Before(from) || s.Start.After(to) {
			continue
		}
		if err := l.Add(ctx, d, s); err != nil {
			t.Fatal(err)
		}
	}
	pts, _, _ := l.Points()
	var mags, errs []float64
	for _, p := range pts {
		mags, errs = append(mags, p.Mag), append(errs, p.Err)
		t.Logf("%s %.3f ± %.3f", p.Time.Format("15:04"), p.Mag, p.Err)
	}
	if len(pts) < 10 {
		t.Fatalf("%d points", len(pts))
	}
	if e, s := median(errs), robustStd(mags); e > 0.1 || s > 0.15 {
		t.Errorf("tonight's subs measured to %.3f mag, scattering %.3f; the fit alone gives 0.07", e, s)
	}
}
