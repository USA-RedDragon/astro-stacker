package config_test

import (
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	configulator "github.com/USA-RedDragon/configulator/v2"
)

func TestLogLevelConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		logLevel config.LogLevel
		valid    bool
	}{
		{"debug level", config.LogLevelDebug, true},
		{"info level", config.LogLevelInfo, true},
		{"warn level", config.LogLevelWarn, true},
		{"error level", config.LogLevelError, true},
		{"invalid level", "invalid", false},
	}

	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := defConfig
			cfg.LogLevel = tt.logLevel
			err := cfg.Validate()
			if tt.valid {
				if err != nil {
					t.Errorf("Validate() unexpected error = %v", err)
				}
			} else if !errors.Is(err, config.ErrInvalidLogLevel) {
				t.Errorf("Validate() error = %v, want %v", err, config.ErrInvalidLogLevel)
			}
		})
	}
}

func TestTSVerdicts(t *testing.T) {
	t.Parallel()

	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}
	if defConfig.Stacking.TSVerdicts != config.TSVerdictsOff {
		t.Errorf("ts-verdicts defaults to %q, want off", defConfig.Stacking.TSVerdicts)
	}
	if err := defConfig.Validate(); err != nil {
		t.Fatalf("default config: %v", err)
	}

	tests := []struct {
		mode, since string
		want        error
	}{
		{config.TSVerdictsOff, "", nil},
		{config.TSVerdictsDryRun, "", nil},
		{config.TSVerdictsOn, verdictsSince, nil},
		{"yes", "", config.ErrInvalidTSVerdicts},
		{"", "", config.ErrInvalidTSVerdicts},
		{config.TSVerdictsOn, "October", config.ErrInvalidTSVerdictsSince},
	}
	for _, tt := range tests {
		cfg := defConfig
		cfg.Stacking.TSVerdicts = tt.mode
		cfg.Stacking.TSVerdictsSince = tt.since
		if err := cfg.Validate(); !errors.Is(err, tt.want) {
			t.Errorf("mode %q since %q: Validate() = %v, want %v", tt.mode, tt.since, err, tt.want)
		}
	}
}

func TestRejectOffTempSince(t *testing.T) {
	t.Parallel()

	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}
	if defConfig.Stacking.RejectOffTempSince != "2026-10-13" {
		t.Errorf("reject-off-temp-since defaults to %q, want 2026-10-13", defConfig.Stacking.RejectOffTempSince)
	}
	if got, want := defConfig.Stacking.RejectOffTempSinceTime(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("RejectOffTempSinceTime() = %v, want %v", got, want)
	}
	for since, want := range map[string]error{"": nil, "2026-10-13": nil, "October": config.ErrInvalidRejectOffTempSince,
		"2026-10-13T00:00:00Z": config.ErrInvalidRejectOffTempSince} {
		cfg := defConfig
		cfg.Stacking.RejectOffTempSince = since
		if err := cfg.Validate(); !errors.Is(err, want) {
			t.Errorf("since %q: Validate() = %v, want %v", since, err, want)
		}
	}
	defConfig.Stacking.RejectOffTempSince = ""
	if !defConfig.Stacking.RejectOffTempSinceTime().IsZero() {
		t.Error("an empty reject-off-temp-since must turn the rule off")
	}
}

func TestGoals(t *testing.T) {
	t.Parallel()

	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}
	if defConfig.Goals.Enabled || defConfig.Goals.Publish != config.GoalsPublishOff ||
		defConfig.Goals.IntervalMinutes != 30 || defConfig.Goals.MaxSubs != 200 {
		t.Errorf("goals defaults %+v", defConfig.Goals)
	}
	for _, mode := range []string{config.GoalsPublishOff, config.GoalsPublishDryRun, config.GoalsPublishOn} {
		cfg := defConfig
		cfg.Goals.Publish = mode
		if err := cfg.Validate(); err != nil {
			t.Errorf("publish %q: %v", mode, err)
		}
	}
	if defConfig.Goals.DepthSwitch != config.GoalsPublishOn {
		t.Errorf("depth switch default %q", defConfig.Goals.DepthSwitch)
	}
	bad := defConfig
	bad.Goals.DepthSwitch = "always"
	if err := bad.Validate(); !errors.Is(err, config.ErrInvalidGoalsDepthSwitch) {
		t.Errorf("depth switch always: %v", err)
	}
	cfg := defConfig
	cfg.Goals.Publish = "yes"
	if err := cfg.Validate(); !errors.Is(err, config.ErrInvalidGoalsPublish) {
		t.Errorf("publish yes: %v", err)
	}
	cfg = defConfig
	cfg.Goals.Enabled = true
	cfg.Goals.IntervalMinutes = 0
	if err := cfg.Validate(); !errors.Is(err, config.ErrInvalidGoalsInterval) {
		t.Errorf("interval 0: %v", err)
	}
	cfg.Goals.IntervalMinutes = 30
	if err := cfg.Validate(); !errors.Is(err, config.ErrMissingS3Credentials) {
		t.Errorf("enabled without S3 credentials: %v", err)
	}
}

