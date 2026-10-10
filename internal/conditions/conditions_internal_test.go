package conditions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const promBody = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"__name__":"observatory_weather_connected"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_weather_cloud_cover_percent"},"value":[1,"0"]},
{"metric":{"__name__":"observatory_weather_humidity_percent"},"value":[1,"51"]},
{"metric":{"__name__":"observatory_weather_pressure_mbar"},"value":[1,"NaN"]},
{"metric":{"__name__":"observatory_safetymonitor_is_safe"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_mount_tracking_enabled"},"value":[1,"1"]}
]}}`

const upsBody = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"__name__":"network_ups_tools_battery_charge","ups":"observatory"},"value":[1,"97"]},
{"metric":{"__name__":"network_ups_tools_device_info","mfr":"CPS","model":"EC450G","ups":"observatory"},"value":[1,"1"]},
{"metric":{"__name__":"network_ups_tools_ups_status","flag":"OB","ups":"observatory"},"value":[1,"1"]},
{"metric":{"__name__":"network_ups_tools_ups_status","flag":"OL","ups":"observatory"},"value":[1,"0"]}
]}}`

func TestReportFromPrometheus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(q, "time()"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1,"95"]}]}}`))
		case strings.Contains(q, "network_ups_tools"):
			_, _ = w.Write([]byte(upsBody))
		default:
			_, _ = w.Write([]byte(promBody))
		}
	}))
	defer srv.Close()
	s := New(Options{MetricsURL: srv.URL + "/", ShutdownSeconds: 60}, nil)
	r := s.Report(context.Background())
	if r.Weather.Source.Source != SourcePrometheus || r.Weather.Humidity == nil || *r.Weather.Humidity != 51 || r.Weather.Pressure != nil {
		t.Fatalf("weather %+v", r.Weather)
	}
	if r.Safety.Safe == nil || !*r.Safety.Safe || r.Mount.Tracking == nil || !*r.Mount.Tracking {
		t.Fatalf("safety %+v mount %+v", r.Safety, r.Mount)
	}
	p := r.Power
	if p.Model != "CyberPower EC450G" || !p.OnBattery || p.Charge == nil || *p.Charge != 97 || p.OnBatterySeconds == nil || *p.OnBatterySeconds != 95 || p.ShutdownSeconds != 60 {
		t.Fatalf("power %+v", p)
	}
	if r.Sync.Source.Source != SourceNone {
		t.Fatalf("sync %+v", r.Sync)
	}
}

func TestNoMetricsURL(t *testing.T) {
	t.Parallel()
	r := New(Options{}, nil).Report(context.Background())
	for _, s := range []Source{r.Weather.Source, r.Safety.Source, r.Mount.Source, r.Power.Source} {
		if s.Source != SourceNone {
			t.Fatalf("want none, got %+v", s)
		}
	}
}

func TestMetricsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	r := New(Options{MetricsURL: srv.URL}, nil).Report(context.Background())
	if r.Weather.Source.Source != SourceError || r.Power.Source.Source != SourceError {
		t.Fatalf("want error, got %+v %+v", r.Weather.Source, r.Power.Source)
	}
}

func TestReadSym(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE sym_node_identity (node_id VARCHAR)",
		"CREATE TABLE sym_node_host (node_id VARCHAR, host_name VARCHAR, heartbeat_time TIMESTAMP, timezone_offset VARCHAR)",
		"CREATE TABLE sym_incoming_batch (batch_id INTEGER, node_id VARCHAR, status VARCHAR, create_time TIMESTAMP)",
		"INSERT INTO sym_node_identity VALUES ('observatory')",
		"INSERT INTO sym_node_host VALUES ('observatory', 'OBS', '2026-10-09 06:32:12.000', '-05:00')",
		"INSERT INTO sym_node_host VALUES ('home', 'a', '2026-04-14 00:12:22.526', '+00:00')",
		"INSERT INTO sym_node_host VALUES ('home', 'b', '2026-10-09 11:24:59.328', '+00:00')",
		"INSERT INTO sym_incoming_batch VALUES (1, 'home', 'OK', '2026-10-09 06:25:30.000')",
		"INSERT INTO sym_incoming_batch VALUES (2, 'home', 'ER', '2026-10-09 06:20:00.000')",
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 9, 11, 26, 0, 0, time.UTC)
	s, err := readSym(context.Background(), db, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Node != "home" || s.Errors != 1 || s.LastBatch == nil || !s.LastBatch.Equal(time.Date(2026, 10, 9, 11, 25, 30, 0, time.UTC)) {
		t.Fatalf("sync %+v", s)
	}
	if s.LagSeconds == nil || *s.LagSeconds != 30 {
		t.Fatalf("lag %v", s.LagSeconds)
	}
	empty, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if _, err := readSym(context.Background(), empty, now); !errors.Is(err, ErrNoSym) {
		t.Fatalf("want ErrNoSym, got %v", err)
	}
}

func TestParseSymTime(t *testing.T) {
	t.Parallel()
	got, ok := parseSymTime("2026-10-09 06:32:12", "-05:00")
	if !ok || !got.Equal(time.Date(2026, 10, 9, 11, 32, 12, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := parseSymTime("", "+00:00"); ok {
		t.Fatal("empty parsed")
	}
}

func TestMoon(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)
	m := Moon(33, -97, start, start.Add(14*time.Hour))
	if len(m.Samples) != 85 {
		t.Fatalf("samples %d", len(m.Samples))
	}
	if m.Illumination > 0.05 {
		t.Fatalf("illumination %v near new moon", m.Illumination)
	}
	if m.NextNew.IsZero() || m.NextNew.Sub(start) > 3*24*time.Hour || m.NextFull.Before(m.NextNew) {
		t.Fatalf("phases new %v full %v", m.NextNew, m.NextFull)
	}
	if len(m.Sets) != 1 {
		t.Fatalf("sets %v rises %v", m.Sets, m.Rises)
	}
	if Moon(33, -97, start, start.Add(72*time.Hour)).Samples[0].T != start {
		t.Fatal("start moved")
	}
}
