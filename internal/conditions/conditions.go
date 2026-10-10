package conditions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	SourceNone       = "none"
	SourceError      = "error"
	SourcePrometheus = "prometheus"
	SourceSymmetric  = "symmetricds"

	cacheTTL     = 15 * time.Second
	queryTimeout = 5 * time.Second
	mConnected   = "connected"
)

type Source struct {
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
}

type Weather struct {
	Source
	Connected      *bool    `json:"connected,omitempty"`
	CloudCover     *float64 `json:"cloud_cover,omitempty"`
	RainRate       *float64 `json:"rain_rate,omitempty"`
	WindSpeed      *float64 `json:"wind_speed,omitempty"`
	WindGust       *float64 `json:"wind_gust,omitempty"`
	Humidity       *float64 `json:"humidity,omitempty"`
	DewPoint       *float64 `json:"dew_point,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	SkyTemperature *float64 `json:"sky_temperature,omitempty"`
	SkyBrightness  *float64 `json:"sky_brightness,omitempty"`
	Pressure       *float64 `json:"pressure,omitempty"`
}

type Safety struct {
	Source
	Connected *bool `json:"connected,omitempty"`
	Safe      *bool `json:"safe,omitempty"`
}

type Mount struct {
	Source
	Connected *bool    `json:"connected,omitempty"`
	Tracking  *bool    `json:"tracking,omitempty"`
	Parked    *bool    `json:"parked,omitempty"`
	Slewing   *bool    `json:"slewing,omitempty"`
	AtHome    *bool    `json:"at_home,omitempty"`
	Altitude  *float64 `json:"altitude,omitempty"`
	FlipHours *float64 `json:"flip_hours,omitempty"`
}

type Camera struct {
	Source
	Connected         *bool    `json:"connected,omitempty"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TargetTemperature *float64 `json:"target_temperature,omitempty"`
	CoolerOn          *bool    `json:"cooler_on,omitempty"`
	CoolerPower       *float64 `json:"cooler_power,omitempty"`
	SensorWidth       *float64 `json:"sensor_width,omitempty"`
	SensorHeight      *float64 `json:"sensor_height,omitempty"`
	BinX              *float64 `json:"bin_x,omitempty"`
	BinY              *float64 `json:"bin_y,omitempty"`
	Gain              *float64 `json:"gain,omitempty"`
	PixelSize         *float64 `json:"pixel_size,omitempty"`
}

type Rotator struct {
	Source
	Connected  *bool    `json:"connected,omitempty"`
	Position   *float64 `json:"position,omitempty"`
	Mechanical *float64 `json:"mechanical_position,omitempty"`
}

type Power struct {
	Source
	Model            string   `json:"model,omitempty"`
	Charge           *float64 `json:"charge,omitempty"`
	InputVoltage     *float64 `json:"input_voltage,omitempty"`
	RuntimeSeconds   *float64 `json:"runtime_seconds,omitempty"`
	Flags            []string `json:"flags,omitempty"`
	OnBattery        *bool    `json:"on_battery,omitempty"`
	LowBattery       *bool    `json:"low_battery,omitempty"`
	OnBatterySeconds *float64 `json:"on_battery_seconds,omitempty"`
}

type Sync struct {
	Source
	Node       string     `json:"node,omitempty"`
	Heartbeat  *time.Time `json:"heartbeat,omitempty"`
	LastBatch  *time.Time `json:"last_batch,omitempty"`
	LagSeconds *float64   `json:"lag_seconds,omitempty"`
	Errors     *int       `json:"errors,omitempty"`
}

type Report struct {
	At      time.Time `json:"at"`
	Weather Weather   `json:"weather"`
	Safety  Safety    `json:"safety"`
	Mount   Mount     `json:"mount"`
	Camera  Camera    `json:"camera"`
	Rotator Rotator   `json:"rotator"`
	Power   Power     `json:"power"`
	Sync    Sync      `json:"sync"`
}

