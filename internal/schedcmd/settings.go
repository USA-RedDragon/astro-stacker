package schedcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	FieldDitherEvery           = "ditherevery"
	FieldFilterSwitchFrequency = "filterswitchfrequency"
	FieldFlatsHandling         = "flatshandling"
	FieldSmartExposureOrder    = "smartexposureorder"
	FieldMeridianWindow        = "meridianwindow"
	FieldUseCustomHorizon      = "usecustomhorizon"
	FieldHorizonOffset         = "horizonoffset"
	FieldMaximumAltitude       = "maximumaltitude"
	FieldEnableGrader          = "enablegrader"
)

const (
	FlatsOff              = 0
	FlatsTargetCompletion = 100
	FlatsUseWithImmediate = 200
)

type ProjectSettings struct {
	DitherEvery           *int     `json:"ditherevery,omitempty"`
	FilterSwitchFrequency *int     `json:"filterswitchfrequency,omitempty"`
	FlatsHandling         *int     `json:"flatshandling,omitempty"`
	SmartExposureOrder    *bool    `json:"smartexposureorder,omitempty"`
	MeridianWindow        *int     `json:"meridianwindow,omitempty"`
	UseCustomHorizon      *bool    `json:"usecustomhorizon,omitempty"`
	HorizonOffset         *float64 `json:"horizonoffset,omitempty"`
	MaximumAltitude       *float64 `json:"maximumaltitude,omitempty"`
	EnableGrader          *bool    `json:"enablegrader,omitempty"`
}

func intRange(name string, lo, hi int) func(int) error {
	return func(v int) error {
		if v < lo || v > hi {
			return fmt.Errorf("%w: %s must be from %d to %d", ErrInvalid, name, lo, hi)
		}
		return nil
	}
}

func floatRange(name string, lo, hi float64) func(float64) error {
	return func(v float64) error {
		if v < lo || v > hi {
			return fmt.Errorf("%w: %s must be from %s to %s", ErrInvalid, name, fnum(lo), fnum(hi))
		}
		return nil
	}
}

func CheckDitherEvery(v int) error { return intRange("dither every", 0, 999)(v) }

func CheckFilterSwitchFrequency(v int) error {
	return intRange("filter switch frequency", 0, 999)(v)
}

func CheckFlatsHandling(v int) error {
	if (v >= 0 && v <= FlatsTargetCompletion) || v == FlatsUseWithImmediate {
		return nil
	}
	return fmt.Errorf("%w: flats handling must be 0 to 99 days, 100 (target completion) or 200 (use with immediate)", ErrInvalid)
}

func CheckMeridianWindow(v int) error { return intRange("meridian window", 0, 720)(v) }

func CheckHorizonOffset(v float64) error { return floatRange("horizon offset", 0, 90)(v) }

func CheckMaximumAltitude(v float64) error { return floatRange("maximum altitude", 0, 90)(v) }

func (s ProjectSettings) Validate(minimumAltitude float64) error {
	ints := []struct {
		v     *int
		check func(int) error
	}{
		{s.DitherEvery, CheckDitherEvery},
		{s.FilterSwitchFrequency, CheckFilterSwitchFrequency},
		{s.FlatsHandling, CheckFlatsHandling},
		{s.MeridianWindow, CheckMeridianWindow},
	}
	for _, f := range ints {
		if f.v != nil {
			if err := f.check(*f.v); err != nil {
				return err
			}
		}
	}
	if s.HorizonOffset != nil {
		if err := CheckHorizonOffset(*s.HorizonOffset); err != nil {
			return err
		}
	}
	if s.MaximumAltitude != nil {
		if err := CheckMaximumAltitude(*s.MaximumAltitude); err != nil {
			return err
		}
		if *s.MaximumAltitude > 0 && *s.MaximumAltitude <= minimumAltitude {
			return fmt.Errorf("%w: maximum altitude must be above the minimum altitude", ErrInvalid)
		}
	}
	return nil
}

