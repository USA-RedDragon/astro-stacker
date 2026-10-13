package horizonsurvey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	pathMountInfo     = "/equipment/mount/info"
	pathMountSlew     = "/equipment/mount/slew"
	pathMountStop     = "/equipment/mount/slew/stop"
	pathMountTracking = "/equipment/mount/tracking"
	pathMountHome     = "/equipment/mount/home"
	pathSequenceState = "/sequence/state"
	pathSafety        = "/equipment/safetymonitor/info"
	pathHorizon       = "/profile/horizon"
	trackingStopped   = 4
	TrackingOffTries  = 10
	trackingOffWait   = 2 * time.Second
)

var (
	ErrNINA          = errors.New("NINA Advanced API")
	ErrNotAllowed    = errors.New("endpoint is not on the horizon-survey allowlist")
	ErrSlewFailed    = errors.New("slew failed")
	ErrHomeFailed    = errors.New("find home failed")
	ErrSequenceState = errors.New("could not read the NINA sequence state")
)

func NINAAllowed(path string) bool {
	switch path {
	case pathMountInfo, pathMountSlew, pathMountStop, pathMountTracking, pathMountHome, pathSequenceState, pathSafety, pathHorizon:
		return true
	}
	return false
}

type NINA struct {
	Base         string
	HTTP         *http.Client
	TrackingWait time.Duration
}

func NewNINA(base string) *NINA {
	return &NINA{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{}, TrackingWait: trackingOffWait}
}

type envelope struct {
	Response   json.RawMessage `json:"Response"`
	Error      string          `json:"Error"`
	StatusCode int             `json:"StatusCode"`
	Success    bool            `json:"Success"`
}

type APIError struct {
	Path   string
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", ErrNINA, e.Path, e.Status, e.Msg)
}

func (e *APIError) Unwrap() error { return ErrNINA }

func (n *NINA) get(ctx context.Context, path string, q url.Values, timeout time.Duration, out any) error {
	if !NINAAllowed(path) {
		return fmt.Errorf("%w: %s", ErrNotAllowed, path)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := n.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrNINA, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrNINA, path, err)
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return &APIError{Path: path, Status: resp.StatusCode, Msg: "unreadable response"}
	}
	if !env.Success || resp.StatusCode >= 300 {
		st := env.StatusCode
		if st == 0 {
			st = resp.StatusCode
		}
		return &APIError{Path: path, Status: st, Msg: env.Error}
	}
	if out != nil {
		if err := json.Unmarshal(env.Response, out); err != nil {
			return fmt.Errorf("%w %s: %w", ErrNINA, path, err)
		}
	}
	return nil
}

type PierSide string

func (p *PierSide) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		switch s {
		case PierEast, PierWest:
			*p = PierSide(s)
		default:
			*p = PierUnknown
		}
		return nil
	}
	var i int
	if err := json.Unmarshal(b, &i); err != nil {
		return err
	}
	switch i {
	case 0:
		*p = PierEast
	case 1:
		*p = PierWest
	default:
		*p = PierUnknown
	}
	return nil
}

type MountInfo struct {
	Connected       bool     `json:"Connected"`
	AtPark          bool     `json:"AtPark"`
	AtHome          bool     `json:"AtHome"`
	Slewing         bool     `json:"Slewing"`
	TrackingEnabled bool     `json:"TrackingEnabled"`
	RightAscension  float64  `json:"RightAscension"`
	Declination     float64  `json:"Declination"`
	SiderealTime    float64  `json:"SiderealTime"`
	Altitude        float64  `json:"Altitude"`
	Azimuth         float64  `json:"Azimuth"`
	SiteLatitude    float64  `json:"SiteLatitude"`
	SiteLongitude   float64  `json:"SiteLongitude"`
	SideOfPier      PierSide `json:"SideOfPier"`
}

func (m MountInfo) State() MountState {
	return MountState{RAHours: m.RightAscension, Dec: m.Declination, SiderealHours: m.SiderealTime, PierSide: string(m.SideOfPier), Alt: m.Altitude, Az: m.Azimuth, Tracking: m.TrackingEnabled}
}

func (n *NINA) MountInfo(ctx context.Context) (MountInfo, error) {
	var m MountInfo
	err := n.get(ctx, pathMountInfo, nil, 15*time.Second, &m)
	return m, err
}

func (n *NINA) Slew(ctx context.Context, raDeg, decDeg float64) error {
	q := url.Values{}
	q.Set("ra", strconv.FormatFloat(raDeg, 'f', 6, 64))
	q.Set("dec", strconv.FormatFloat(decDeg, 'f', 6, 64))
	q.Set("waitForResult", "true")
	err := n.get(ctx, pathMountSlew, q, 5*time.Minute, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Msg == "" {
		return fmt.Errorf("%w: %w", ErrSlewFailed, err)
	}
	return err
}

func (n *NINA) StopSlew(ctx context.Context) error {
	return n.get(ctx, pathMountStop, nil, 15*time.Second, nil)
}

func (n *NINA) TrackingOff(ctx context.Context) error {
	q := url.Values{}
	q.Set("mode", strconv.Itoa(trackingStopped))
	var err error
	for range TrackingOffTries {
		if err = n.get(ctx, pathMountTracking, q, 30*time.Second, nil); err == nil {
			return nil
		}
		var ae *APIError
		if !errors.As(err, &ae) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(n.TrackingWait):
		}
	}
	return err
}

func (n *NINA) FindHome(ctx context.Context) error {
	return n.get(ctx, pathMountHome, nil, 30*time.Second, nil)
}

type SafetyInfo struct {
	Connected bool `json:"Connected"`
	IsSafe    bool `json:"IsSafe"`
}

func (n *NINA) Safety(ctx context.Context) (SafetyInfo, error) {
	var s SafetyInfo
	err := n.get(ctx, pathSafety, nil, 15*time.Second, &s)
	return s, err
}

func (n *NINA) SequenceRunning(ctx context.Context) (bool, error) {
	var v any
	err := n.get(ctx, pathSequenceState, nil, 30*time.Second, &v)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrSequenceState, err)
	}
	return anyRunning(v), nil
}

func anyRunning(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["Status"].(string); ok && strings.EqualFold(s, "RUNNING") {
			return true
		}
		for _, x := range t {
			if anyRunning(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if anyRunning(x) {
				return true
			}
		}
	}
	return false
}

type ProfileHorizon struct {
	Altitudes []float64 `json:"Altitudes"`
	Azimuths  []float64 `json:"Azimuths"`
}

func (n *NINA) Horizon(ctx context.Context) (ProfileHorizon, error) {
	var h ProfileHorizon
	err := n.get(ctx, pathHorizon, nil, 15*time.Second, &h)
	return h, err
}
