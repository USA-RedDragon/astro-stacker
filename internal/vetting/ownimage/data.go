package ownimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// ErrNoGaia says DBData has no Gaia mirror to read stars from.
var ErrNoGaia = errors.New("no Gaia mirror")

// DBData reads subs from the app database, images from the processed
// bucket and stars from the Gaia mirror.
type DBData struct {
	App   *gorm.DB
	Store discoveries.ObjectStore
	Gaia  *starcat.Gaia
	// BelowCut also lists the subs left out of their stack on their score
	// that were registered for photometry, for measuring a star through a
	// dip watch or eclipse request rather than vetting.
	BelowCut bool
}

type subRow struct {
	FrameID  int
	Filter   string
	Night    time.Time
	DateObs  time.Time
	Exposure float64
	Weight   float64
	Key      string
	DarkKey  *string
	AddedAt  *time.Time
}

// Subs lists the target's added, registered subs as the discoveries
// runner does, with the master dark each was calibrated with.
func (d DBData) Subs(ctx context.Context, object string) ([]Sub, error) {
	var rows []subRow
	err := d.App.WithContext(ctx).Table("stack_frames sf").
		Select("f.id AS frame_id, s.filter, f.night, f.date_obs, f.exposure, sf.weight, sf.registered_key AS key, "+
			"COALESCE(cm.object_key, sf.dark_master) AS dark_key, sf.processed_at AS added_at").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Joins("LEFT JOIN calibration_masters cm ON cm.set_key = sf.dark_master").
		Where("sf.status IN ? AND sf.registered_key IS NOT NULL AND sf.weight > 0 AND f.night IS NOT NULL AND f.date_obs IS NOT NULL AND f.exposure > 0", d.statuses()).
		Where("s.object = ?", object).
		Order("f.date_obs").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load subs: %w", err)
	}
	out := make([]Sub, len(rows))
	for i, r := range rows {
		out[i] = Sub{FrameID: r.FrameID, Filter: r.Filter, Night: dateOnly(r.Night), Start: r.DateObs.UTC(), Exposure: r.Exposure,
			Weight: r.Weight, Key: r.Key, DarkKey: r.DarkKey}
		if r.AddedAt != nil {
			out[i].AddedAt = r.AddedAt.UTC()
		}
	}
	return out, nil
}

func (d DBData) statuses() []string {
	if d.BelowCut {
		return []string{app.StackStatusAdded, app.StackStatusLowScore}
	}
	return []string{app.StackStatusAdded}
}

// Load reads one registered sub.
func (d DBData) Load(ctx context.Context, key string) (*discoveries.Plane, error) {
	b, err := d.Store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", key, err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	return discoveries.PlaneOf(im), nil
}

// Get reads any object of the processed bucket.
func (d DBData) Get(ctx context.Context, key string) ([]byte, error) {
	return d.Store.Get(ctx, key)
}

// Frame is the plate solution of the target's registration reference and
// the size of its masters.
func (d DBData) Frame(ctx context.Context, object string) (*discoveries.WCS, int, int, error) {
	var tr app.TargetReference
	if err := d.App.WithContext(ctx).Where("object = ?", object).First(&tr).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("no registration reference: %w", err)
	}
	if tr.WCS == nil {
		return nil, 0, 0, errors.New("the registration reference has no plate solution")
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*tr.WCS), &cards); err != nil {
		return nil, 0, 0, fmt.Errorf("read the plate solution: %w", err)
	}
	kw := map[string]string{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	var st app.Stack
	if err := d.App.WithContext(ctx).Where("object = ? AND master_key IS NOT NULL AND width > 0", object).First(&st).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("no master: %w", err)
	}
	w, err := discoveries.ParseWCS(kw, st.Height)
	if err != nil {
		return nil, 0, 0, err
	}
	return w, st.Width, st.Height, nil
}

// Stars reads the Gaia mirror, with positions moved by proper motion to
// now.
func (d DBData) Stars(ctx context.Context, ra, dec, radius float64) ([]Star, error) {
	if d.Gaia == nil {
		return nil, ErrNoGaia
	}
	rows, err := d.Gaia.Cone(ctx, ra, dec, radius, nil)
	if err != nil {
		return nil, fmt.Errorf("read Gaia stars: %w", err)
	}
	return starsOf(rows, julianYear(time.Now())), nil
}

func julianYear(t time.Time) float64 {
	return 2000 + float64(t.Sub(time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)))/float64(365.25*24*time.Hour)
}

func starsOf(rows []starcat.GaiaStar, year float64) []Star {
	out := make([]Star, 0, len(rows))
	for _, r := range rows {
		ra, dec := r.RA, r.Dec
		if finite(r.PMRA) && finite(r.PMDec) {
			ra, dec = r.At(year)
		}
		out = append(out, Star{SourceID: r.SourceID, RA: ra, Dec: dec, G: float64(r.G),
			BPRP: float64(r.BP) - float64(r.RP), Variable: r.Variable == starcat.VariableVariable})
	}
	return out
}

// Masters names each filter's master of the target.
func (d DBData) Masters(ctx context.Context, object string) (map[string]string, error) {
	var rows []app.Stack
	if err := d.App.WithContext(ctx).Where("object = ? AND master_key IS NOT NULL", object).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load masters: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.MasterKey != nil && *r.MasterKey != "" {
			out[r.Filter] = *r.MasterKey
		}
	}
	return out, nil
}

var (
	_ Data    = DBData{}
	_ Masters = DBData{}
)
