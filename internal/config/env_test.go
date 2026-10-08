package config_test

import (
	"reflect"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/types"
	configulator "github.com/USA-RedDragon/configulator/v2"
)

const (
	envTrue       = "true"
	bindAll       = "[::]"
	verdictsSince = "2026-10-01"
)

// TestProductionEnv sets every variable the home-cluster deployment
// (apps/astro-processing/resources/astro-stacker/values.yaml) passes, under
// the same names, and checks each lands in its field. A renamed tag or
// section that silently drops one of them fails here.
func TestProductionEnv(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"STORAGE_TYPE":                 "postgres",
		"STORAGE_DSN_APP":              "host=db user=u password=p dbname=app port=5432 sslmode=disable",
		"STORAGE_DSN_SCHEDULERDB":      "host=db user=u password=p dbname=schedulerdb port=5432 sslmode=disable",
		"METRICS_ENABLED":              envTrue,
		"METRICS_BIND":                 bindAll,
		"METRICS_PORT":                 "9090",
		"PPROF_ENABLED":                envTrue,
		"PPROF_BIND":                   bindAll,
		"LOG_LEVEL":                    "debug",
		"HTTP_TRUSTED_PROXIES":         "172.17.0.0/16",
		"INDEXER_ENABLED":              envTrue,
		"S3_ENDPOINT":                  "minio.minio.svc:9000",
		"S3_USE_SSL":                   "false",
		"S3_ACCESS_KEY":                "access",
		"S3_SECRET_KEY":                "secret",
		"PREVIEWS_ENABLED":             envTrue,
		"PREVIEWS_CONCURRENCY":         "3",
		"STACKING_ENABLED":             envTrue,
		"STACKING_WORK_DIR":            "/tmp/stacking-test",
		"STACKING_SIRIL_THREADS":       "5",
		"STACKING_SIRIL_MEMORY":        "0.4",
		"STACKING_WORKERS":             "6",
		"STACKING_TS_VERDICTS":         "on",
		"STACKING_TS_VERDICTS_TARGETS": "M31,Cygnis Loop Panel 2",
		"STACKING_TS_VERDICTS_SINCE":   verdictsSince,
		"PUBLIC_FRAMES_ENABLED":        envTrue,
	}

	cfg, err := configulator.New(config.ConfigSchema()).
		WithEnvironmentVariables(&configulator.EnvironmentVariableOptions{Separator: "_"}).
		WithEnviron(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).
		LoadWithoutValidation()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	checks := []struct {
		env  string
		got  any
		want any
	}{
		{"STORAGE_TYPE", cfg.Storage.Type, types.StorageTypePostgres},
		{"STORAGE_DSN_APP", cfg.Storage.DSN.App, env["STORAGE_DSN_APP"]},
		{"STORAGE_DSN_SCHEDULERDB", cfg.Storage.DSN.SchedulerDB, env["STORAGE_DSN_SCHEDULERDB"]},
		{"METRICS_ENABLED", cfg.Metrics.Enabled, true},
		{"METRICS_BIND", cfg.Metrics.Bind, bindAll},
		{"METRICS_PORT", cfg.Metrics.Port, 9090},
		{"PPROF_ENABLED", cfg.PProf.Enabled, true},
		{"PPROF_BIND", cfg.PProf.Bind, bindAll},
		{"LOG_LEVEL", cfg.LogLevel, config.LogLevelDebug},
		{"HTTP_TRUSTED_PROXIES", cfg.HTTP.TrustedProxies, []string{"172.17.0.0/16"}},
		{"INDEXER_ENABLED", cfg.Indexer.Enabled, true},
		{"S3_ENDPOINT", cfg.S3.Endpoint, "minio.minio.svc:9000"},
		{"S3_USE_SSL", cfg.S3.UseSSL, false},
		{"S3_ACCESS_KEY", cfg.S3.AccessKey, "access"},
		{"S3_SECRET_KEY", cfg.S3.SecretKey, "secret"},
		{"PREVIEWS_ENABLED", cfg.Previews.Enabled, true},
		{"PREVIEWS_CONCURRENCY", cfg.Previews.Concurrency, 3},
		{"STACKING_ENABLED", cfg.Stacking.Enabled, true},
		{"STACKING_WORK_DIR", cfg.Stacking.WorkDir, "/tmp/stacking-test"},
		{"STACKING_SIRIL_THREADS", cfg.Stacking.SirilThreads, 5},
		{"STACKING_SIRIL_MEMORY", cfg.Stacking.SirilMemory, 0.4},
		{"STACKING_WORKERS", cfg.Stacking.Workers, 6},
		{"STACKING_TS_VERDICTS", cfg.Stacking.TSVerdicts, config.TSVerdictsOn},
		{"STACKING_TS_VERDICTS_TARGETS", cfg.Stacking.TSVerdictsTargets, []string{"M31", "Cygnis Loop Panel 2"}},
		{"STACKING_TS_VERDICTS_SINCE", cfg.Stacking.TSVerdictsSince, verdictsSince},
		{"PUBLIC_FRAMES_ENABLED", cfg.PublicFrames.Enabled, true},
	}
	if len(checks) != len(env) {
		t.Fatalf("%d checks for %d variables", len(checks), len(env))
	}
	for _, c := range checks {
		if _, ok := env[c.env]; !ok {
			t.Errorf("check for unset variable %s", c.env)
		}
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.env, c.got, c.want)
		}
	}

	// Untouched fields keep their defaults.
	if cfg.PublicFrames.IntervalSeconds != 60 || cfg.PublicFrames.MaxAgeDays != 14 {
		t.Errorf("public-frames defaults: %+v", cfg.PublicFrames)
	}

	// Everything but the DSNs (Validate opens them) validates.
	cfg.Storage.Type = types.StorageTypeSQLite
	cfg.Storage.DSN.App = ":memory:"
	cfg.Storage.DSN.SchedulerDB = ":memory:"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestV1SectionNamesIgnored pins the v1 name, built from the Go field
// name, as no longer read: PUBLICFRAMES_ENABLED must not turn frames on.
func TestV1SectionNamesIgnored(t *testing.T) {
	t.Parallel()

	env := map[string]string{"PUBLICFRAMES_ENABLED": envTrue}
	cfg, err := configulator.New(config.ConfigSchema()).
		WithEnvironmentVariables(&configulator.EnvironmentVariableOptions{Separator: "_"}).
		WithEnviron(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).
		LoadWithoutValidation()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PublicFrames.Enabled {
		t.Error("PUBLICFRAMES_ENABLED was read; the section is PUBLIC_FRAMES_")
	}
}
