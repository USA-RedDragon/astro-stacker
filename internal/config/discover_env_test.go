package config_test

import (
	"reflect"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	configulator "github.com/USA-RedDragon/configulator/v2"
)

const envFalse = "false"

func loadEnv(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	cfg, err := configulator.New(config.ConfigSchema()).
		WithEnvironmentVariables(&configulator.EnvironmentVariableOptions{Separator: "_"}).
		WithEnviron(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).
		LoadWithoutValidation()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestDiscoverDefaultsWithoutConfig(t *testing.T) {
	t.Parallel()
	d := loadEnv(t, map[string]string{}).Discover
	if d.SiteLatitude != 0 || d.SiteLongitude != 0 {
		t.Errorf("the site has a default: %v %v", d.SiteLatitude, d.SiteLongitude)
	}
	if d.MinAltitude != 30 || d.FocalLength != 405 || d.PixelSize != 3.76 || d.SensorWidth != 6248 || d.SensorHeight != 4176 {
		t.Errorf("rig defaults %+v", d)
	}
	if !d.Starfront || d.StarfrontURL != "https://collab.starfront.space" || d.StarfrontMinutes != 30 || d.SkyBrightness != 21.4 {
		t.Errorf("starfront defaults %+v", d)
	}
	if !reflect.DeepEqual(d.Filters, []string{"L", "R", "G", "B", "H", "O", "S"}) {
		t.Errorf("filters default %#v", d.Filters)
	}
}

func TestDiscoverEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"DISCOVER_SITE_LATITUDE":     "31.5",
		"DISCOVER_SITE_LONGITUDE":    "-99.4",
		"DISCOVER_SITE_ELEVATION":    "473",
		"DISCOVER_MIN_ALTITUDE":      "25",
		"DISCOVER_SKY_BRIGHTNESS":    "21.5",
		"DISCOVER_FOCAL_LENGTH":      "405",
		"DISCOVER_PIXEL_SIZE":        "3.76",
		"DISCOVER_SENSOR_WIDTH":      "6248",
		"DISCOVER_SENSOR_HEIGHT":     "4176",
		"DISCOVER_COLOUR":            envFalse,
		"DISCOVER_FILTERS":           "L,R,G,B,H=3,O=3,S=3",
		"DISCOVER_TYPICAL_HFR":       "2.4",
		"DISCOVER_TYPICAL_GUIDE_RMS": "0.6",
		"DISCOVER_EXPOSURES":         "H=600,O=600,S=600,L=300",
		"DISCOVER_STARFRONT":         envFalse,
		"DISCOVER_STARFRONT_URL":     "https://example.invalid",
		"DISCOVER_STARFRONT_MINUTES": "60",
	}
	d := loadEnv(t, env).Discover
	checks := map[string][2]any{
		"DISCOVER_SITE_LATITUDE":     {d.SiteLatitude, 31.5},
		"DISCOVER_SITE_LONGITUDE":    {d.SiteLongitude, -99.4},
		"DISCOVER_SITE_ELEVATION":    {d.SiteElevation, 473.0},
		"DISCOVER_MIN_ALTITUDE":      {d.MinAltitude, 25.0},
		"DISCOVER_SKY_BRIGHTNESS":    {d.SkyBrightness, 21.5},
		"DISCOVER_FOCAL_LENGTH":      {d.FocalLength, 405.0},
		"DISCOVER_PIXEL_SIZE":        {d.PixelSize, 3.76},
		"DISCOVER_SENSOR_WIDTH":      {d.SensorWidth, 6248},
		"DISCOVER_SENSOR_HEIGHT":     {d.SensorHeight, 4176},
		"DISCOVER_COLOUR":            {d.Colour, false},
		"DISCOVER_FILTERS":           {d.Filters, []string{"L", "R", "G", "B", "H=3", "O=3", "S=3"}},
		"DISCOVER_TYPICAL_HFR":       {d.TypicalHFR, 2.4},
		"DISCOVER_TYPICAL_GUIDE_RMS": {d.TypicalGuideRMS, 0.6},
		"DISCOVER_EXPOSURES":         {d.Exposures, []string{"H=600", "O=600", "S=600", "L=300"}},
		"DISCOVER_STARFRONT":         {d.Starfront, false},
		"DISCOVER_STARFRONT_URL":     {d.StarfrontURL, "https://example.invalid"},
		"DISCOVER_STARFRONT_MINUTES": {d.StarfrontMinutes, 60},
	}
	if len(checks) != len(env) {
		t.Fatalf("%d checks for %d variables", len(checks), len(env))
	}
	for k, c := range checks {
		if !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s: got %#v, want %#v", k, c[0], c[1])
		}
	}
}
