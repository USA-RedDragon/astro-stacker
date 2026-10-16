package conditions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	// HistoryMinMinutes and HistoryMaxMinutes bound the camera history span.
	HistoryMinMinutes = 5
	HistoryMaxMinutes = 360
	// HistoryMaxPoints is the most points one camera history holds.
	HistoryMaxPoints = 240
	historyMinStep   = 15 * time.Second
)

// CameraPoint is the camera's cooling state at one time.
type CameraPoint struct {
	T           time.Time `json:"t"`
	Temperature *float64  `json:"temperature,omitempty"`
	Setpoint    *float64  `json:"setpoint,omitempty"`
	CoolerPower *float64  `json:"cooler_power,omitempty"`
}

// CameraHistory is the camera's sensor temperature, setpoint and cooler
// power over a span, oldest first.
type CameraHistory struct {
	Source
	StepSeconds int           `json:"step_seconds"`
	Points      []CameraPoint `json:"points"`
}

// HistoryStep is the step that keeps a span of minutes within
// HistoryMaxPoints points, never finer than historyMinStep.
func HistoryStep(minutes int) time.Duration {
	span := time.Duration(minutes) * time.Minute
	step := time.Duration(math.Ceil(span.Seconds()/HistoryMaxPoints)) * time.Second
	return max(step, historyMinStep)
}

// CameraHistory reads the camera's cooling over the last minutes from the
// metrics store.
func (s *Service) CameraHistory(ctx context.Context, minutes int) CameraHistory {
	step := HistoryStep(minutes)
	out := CameraHistory{StepSeconds: int(step / time.Second), Points: []CameraPoint{}}
	if s.opts.MetricsURL == "" {
		out.Source = Source{Source: SourceNone, Error: "no metrics store is configured"}
		return out
	}
	end := s.now().UTC().Truncate(step)
	start := end.Add(-time.Duration(minutes) * time.Minute)
	byT := map[int64]*CameraPoint{}
	series := []struct {
		metric string
		set    func(*CameraPoint, *float64)
	}{
		{"observatory_camera_temperature_celsius", func(p *CameraPoint, v *float64) { p.Temperature = v }},
		{"observatory_camera_target_temperature_celsius", func(p *CameraPoint, v *float64) { p.Setpoint = v }},
		{"observatory_camera_cooler_power_percent", func(p *CameraPoint, v *float64) { p.CoolerPower = v }},
	}
	for _, se := range series {
		pts, err := s.queryRange(ctx, "max("+se.metric+")", start, end, step)
		if err != nil {
			out.Source = Source{Source: SourceError, Error: err.Error()}
			out.Points = []CameraPoint{}
			return out
		}
		for _, p := range pts {
			cp := byT[p.t.Unix()]
			if cp == nil {
				cp = &CameraPoint{T: p.t}
				byT[p.t.Unix()] = cp
			}
			se.set(cp, finite(p.v))
		}
	}
	for _, p := range byT {
		if p.Temperature != nil || p.Setpoint != nil || p.CoolerPower != nil {
			out.Points = append(out.Points, *p)
		}
	}
	sort.Slice(out.Points, func(i, j int) bool { return out.Points[i].T.Before(out.Points[j].T) })
	if len(out.Points) == 0 {
		out.Source = Source{Source: SourceNone, Error: "no camera cooling metrics in the metrics store for this span"}
		return out
	}
	out.Source = Source{Source: SourcePrometheus}
	return out
}

type rangePoint struct {
	t time.Time
	v float64
}

func (s *Service) queryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]rangePoint, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	v := url.Values{}
	v.Set("query", q)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", strconv.Itoa(int(step/time.Second)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.MetricsURL+"/api/v1/query_range?"+v.Encode(), nil)
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
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("metrics range query answered %s", resp.Status)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("metrics range query failed: %s", out.Error)
	}
	var pts []rangePoint
	for _, r := range out.Data.Result {
		for _, p := range r.Values {
			ts, ok := p[0].(float64)
			str, ok2 := p[1].(string)
			if !ok || !ok2 {
				continue
			}
			f, err := strconv.ParseFloat(str, 64)
			if err != nil {
				continue
			}
			pts = append(pts, rangePoint{t: time.UnixMilli(int64(math.Round(ts * 1000))).UTC(), v: f})
		}
	}
	return pts, nil
}
