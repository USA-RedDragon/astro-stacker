package horizonsurvey

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	RunFile      = "run.json"
	ManifestFile = "frames.jsonl"
)

type MountState struct {
	RAHours       float64 `json:"ra_hours"`
	Dec           float64 `json:"dec"`
	SiderealHours float64 `json:"sidereal_hours"`
	PierSide      string  `json:"pier_side"`
	Alt           float64 `json:"alt"`
	Az            float64 `json:"az"`
	Tracking      bool    `json:"tracking"`
}

func (m MountState) HourAngle() float64 { return wrap180((m.SiderealHours - m.RAHours) * 15) }

type FrameRecord struct {
	File        string     `json:"file"`
	Column      int        `json:"column"`
	Row         int        `json:"row"`
	TargetAz    float64    `json:"target_az"`
	TargetAlt   float64    `json:"target_alt"`
	Start       time.Time  `json:"start"`
	ExposureMS  int        `json:"exposure_ms"`
	Mode        string     `json:"mode"`
	PixelScale  float64    `json:"pixel_scale"`
	Mount       MountState `json:"mount"`
	SkyRef      float64    `json:"sky_ref"`
	StarRef     float64    `json:"star_ref,omitempty"`
	Stars       int        `json:"stars"`
	Class       string     `json:"class"`
	ObstFrac    float64    `json:"obstructed_fraction"`
	SkyLevel    float64    `json:"sky_level"`
	SunAlt      float64    `json:"sun_alt"`
	SunSep      float64    `json:"sun_separation"`
	Solved      bool       `json:"solved"`
	SolveDetail string     `json:"solve_detail,omitempty"`
}

func (f FrameRecord) MidTime() time.Time {
	return f.Start.Add(time.Duration(f.ExposureMS) * time.Millisecond / 2)
}

type RunHeader struct {
	Started     time.Time  `json:"started"`
	Site        Site       `json:"site"`
	Mode        string     `json:"mode"`
	PixelScale  float64    `json:"pixel_scale"`
	FrameW      int        `json:"frame_width"`
	FrameH      int        `json:"frame_height"`
	PHD2Version string     `json:"phd2_version"`
	Plan        PlanConfig `json:"plan"`
	Safety      Safety     `json:"safety"`
	Ended       time.Time  `json:"ended,omitzero"`
	Outcome     string     `json:"outcome,omitempty"`
}

func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Manifest struct {
	f *os.File
}

func CreateManifest(dir string) (*Manifest, error) {
	f, err := os.OpenFile(filepath.Join(dir, ManifestFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Manifest{f: f}, nil
}

func (m *Manifest) Append(r FrameRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := m.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return m.f.Sync()
}

func (m *Manifest) Close() error { return m.f.Close() }

func ReadRun(dir string) (RunHeader, []FrameRecord, error) {
	var h RunHeader
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, RunFile)))
	if err != nil {
		return h, nil, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, nil, fmt.Errorf("%s: %w", RunFile, err)
	}
	f, err := os.Open(filepath.Clean(filepath.Join(dir, ManifestFile)))
	if err != nil {
		return h, nil, err
	}
	defer func() { _ = f.Close() }()
	var frames []FrameRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r FrameRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return h, nil, fmt.Errorf("%s: %w", ManifestFile, err)
		}
		frames = append(frames, r)
	}
	if err := sc.Err(); err != nil {
		return h, nil, err
	}
	if len(frames) == 0 {
		return h, nil, errors.New("the survey has no frames")
	}
	return h, frames, nil
}