func TestCalibrationPublish(t *testing.T) {
	t.Parallel()
	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}
	if defConfig.Calibration.Publish != config.GoalsPublishOff {
		t.Errorf("calibration.publish default %q", defConfig.Calibration.Publish)
	}
	for _, mode := range []string{config.GoalsPublishOff, config.GoalsPublishDryRun, config.GoalsPublishOn} {
		cfg := defConfig
		cfg.Calibration.Publish = mode
		if err := cfg.Validate(); err != nil {
			t.Errorf("publish %q: %v", mode, err)
		}
	}
	bad := defConfig
	bad.Calibration.Publish = "always"
	if err := bad.Validate(); !errors.Is(err, config.ErrInvalidCalibrationPublish) {
		t.Errorf("publish yes: %v", err)
	}
}
func TestSubLength(t *testing.T) {
	t.Parallel()
	def, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatal(err)
	}
	if def.SubLength.Publish != config.GoalsPublishOff || def.SubLength.NewLengths || def.SubLength.IntervalMinutes != 60 || def.SubLength.TolerancePct != 2 || def.Stacking.ScorePerSecond {
		t.Errorf("defaults %+v, per second %v", def.SubLength, def.Stacking.ScorePerSecond)
	}
	for _, tt := range []struct {
		publish   string
		perSecond bool
		interval  int
		want      error
	}{
		{config.GoalsPublishOff, false, 60, nil},
		{config.GoalsPublishDryRun, false, 60, nil},
		{config.GoalsPublishOn, false, 60, config.ErrSubLengthNeedsPerSecond},
		{config.GoalsPublishOn, true, 60, nil},
		{"sometimes", true, 60, config.ErrInvalidSubLengthPublish},
		{config.GoalsPublishOff, false, 0, config.ErrInvalidSubLengthInterval},
	} {
		cfg := def
		cfg.SubLength.Publish, cfg.Stacking.ScorePerSecond, cfg.SubLength.IntervalMinutes = tt.publish, tt.perSecond, tt.interval
		if err := cfg.Validate(); !errors.Is(err, tt.want) {
			t.Errorf("%+v: Validate() = %v, want %v", tt, err, tt.want)
		}
	}
	for _, tol := range []float64{0, -1, 50} {
		cfg := def
		cfg.SubLength.TolerancePct = tol
		if err := cfg.Validate(); !errors.Is(err, config.ErrInvalidSubLengthTolerance) {
			t.Errorf("tolerance %v: %v", tol, err)
		}
	}
}

func TestSurveyMirror(t *testing.T) {
	t.Parallel()
	defConfig, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}
	d := defConfig.Discover
	if !d.SurveyMirror || d.SurveyBucket != "sky-survey" || d.SurveyPrefix != "hips/CDS_P_DSS2_color" || d.SurveyMaxOrder != 8 || d.SurveyCacheMB != 100 {
		t.Errorf("survey mirror defaults %+v", d)
	}
	if err := defConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		order, mb int
		want      error
	}{
		{-1, 100, config.ErrInvalidSurveyMaxOrder},
		{21, 100, config.ErrInvalidSurveyMaxOrder},
		{8, 0, config.ErrInvalidSurveyCache},
	} {
		cfg := defConfig
		cfg.Discover.SurveyMaxOrder, cfg.Discover.SurveyCacheMB = tt.order, tt.mb
		if err := cfg.Validate(); !errors.Is(err, tt.want) {
			t.Errorf("order %d cache %d: %v", tt.order, tt.mb, err)
		}
		cfg.Discover.SurveyMirror = false
		if err := cfg.Validate(); err != nil {
			t.Errorf("mirror off, order %d cache %d: %v", tt.order, tt.mb, err)
		}
	}
}
