package conditions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
{"metric":{"__name__":"observatory_mount_tracking_enabled"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_camera_temperature_celsius"},"value":[1,"-3"]},
{"metric":{"__name__":"observatory_camera_sensor_width_pixels"},"value":[1,"6248"]},
{"metric":{"__name__":"observatory_camera_cooler_power_percent"},"value":[1,"NaN"]},
{"metric":{"__name__":"observatory_camera_offset"},"value":[1,"50"]},
{"metric":{"__name__":"observatory_camera_is_exposing"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_camera_at_target_temperature"},"value":[1,"0"]},
{"metric":{"__name__":"observatory_rotator_position_degrees"},"value":[1,"359.5"]},
{"metric":{"__name__":"observatory_flatdevice_connected"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_flatdevice_light_on"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_flatdevice_brightness"},"value":[1,"120"]},
{"metric":{"__name__":"observatory_flatdevice_brightness_max"},"value":[1,"255"]},
{"metric":{"__name__":"observatory_filterwheel_connected"},"value":[1,"1"]},
{"metric":{"__name__":"observatory_filterwheel_selected_filter_id"},"value":[1,"3"]},
{"metric":{"__name__":"observatory_image_exposure_time_seconds"},"value":[1,"2.5"]},
{"metric":{"__name__":"observatory_image_median_adu"},"value":[1,"21000"]},
{"metric":{"__name__":"observatory_image_mean_adu"},"value":[1,"21100.5"]},
{"metric":{"__name__":"observatory_image_stdev_adu"},"value":[1,"410"]},
{"metric":{"__name__":"observatory_image_max_adu"},"value":[1,"65535"]},
{"metric":{"__name__":"observatory_image_gain"},"value":[1,"0"]},
{"metric":{"__name__":"observatory_image_offset"},"value":[1,"50"]},
{"metric":{"__name__":"observatory_image_temperature_celsius"},"value":[1,"-10"]},
{"metric":{"__name__":"observatory_image_hfr"},"value":[1,"2.1"]}
]}}`

const upsBody = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"__name__":"network_ups_tools_battery_charge","ups":"observatory"},"value":[1,"97"]},
{"metric":{"__name__":"network_ups_tools_battery_runtime","ups":"observatory"},"value":[1,"4300"]},
{"metric":{"__name__":"network_ups_tools_battery_charge_low","ups":"observatory"},"value":[1,"40"]},
{"metric":{"__name__":"network_ups_tools_battery_runtime_low","ups":"observatory"},"value":[1,"300"]},
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
		case strings.HasPrefix(q, "max_over_time("):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1,"1791700000.5"]}]}}`))
		case strings.HasPrefix(q, "min_over_time("):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1,"1791478785"]}]}}`))
		case strings.Contains(q, "network_ups_tools"):
			_, _ = w.Write([]byte(upsBody))
		default:
			_, _ = w.Write([]byte(promBody))
		}
	}))
	defer srv.Close()
	s := New(Options{MetricsURL: srv.URL + "/"}, nil)
	r := s.Report(context.Background())
	if r.Weather.Source.Source != SourcePrometheus || r.Weather.Humidity == nil || *r.Weather.Humidity != 51 || r.Weather.Pressure != nil {
		t.Fatalf("weather %+v", r.Weather)
	}
	if r.Safety.Safe == nil || !*r.Safety.Safe || r.Mount.Tracking == nil || !*r.Mount.Tracking {
		t.Fatalf("safety %+v mount %+v", r.Safety, r.Mount)
	}
	if r.Camera.Source.Source != SourcePrometheus || r.Camera.Temperature == nil || *r.Camera.Temperature != -3 || r.Camera.SensorWidth == nil || *r.Camera.SensorWidth != 6248 || r.Camera.CoolerPower != nil {
		t.Fatalf("camera %+v", r.Camera)
	}
	if r.Rotator.Source.Source != SourcePrometheus || r.Rotator.Position == nil || *r.Rotator.Position != 359.5 {
		t.Fatalf("rotator %+v", r.Rotator)
	}
	checkCalibrationDevices(t, r)
	checkPower(t, r.Power)
	if r.Sync.Source.Source != SourceNone {
		t.Fatalf("sync %+v", r.Sync)
	}
}

func checkCalibrationDevices(t *testing.T, r Report) {
	t.Helper()
	c := r.Camera
	if c.Offset == nil || *c.Offset != 50 || c.IsExposing == nil || !*c.IsExposing || c.AtTarget == nil || *c.AtTarget {
		t.Fatalf("camera offset/exposing/at target %+v %+v %+v", c.Offset, c.IsExposing, c.AtTarget)
	}
	f := r.FlatDevice
	if f.Source.Source != SourcePrometheus || f.Connected == nil || !*f.Connected || f.LightOn == nil || !*f.LightOn ||
		f.Brightness == nil || *f.Brightness != 120 || f.BrightnessMax == nil || *f.BrightnessMax != 255 {
		t.Fatalf("flat device %+v", f)
	}
	w := r.FilterWheel
	if w.Source.Source != SourcePrometheus || w.Connected == nil || !*w.Connected || w.SelectedID == nil || *w.SelectedID != 3 {
		t.Fatalf("filter wheel %+v", w)
	}
	b, err := json.Marshal(r.LastImage)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"source":"prometheus","exposure":2.5,"median_adu":21000,"mean_adu":21100.5,"stdev_adu":410,"max_adu":65535,"gain":0,"offset":50,"temperature":-10}`; string(b) != want {
		t.Fatalf("last image %s, want %s", b, want)
	}
}

