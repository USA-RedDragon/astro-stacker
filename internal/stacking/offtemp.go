package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// LightTempToleranceC is how far, in °C, a light's sensor temperature at the
// end of its exposure (CCD-TEMP) may be from the camera's setpoint (SET-TEMP)
// for the light to be stacked. A camera that reconnects mid-night takes
// lights while it cools back down, with more dark current than the dark
// matched to the setpoint takes out. A light without either reading is
// stacked.
const LightTempToleranceC = 0.5

// offTemp reports whether a light's sensor was more than LightTempToleranceC
// off its setpoint, and if so why, in words. The difference is rounded to
// 0.01 °C, so a light exactly LightTempToleranceC off is not left out for
// the rounding of its readings.
func offTemp(f app.Frame) (string, bool) {
	if f.CCDTemp == nil || f.SetTemp == nil {
		return "", false
	}
	diff := math.Round(math.Abs(*f.CCDTemp-*f.SetTemp)*100) / 100
	if math.IsNaN(diff) || diff <= LightTempToleranceC {
		return "", false
	}
	return fmt.Sprintf("the sensor was at %s °C, %s °C off its %s °C setpoint; lights more than %s °C off it are left out",
		celsius(*f.CCDTemp), celsius(diff), celsius(*f.SetTemp), celsius(LightTempToleranceC)), true
}

func celsius(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// takenSince reports whether a light was taken on or after since: its
// DATE-OBS, or its night when it has none. A light of unknown date, or a zero
// since, is not.
func takenSince(f app.Frame, since time.Time) bool {
	switch {
	case since.IsZero():
		return false
	case f.DateObs != nil:
		return !f.DateObs.Before(since)
	case f.Night != nil:
		return !f.Night.Before(since)
	}
	return false
}

// withTemp is a light's status once the temperature rule is applied: a light
// taken on or after since whose sensor was off its setpoint (offTemp) is left
// out whatever classify decided, but for a reject in Target Scheduler or by
// hand, which stands. Older lights are stacked as before.
func withTemp(status string, f app.Frame, since time.Time) (string, string) {
	if status == app.StackStatusRejected || !takenSince(f, since) {
		return status, ""
	}
	if why, off := offTemp(f); off {
		return app.StackStatusOffTemp, why
	}
	return status, ""
}

// dropOffTemp takes stacked lights taken on or after RejectOffTempSince whose
// sensor was off its setpoint out of their masters, and marks the masters to
// be stacked again, which the moon sweep does. Older lights stay in.
func (p *Pipeline) dropOffTemp(ctx context.Context) error {
	since := p.opts.RejectOffTempSince
	if since.IsZero() {
		return nil
	}
	var rows []struct {
		SfID    int  `gorm:"column:sf_id"`
		SfStack *int `gorm:"column:sf_stack"`
		app.Frame
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.id AS sf_id, sf.stack_id AS sf_stack, f.ccd_temp, f.set_temp, f.date_obs, f.night").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status IN ? AND f.ccd_temp IS NOT NULL AND f.set_temp IS NOT NULL AND ABS(f.ccd_temp - f.set_temp) > ?",
			stackedStatuses(), LightTempToleranceC).
		Scan(&rows).Error; err != nil {
		return err
	}
	why := map[int]string{}
	stacks := map[int]bool{}
	for _, r := range rows {
		msg, off := offTemp(r.Frame)
		if !off || !takenSince(r.Frame, since) {
			continue
		}
		why[r.SfID] = msg
		if r.SfStack != nil {
			stacks[*r.SfStack] = true
		}
	}
	if len(why) == 0 {
		return nil
	}
	ids := make([]int, 0, len(stacks))
	for id := range stacks {
		ids = append(ids, id)
	}
	if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for id, msg := range why {
			if err := tx.Model(&app.StackFrame{}).Where("id = ? AND status IN ?", id, stackedStatuses()).
				UpdateColumns(map[string]any{columnStatus: app.StackStatusOffTemp, columnNextAttemptAt: nil, columnError: msg}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&app.Stack{}).Where("id IN ?", ids).UpdateColumn("needs_rebuild", true).Error
	}); err != nil {
		return err
	}
	slog.Info("Taking lights shot off their sensor setpoint out of their masters", "lights", len(why), "masters", len(stacks))
	return nil
}
