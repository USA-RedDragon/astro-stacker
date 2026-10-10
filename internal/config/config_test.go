package config_test

import (
	"errors"
	"testing"

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
