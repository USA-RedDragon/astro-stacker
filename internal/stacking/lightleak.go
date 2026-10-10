package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func frameSetup(f app.Frame) darkcheck.Setup {
	s := darkcheck.Setup{Exposure: val(f.Exposure), Gain: val(f.Gain), Offset: val(f.Offset), BinX: val(f.BinX), SetTemp: val(f.SetTemp), CCDTemp: f.CCDTemp}
	switch {
	case f.DateObs != nil:
		s.TakenAt = *f.DateObs
	case f.Night != nil:
		s.TakenAt = *f.Night
	}
	return s
}

func (p *Pipeline) dropLeakyDarks(ctx context.Context, frames []app.Frame, files []string) (int, error) {
	refs, err := darkcheck.LoadReferences(ctx, p.db, coverage.SessionGap)
	if err != nil {
		return 0, err
	}
	left := 0
	for i, f := range frames {
		if f.CalCheck != nil && *f.CalCheck == darkcheck.StateClean {
			left++
			continue
		}
		b, err := os.ReadFile(files[i])
		if err != nil {
			return 0, err
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", f.Key, err)
		}
		m := darkcheck.Measure(im.Data, im.W, im.H)
		s := frameSetup(f)
		v := refs.Judge(s, m)
		cols := map[string]any{
			"dark_spread": m.Spread, "cal_median_adu": m.Median, "cal_spread_adu": m.Spread, "cal_noise_adu": m.Noise,
			"cal_measured_at": time.Now().UTC(), "cal_check": v.State, "cal_check_reason": v.Reason,
		}
		if v.State == darkcheck.StateLeak && !p.opts.RejectDarksSince.IsZero() && !s.TakenAt.Before(p.opts.RejectDarksSince) {
			cols["light_leak"] = m.Spread
			slog.Warn("Leaving out a rejected dark", "key", f.Key, "reason", v.Reason)
			if err := os.Remove(files[i]); err != nil {
				return 0, err
			}
		} else {
			if v.State == darkcheck.StateClean {
				refs.AddClean(s, m)
			}
			left++
		}
		if err := p.db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).UpdateColumns(cols).Error; err != nil {
			return 0, err
		}
	}
	return left, nil
}