type Options struct {
	MetricsURL string
	UPS        string
}

type Service struct {
	opts   Options
	client *http.Client
	sched  *gorm.DB
	now    func() time.Time

	mu     sync.Mutex
	cached *Report
}

func New(opts Options, sched *gorm.DB) *Service {
	if opts.UPS == "" {
		opts.UPS = "observatory"
	}
	opts.MetricsURL = strings.TrimRight(opts.MetricsURL, "/")
	return &Service{opts: opts, client: &http.Client{Timeout: queryTimeout}, sched: sched, now: time.Now}
}

func (s *Service) Report(ctx context.Context) Report {
	now := s.now()
	s.mu.Lock()
	if s.cached != nil && now.Sub(s.cached.At) < cacheTTL {
		r := *s.cached
		s.mu.Unlock()
		return r
	}
	s.mu.Unlock()
	r := Report{At: now}
	s.fillMetrics(ctx, &r)
	r.Sync = s.readSync(ctx, now)
	s.mu.Lock()
	s.cached = &r
	s.mu.Unlock()
	return r
}

type sample struct {
	labels map[string]string
	value  float64
}

func (s *Service) query(ctx context.Context, q string) ([]sample, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.MetricsURL+"/api/v1/query?query="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("metrics query answered %s", resp.Status)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("metrics query failed: %s", out.Error)
	}
	res := make([]sample, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		str, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			continue
		}
		res = append(res, sample{labels: r.Metric, value: v})
	}
	return res, nil
}

func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func flag(v float64) *bool {
	if math.IsNaN(v) {
		return nil
	}
	b := v != 0
	return &b
}

func manufacturer(m string) string {
	switch strings.ToUpper(m) {
	case "CPS":
		return "CyberPower"
	case "":
		return ""
	}
	return m
}

func (s *Service) fillMetrics(ctx context.Context, r *Report) {
	if s.opts.MetricsURL == "" {
		none := Source{Source: SourceNone}
		r.Weather.Source, r.Safety.Source, r.Mount.Source, r.Power.Source = none, none, none, none
		r.Camera.Source, r.Rotator.Source = none, none
		return
	}
	obs, err := s.query(ctx, `{__name__=~"observatory_(weather|safetymonitor|mount|camera|rotator)_.+"}`)
	if err != nil {
		e := Source{Source: SourceError, Error: err.Error()}
		r.Weather.Source, r.Safety.Source, r.Mount.Source = e, e, e
		r.Camera.Source, r.Rotator.Source = e, e
	} else {
		fillObservatory(r, obs)
	}
	ups, err := s.query(ctx, fmt.Sprintf(`{__name__=~"network_ups_tools_.+",ups=%q}`, s.opts.UPS))
	if err != nil {
		r.Power.Source = Source{Source: SourceError, Error: err.Error()}
		return
	}
	fillPower(&r.Power, ups)
	if r.Power.OnBattery != nil && *r.Power.OnBattery {
		q := fmt.Sprintf(`time() - max_over_time(timestamp(network_ups_tools_ups_status{flag="OB",ups=%q} == 0)[6h:30s])`, s.opts.UPS)
		if v, err := s.query(ctx, q); err == nil && len(v) > 0 {
			r.Power.OnBatterySeconds = finite(v[0].value)
		}
	}
}