func isNull(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func IntCheck(check func(int) error) func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		var n int
		if isNull(v) || json.Unmarshal(v, &n) != nil {
			return fmt.Errorf("%w: %s is not a whole number", ErrInvalid, string(v))
		}
		if check == nil {
			return nil
		}
		return check(n)
	}
}

func FloatCheck(check func(float64) error) func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		var n float64
		if isNull(v) || json.Unmarshal(v, &n) != nil {
			return fmt.Errorf("%w: %s is not a number", ErrInvalid, string(v))
		}
		if check == nil {
			return nil
		}
		return check(n)
	}
}

func BoolCheck() func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		var b bool
		if isNull(v) || json.Unmarshal(v, &b) != nil {
			return fmt.Errorf("%w: %s is not true or false", ErrInvalid, string(v))
		}
		return nil
	}
}

func FlatsText(v int) string {
	switch v {
	case FlatsOff:
		return "Off"
	case FlatsTargetCompletion:
		return "Target completion"
	case FlatsUseWithImmediate:
		return "Use with immediate"
	case 1:
		return "Every 1 day"
	default:
		return fmt.Sprintf("Every %d days", v)
	}
}

func FlatsFormat() func(json.RawMessage) string {
	return func(v json.RawMessage) string {
		var n int
		if err := json.Unmarshal(v, &n); err == nil && !isNull(v) {
			return FlatsText(n)
		}
		return string(v)
	}
}

func OffOrSuffixFormat(suffix string) func(json.RawMessage) string {
	return func(v json.RawMessage) string {
		var n float64
		if err := json.Unmarshal(v, &n); err == nil && !isNull(v) && n == 0 {
			return "Off"
		}
		return string(v) + suffix
	}
}

func projectSettingFields() map[string]FieldSpec {
	onOff := BoolFormat("On", "Off")
	return map[string]FieldSpec{
		FieldDitherEvery:           {Label: "Dither every", Check: IntCheck(CheckDitherEvery)},
		FieldFilterSwitchFrequency: {Label: "Filter switch frequency", Check: IntCheck(CheckFilterSwitchFrequency)},
		FieldFlatsHandling:         {Label: "Flats", Format: FlatsFormat(), Check: IntCheck(CheckFlatsHandling)},
		FieldSmartExposureOrder:    {Label: "Smart exposure order", Format: onOff, Check: BoolCheck()},
		FieldMeridianWindow:        {Label: "Meridian window", Format: SuffixFormat(" min"), Check: IntCheck(CheckMeridianWindow)},
		FieldUseCustomHorizon:      {Label: "Custom horizon", Format: onOff, Check: BoolCheck()},
		FieldHorizonOffset:         {Label: "Horizon offset", Format: SuffixFormat("°"), Check: FloatCheck(CheckHorizonOffset)},
		FieldMaximumAltitude:       {Label: "Maximum altitude", Format: OffOrSuffixFormat("°"), Check: FloatCheck(CheckMaximumAltitude)},
		FieldEnableGrader:          {Label: "Grader", Format: onOff, Check: BoolCheck()},
	}
}

func projectSettingsNote(s ProjectSettings) string {
	var parts []string
	if s.DitherEvery != nil {
		if *s.DitherEvery == 0 {
			parts = append(parts, "dither off")
		} else {
			parts = append(parts, fmt.Sprintf("dither every %d", *s.DitherEvery))
		}
	}
	if s.FilterSwitchFrequency != nil {
		if *s.FilterSwitchFrequency == 0 {
			parts = append(parts, "filter switch off")
		} else {
			parts = append(parts, fmt.Sprintf("filter switch every %d", *s.FilterSwitchFrequency))
		}
	}
	if s.FlatsHandling != nil {
		parts = append(parts, "flats "+strings.ToLower(FlatsText(*s.FlatsHandling)))
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, " · ")
	return strings.ToUpper(out[:1]) + out[1:] + "."
}
