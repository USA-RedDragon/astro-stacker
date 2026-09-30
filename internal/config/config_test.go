package config_test

import (
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/configulator"
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

	defConfig, err := configulator.New[config.Config]().Default()
	if err != nil {
		t.Fatalf("failed to create default config: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

	defConfig, err := configulator.New[config.Config]().Default()
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
		{config.TSVerdictsOn, "2026-10-01", nil},
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