func fillObservatory(r *Report, samples []sample) {
	found := map[string]bool{}
	for _, sm := range samples {
		name := sm.labels["__name__"]
		v := sm.value
		switch {
		case strings.HasPrefix(name, "observatory_weather_"):
			found["weather"] = true
			w := &r.Weather
			switch strings.TrimPrefix(name, "observatory_weather_") {
			case mConnected:
				w.Connected = flag(v)
			case "cloud_cover_percent":
				w.CloudCover = finite(v)
			case "rain_rate":
				w.RainRate = finite(v)
			case "wind_speed_mps":
				w.WindSpeed = finite(v)
			case "wind_gust_mps":
				w.WindGust = finite(v)
			case "humidity_percent":
				w.Humidity = finite(v)
			case "dew_point_celsius":
				w.DewPoint = finite(v)
			case "temperature_celsius":
				w.Temperature = finite(v)
			case "sky_temperature_celsius":
				w.SkyTemperature = finite(v)
			case "sky_brightness":
				w.SkyBrightness = finite(v)
			case "pressure_mbar":
				w.Pressure = finite(v)
			}
		case strings.HasPrefix(name, "observatory_safetymonitor_"):
			found["safety"] = true
			switch strings.TrimPrefix(name, "observatory_safetymonitor_") {
			case mConnected:
				r.Safety.Connected = flag(v)
			case "is_safe":
				r.Safety.Safe = flag(v)
			}
		case strings.HasPrefix(name, "observatory_mount_"):
			found["mount"] = true
			m := &r.Mount
			switch strings.TrimPrefix(name, "observatory_mount_") {
			case mConnected:
				m.Connected = flag(v)
			case "tracking_enabled":
				m.Tracking = flag(v)
			case "at_park":
				m.Parked = flag(v)
			case "slewing":
				m.Slewing = flag(v)
			case "at_home":
				m.AtHome = flag(v)
			case "altitude_degrees":
				m.Altitude = finite(v)
			case "time_to_meridian_flip_hours":
				m.FlipHours = finite(v)
			}
		case strings.HasPrefix(name, "observatory_camera_"):
			found["camera"] = true
			fillCamera(&r.Camera, strings.TrimPrefix(name, "observatory_camera_"), v)
		case strings.HasPrefix(name, "observatory_rotator_"):
			found["rotator"] = true
			fillRotator(&r.Rotator, strings.TrimPrefix(name, "observatory_rotator_"), v)
		}
	}
	state := func(k, what string) Source {
		if found[k] {
			return Source{Source: SourcePrometheus}
		}
		return Source{Source: SourceNone, Error: "no " + what + " metrics in the metrics store"}
	}
	r.Weather.Source, r.Safety.Source, r.Mount.Source = state("weather", "weather"), state("safety", "safety monitor"), state("mount", "mount")
	r.Camera.Source, r.Rotator.Source = state("camera", "camera"), state("rotator", "rotator")
}

func fillRotator(rt *Rotator, metric string, v float64) {
	switch metric {
	case mConnected:
		rt.Connected = flag(v)
	case "position_degrees":
		rt.Position = finite(v)
	case "mechanical_position_degrees":
		rt.Mechanical = finite(v)
	}
}

func fillCamera(c *Camera, metric string, v float64) {
	switch metric {
	case mConnected:
		c.Connected = flag(v)
	case "temperature_celsius":
		c.Temperature = finite(v)
	case "target_temperature_celsius":
		c.TargetTemperature = finite(v)
	case "cooler_on":
		c.CoolerOn = flag(v)
	case "cooler_power_percent":
		c.CoolerPower = finite(v)
	case "sensor_width_pixels":
		c.SensorWidth = finite(v)
	case "sensor_height_pixels":
		c.SensorHeight = finite(v)
	case "binning_x":
		c.BinX = finite(v)
	case "binning_y":
		c.BinY = finite(v)
	case "gain":
		c.Gain = finite(v)
	case "pixel_size_microns":
		c.PixelSize = finite(v)
	}
}