func TestMissingCalibrationDevicesAreNone(t *testing.T) {
	t.Parallel()
	var r Report
	fillObservatory(&r, []sample{
		{labels: map[string]string{"__name__": "observatory_camera_connected"}, value: 1},
		{labels: map[string]string{"__name__": "observatory_image_hfr"}, value: 2},
	})
	for name, src := range map[string]Source{"flat device": r.FlatDevice.Source, "filter wheel": r.FilterWheel.Source, "last image": r.LastImage.Source} {
		if src.Source != SourceNone || src.Error == "" {
			t.Errorf("%s source %+v, want none with an error", name, src)
		}
	}
	if r.Camera.Offset != nil || r.Camera.IsExposing != nil || r.Camera.AtTarget != nil {
		t.Errorf("camera fields not reported were filled: %+v", r.Camera)
	}
}

func checkPower(t *testing.T, p Power) {
	t.Helper()
	if p.Model != "CyberPower EC450G" || p.OnBattery == nil || !*p.OnBattery || p.LowBattery != nil || p.Charge == nil || *p.Charge != 97 || p.OnBatterySeconds == nil || *p.OnBatterySeconds != 95 {
		t.Fatalf("power %+v", p)
	}
	if p.RuntimeSeconds == nil || *p.RuntimeSeconds != 4300 {
		t.Fatalf("runtime %+v", p.RuntimeSeconds)
	}
	if p.ChargeLow == nil || *p.ChargeLow != 40 || p.RuntimeLowSeconds == nil || *p.RuntimeLowSeconds != 300 {
		t.Fatalf("low battery levels %+v %+v", p.ChargeLow, p.RuntimeLowSeconds)
	}
	if p.LastOnBattery == nil || !p.LastOnBattery.Equal(time.Unix(1791700000, 5e8)) || p.BatteryHistoryFrom == nil || p.BatteryHistoryFrom.Unix() != 1791478785 {
		t.Fatalf("battery history %+v %+v", p.LastOnBattery, p.BatteryHistoryFrom)
	}
}

