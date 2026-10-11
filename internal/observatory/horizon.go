package observatory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	pathHorizon         = "/os/v1/horizon"
	HorizonRefresh      = time.Hour
	HorizonRetry        = 10 * time.Minute
	horizonSaveDeadline = 5 * time.Second
)

var (
	ErrHorizonUnsupported = errors.New("the observatory scheduler is older than the horizon endpoint")
	ErrHorizonInvalid     = errors.New("the observatory scheduler reported an invalid horizon")
)

type HorizonResponse struct {
	Custom      bool      `json:"custom"`
	ProfileID   string    `json:"profile_id"`
	AzimuthStep float64   `json:"azimuth_step"`
	Altitudes   []float64 `json:"altitudes"`
}

func (c *Client) Horizon(ctx context.Context) (HorizonResponse, error) {
	var r HorizonResponse
	err := c.doWithin(ctx, RequestTimeout, http.MethodGet, pathHorizon, nil, &r)
	if errors.Is(err, ErrNotFound) {
		return r, ErrHorizonUnsupported
	}
	if err != nil {
		return r, err
	}
	if r.Custom && !(&mosaics.Horizon{Step: r.AzimuthStep, Altitudes: r.Altitudes}).Valid() {
		return r, fmt.Errorf("%w: step %v with %d altitudes", ErrHorizonInvalid, r.AzimuthStep, len(r.Altitudes))
	}
	return r, nil
}

// HorizonProvider keeps the last good custom horizon report from the
// observatory scheduler, refreshes it at most hourly, and keeps it in the app
// database so a restart while the observatory PC is offline still knows it.
type HorizonProvider struct {
	Client *Client
	DB     *gorm.DB
	Now    func() time.Time

	mu     sync.Mutex
	last   *mosaics.HorizonReport
	loaded bool
}

func (p *HorizonProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *HorizonProvider) Get(ctx context.Context) (mosaics.HorizonReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded && p.DB != nil {
		var row app.ObservatoryHorizon
		res := p.DB.WithContext(ctx).Order("fetched_at DESC").Limit(1).Find(&row)
		switch {
		case res.Error != nil:
			slog.Warn("Loading the saved observatory horizon failed", "error", res.Error)
		default:
			p.loaded = true
			if res.RowsAffected > 0 && p.last == nil {
				r := reportOf(row)
				p.last = &r
			}
		}
	}
	if p.last == nil {
		return mosaics.HorizonReport{}, false
	}
	return *p.last, true
}

func reportOf(row app.ObservatoryHorizon) mosaics.HorizonReport {
	r := mosaics.HorizonReport{ProfileID: row.ProfileID, Custom: row.Custom, FetchedAt: row.FetchedAt}
	if h := (&mosaics.Horizon{Step: row.AzimuthStep, Altitudes: row.Altitudes}); row.Custom && h.Valid() {
		r.Horizon = h
	}
	return r
}

// Refresh asks the observatory scheduler for its horizon once. On any error
// the last good report stays in place.
func (p *HorizonProvider) Refresh(ctx context.Context) error {
	res, err := p.Client.Horizon(ctx)
	if err != nil {
		return err
	}
	row := app.ObservatoryHorizon{ProfileID: res.ProfileID, Custom: res.Custom, FetchedAt: p.now().UTC()}
	if res.Custom {
		row.AzimuthStep, row.Altitudes = res.AzimuthStep, res.Altitudes
	}
	r := reportOf(row)
	p.mu.Lock()
	p.last, p.loaded = &r, true
	p.mu.Unlock()
	if p.DB != nil {
		sctx, cancel := context.WithTimeout(ctx, horizonSaveDeadline)
		defer cancel()
		if err := p.DB.WithContext(sctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("save horizon: %w", err)
		}
	}
	return nil
}

func (p *HorizonProvider) Run(ctx context.Context) {
	if !p.Client.Configured() {
		return
	}
	for {
		wait := HorizonRefresh
		err := p.Refresh(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrHorizonUnsupported):
			slog.Info("The observatory scheduler has no horizon endpoint yet; dark hours use the flat minimum altitude for custom-horizon projects")
		case err != nil:
			wait = HorizonRetry
			slog.Warn("Observatory horizon refresh failed; keeping the last good horizon", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
