package darkcheck

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	TypeDark = "DARK"
	TypeBias = "BIAS"
)

type Download func(ctx context.Context, key string) ([]byte, error)

func measurePending(db *gorm.DB) *gorm.DB {
	return db.Model(&app.Frame{}).Where("type IN ? AND index_error IS NULL AND cal_measured_at IS NULL", []string{TypeDark, TypeBias})
}

func MeasurePending(ctx context.Context, db *gorm.DB, download Download, workers int) (int, error) {
	var frames []app.Frame
	if err := measurePending(db.WithContext(ctx)).Select("id", "key", "type").Order("id").Find(&frames).Error; err != nil {
		return 0, fmt.Errorf("find calibration frames to measure: %w", err)
	}
	work := make(chan app.Frame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range max(1, workers) {
		wg.Go(func() {
			for f := range work {
				b, err := download(ctx, f.Key)
				if err != nil {
					if ctx.Err() == nil {
						slog.Warn("Could not download calibration frame to check it", "key", f.Key, "error", err)
					}
					continue
				}
				im, err := imagedata.Decode(b)
				if err != nil {
					slog.Warn("Could not read calibration frame to check it", "key", f.Key, "error", err)
					continue
				}
				m := Measure(im.Data, im.W, im.H)
				cols := map[string]any{"cal_median_adu": m.Median, "cal_spread_adu": m.Spread, "cal_noise_adu": m.Noise, "cal_measured_at": time.Now().UTC()}
				if f.Type == TypeDark {
					cols["dark_spread"] = m.Spread
				}
				if err := db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", f.ID).UpdateColumns(cols).Error; err != nil {
					slog.Warn("Could not save calibration frame measures", "key", f.Key, "error", err)
					continue
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		})
	}
	for _, f := range frames {
		select {
		case work <- f:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return done, ctx.Err()
}

type measured struct {
	ID           int
	Key          string
	Type         string
	Exposure     *float64
	Gain         *float64
	Offset       *float64
	BinX         *float64
	SetTemp      *float64
	CCDTemp      *float64
	DateObs      *time.Time
	Night        *time.Time
	CalMedianADU *float64
	CalSpreadADU *float64
	CalNoiseADU  *float64
	CalCheck     *string
}

func fval(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func (m measured) setup() Setup {
	s := Setup{Exposure: fval(m.Exposure), Gain: fval(m.Gain), Offset: fval(m.Offset), BinX: fval(m.BinX), SetTemp: fval(m.SetTemp), CCDTemp: m.CCDTemp}
	switch {
	case m.DateObs != nil:
		s.TakenAt = *m.DateObs
	case m.Night != nil:
		s.TakenAt = *m.Night
	}
	return s
}

func (m measured) measures() Measures {
	return Measures{Median: fval(m.CalMedianADU), Spread: fval(m.CalSpreadADU), Noise: fval(m.CalNoiseADU)}
}

func camKey(s Setup) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return f(s.Gain) + "|" + f(s.Offset) + "|" + f(s.BinX)
}

type References struct {
	bias  map[string][]Measures
	clean map[string][]Sample
	gap   time.Duration
}

func NewReferences(gap time.Duration) *References {
	return &References{bias: map[string][]Measures{}, clean: map[string][]Sample{}, gap: gap}
}

func (r *References) AddBias(s Setup, m Measures) { r.bias[camKey(s)] = append(r.bias[camKey(s)], m) }

func (r *References) AddClean(s Setup, m Measures) {
	r.clean[camKey(s)] = append(r.clean[camKey(s)], Sample{Setup: s, Measures: m})
}

func (r *References) Judge(s Setup, m Measures) Verdict {
	b, ok := BiasReference(r.bias[camKey(s)])
	if !ok {
		return Judge(s, m, nil, nil)
	}
	if d, ok := DarkReference(s, b, r.clean[camKey(s)], r.gap); ok {
		return Judge(s, m, &b, &d)
	}
	return Judge(s, m, &b, nil)
}

func LoadReferences(ctx context.Context, db *gorm.DB, gap time.Duration) (*References, error) {
	refs, _, err := loadReferences(ctx, db, gap)
	return refs, err
}

func loadReferences(ctx context.Context, db *gorm.DB, gap time.Duration) (*References, []measured, error) {
	var rows []measured
	if err := db.WithContext(ctx).Model(&app.Frame{}).
		Select(`id, key, type, exposure, gain, "offset", bin_x, set_temp, ccd_temp, date_obs, night, cal_median_adu, cal_spread_adu, cal_noise_adu, cal_check`).
		Where("type IN ? AND index_error IS NULL AND cal_measured_at IS NOT NULL", []string{TypeDark, TypeBias}).
		Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("load measured calibration frames: %w", err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, tj := rows[i].setup().TakenAt, rows[j].setup().TakenAt
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return rows[i].ID < rows[j].ID
	})
	refs := NewReferences(gap)
	for _, r := range rows {
		switch {
		case r.Type == TypeBias:
			refs.AddBias(r.setup(), r.measures())
		case r.CalCheck != nil && *r.CalCheck == StateClean:
			refs.AddClean(r.setup(), r.measures())
		}
	}
	return refs, rows, nil
}

func JudgePending(ctx context.Context, db *gorm.DB, gap time.Duration) (clean, leak int, err error) {
	refs, rows, err := loadReferences(ctx, db, gap)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		if r.Type != TypeDark || (r.CalCheck != nil && *r.CalCheck != StateUnchecked) {
			continue
		}
		s, m := r.setup(), r.measures()
		v := refs.Judge(s, m)
		cols := map[string]any{"cal_check": v.State, "cal_check_reason": v.Reason}
		switch v.State {
		case StateLeak, StateOffTemp:
			cols["light_leak"] = m.Spread
			leak++
			slog.Warn("Dark rejected", "key", r.Key, "reason", v.Reason)
		case StateClean:
			cols["light_leak"] = nil
			refs.AddClean(s, m)
			clean++
		}
		if err := db.WithContext(ctx).Model(&app.Frame{}).Where("id = ?", r.ID).UpdateColumns(cols).Error; err != nil {
			return clean, leak, fmt.Errorf("save dark check for %s: %w", r.Key, err)
		}
	}
	return clean, leak, nil
}

func Run(ctx context.Context, db *gorm.DB, download Download, workers int, gap time.Duration) {
	if n, err := MeasurePending(ctx, db, download, workers); err != nil && ctx.Err() == nil {
		slog.Error("Measuring darks and bias frames failed", "error", err)
	} else if n > 0 {
		slog.Info("Measured darks and bias frames", "frames", n)
	}
	if ctx.Err() != nil {
		return
	}
	clean, leak, err := JudgePending(ctx, db, gap)
	if err != nil {
		slog.Error("Checking darks for light failed", "error", err)
		return
	}
	if clean+leak > 0 {
		slog.Info("Checked darks for light", "clean", clean, "leak", leak)
	}
}