func TestBatteryHistoryCachedAndEmpty(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.HasPrefix(q, "max_over_time(") || strings.HasPrefix(q, "min_over_time(") {
			calls.Add(1)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer srv.Close()
	s := New(Options{MetricsURL: srv.URL}, nil)
	now := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	for i := range 3 {
		h := s.batteryHistory(context.Background(), now.Add(time.Duration(i)*time.Minute))
		if h.last != nil || h.from != nil {
			t.Fatalf("history %+v", h)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("history queried %d times, want 2", calls.Load())
	}
	s.batteryHistory(context.Background(), now.Add(historyTTL))
	if calls.Load() != 4 {
		t.Fatalf("history not refreshed after its TTL: %d queries", calls.Load())
	}
}

func TestReportFromAnEndedRequestIsNotShared(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "network_ups_tools") {
			_, _ = w.Write([]byte(upsBody))
			return
		}
		_, _ = w.Write([]byte(promBody))
	}))
	defer srv.Close()
	s := New(Options{MetricsURL: srv.URL}, nil)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	s.Report(ended)
	if r := s.Report(context.Background()); r.Weather.Source.Source != SourcePrometheus {
		t.Fatalf("the next viewer got the ended request's report: %+v", r.Weather.Source)
	}
}

func TestNoMetricsURL(t *testing.T) {
	t.Parallel()
	r := New(Options{}, nil).Report(context.Background())
	for _, s := range []Source{r.Weather.Source, r.Safety.Source, r.Mount.Source, r.Power.Source, r.Camera.Source, r.Rotator.Source,
		r.FlatDevice.Source, r.FilterWheel.Source, r.LastImage.Source} {
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
	if r.Weather.Source.Source != SourceError || r.Power.Source.Source != SourceError || r.FlatDevice.Source.Source != SourceError || r.LastImage.Source.Source != SourceError {
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
	if s.Node != "home" || s.Errors == nil || *s.Errors != 1 || s.LastBatch == nil || !s.LastBatch.Equal(time.Date(2026, 10, 9, 11, 25, 30, 0, time.UTC)) {
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
	if _, ok := parseSymTime("2026-10-09 06:32:12", ""); ok {
		t.Fatal("no offset parsed as UTC")
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

func TestMoonMatchesUSNO(t *testing.T) {
	t.Parallel()
	const lat, lon = 31.546944, -99.382222
	near := func(got time.Time, want string, tol time.Duration) {
		t.Helper()
		w, _ := time.Parse(time.RFC3339, want)
		if d := got.Sub(w); d > tol || d < -tol {
			t.Errorf("got %v, want %v", got, w)
		}
	}
	m := Moon(lat, lon, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 23, 0, 0, 0, time.UTC))
	if len(m.Rises) != 1 {
		t.Fatalf("rises %v", m.Rises)
	}
	near(m.Rises[0], "2026-10-10T12:37:00Z", 4*time.Minute)
	near(m.NextNew, "2026-10-10T15:50:00Z", 30*time.Minute)
	near(m.NextFull, "2026-10-26T04:12:00Z", 30*time.Minute)
	if m.NextNew.Second() != 0 || m.NextFull.Second() != 0 {
		t.Errorf("phases not to the minute: %v %v", m.NextNew, m.NextFull)
	}
	m = Moon(lat, lon, time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 22, 30, 0, 0, time.UTC))
	if len(m.Sets) != 1 || len(m.Rises) != 1 {
		t.Fatalf("sets %v rises %v", m.Sets, m.Rises)
	}
	near(m.Sets[0], "2026-10-22T09:05:00Z", 4*time.Minute)
	near(m.Rises[0], "2026-10-22T21:53:00Z", 4*time.Minute)
	for _, c := range []struct {
		at   time.Time
		frac float64
	}{
		{time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), 0.22},
		{time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), 0.84},
		{time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), 0.85},
	} {
		got := Moon(lat, lon, c.at, c.at).Illumination
		if d := got - c.frac; d > 0.01 || d < -0.01 {
			t.Errorf("%v: illumination %.3f, want %.2f", c.at, got, c.frac)
		}
	}
}
