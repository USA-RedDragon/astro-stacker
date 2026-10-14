package stacking

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