func fillPower(p *Power, samples []sample) {
	if len(samples) == 0 {
		p.Source = Source{Source: SourceNone, Error: "no UPS metrics in the metrics store"}
		return
	}
	p.Source = Source{Source: SourcePrometheus}
	for _, sm := range samples {
		switch strings.TrimPrefix(sm.labels["__name__"], "network_ups_tools_") {
		case "battery_charge":
			p.Charge = finite(sm.value)
		case "input_voltage":
			p.InputVoltage = finite(sm.value)
		case "battery_runtime":
			p.RuntimeSeconds = finite(sm.value)
		case "device_info":
			p.Model = strings.TrimSpace(manufacturer(sm.labels["mfr"]) + " " + sm.labels["model"])
		case "ups_status":
			f := sm.labels["flag"]
			switch f {
			case "OB":
				p.OnBattery = flag(sm.value)
			case "LB":
				p.LowBattery = flag(sm.value)
			}
			if sm.value == 1 {
				p.Flags = append(p.Flags, f)
			}
		}
	}
}

var ErrNoSym = errors.New("no SymmetricDS tables")

func parseSymTime(ts, offset string) (time.Time, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.UTC(), true
	}
	ts = strings.Replace(ts, "T", " ", 1)
	if offset == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00"} {
		if t, err := time.Parse(layout, ts+offset); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (s *Service) readSync(ctx context.Context, now time.Time) Sync {
	if s.sched == nil {
		return Sync{Source: Source{Source: SourceNone}}
	}
	out, err := readSym(ctx, s.sched, now)
	if errors.Is(err, ErrNoSym) {
		return Sync{Source: Source{Source: SourceNone, Error: "the scheduler database has no SymmetricDS tables"}}
	}
	if err != nil {
		return Sync{Source: Source{Source: SourceError, Error: err.Error()}}
	}
	return out
}

func readSym(ctx context.Context, db *gorm.DB, now time.Time) (Sync, error) {
	db = db.WithContext(ctx)
	if !db.Migrator().HasTable("sym_node_identity") || !db.Migrator().HasTable("sym_node_host") {
		return Sync{}, ErrNoSym
	}
	var self string
	if err := db.Raw("SELECT node_id FROM sym_node_identity").Scan(&self).Error; err != nil {
		return Sync{}, err
	}
	type hostRow struct {
		NodeID string
		HB     string
		Offset string
	}
	var selfHost hostRow
	if err := db.Raw("SELECT node_id, CAST(heartbeat_time AS VARCHAR(40)) AS hb, timezone_offset AS \"offset\" FROM sym_node_host WHERE node_id = ? ORDER BY heartbeat_time DESC LIMIT 1", self).
		Scan(&selfHost).Error; err != nil {
		return Sync{}, err
	}
	var remote hostRow
	if err := db.Raw("SELECT node_id, CAST(heartbeat_time AS VARCHAR(40)) AS hb, timezone_offset AS \"offset\" FROM sym_node_host WHERE node_id <> ? ORDER BY heartbeat_time DESC LIMIT 1", self).
		Scan(&remote).Error; err != nil {
		return Sync{}, err
	}
	out := Sync{Source: Source{Source: SourceSymmetric}, Node: remote.NodeID}
	var latest time.Time
	if t, ok := parseSymTime(remote.HB, remote.Offset); ok {
		out.Heartbeat = &t
		latest = t
	}
	if db.Migrator().HasTable("sym_incoming_batch") {
		var last string
		if err := db.Raw("SELECT COALESCE(CAST(MAX(create_time) AS VARCHAR(40)), '') FROM sym_incoming_batch WHERE status = 'OK'").Scan(&last).Error; err != nil {
			return Sync{}, err
		}
		if t, ok := parseSymTime(last, selfHost.Offset); ok {
			out.LastBatch = &t
			if t.After(latest) {
				latest = t
			}
		}
		var errs int64
		if err := db.Raw("SELECT COUNT(*) FROM sym_incoming_batch WHERE status = 'ER'").Scan(&errs).Error; err != nil {
			return Sync{}, err
		}
		n := int(errs)
		out.Errors = &n
	}
	if !latest.IsZero() {
		lag := math.Max(0, now.Sub(latest).Seconds())
		out.LagSeconds = &lag
	}
	return out, nil
}
