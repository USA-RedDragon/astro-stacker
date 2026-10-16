package observatory

import (
	"encoding/json"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/autofocus"
)

type Score struct {
	Rule   string  `json:"rule"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
}

type Skip struct {
	Scope     string     `json:"scope"`
	TargetID  int64      `json:"target_id,omitempty"`
	ProjectID int64      `json:"project_id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
}

type Pause struct {
	Mount    string     `json:"mount,omitempty"`
	ResumeAt *time.Time `json:"resume_at,omitempty"`
}

type Target struct {
	ProjectID      int64      `json:"project_id"`
	ProjectName    string     `json:"project_name"`
	ProjectGUID    string     `json:"project_guid,omitempty"`
	TargetID       int64      `json:"target_id"`
	TargetName     string     `json:"target_name"`
	TargetGUID     string     `json:"target_guid,omitempty"`
	Priority       *int       `json:"priority,omitempty"`
	IsMosaic       bool       `json:"is_mosaic"`
	RAHours        *float64   `json:"ra_hours,omitempty"`
	DecDegrees     *float64   `json:"dec_degrees,omitempty"`
	Rotation       *float64   `json:"rotation,omitempty"`
	PickedAt       *time.Time `json:"picked_at,omitempty"`
	MinimumTimeEnd *time.Time `json:"minimum_time_end,omitempty"`
	HardStop       *time.Time `json:"hard_stop,omitempty"`
}

type Exposure struct {
	PlanID       int64      `json:"plan_id,omitempty"`
	Filter       string     `json:"filter"`
	Seconds      float64    `json:"seconds"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	EndsAt       *time.Time `json:"ends_at,omitempty"`
	Number       int        `json:"number,omitempty"`
	AbortBlocked string     `json:"abort_blocked,omitempty"`
	SyncClients  bool       `json:"sync_clients,omitempty"`
}

type Wait struct {
	Until      *time.Time `json:"until,omitempty"`
	TargetName string     `json:"target_name,omitempty"`
}

type PendingCommand struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
}

type Autofocus struct {
	RunID            string            `json:"run_id"`
	State            string            `json:"state"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	Filter           string            `json:"filter,omitempty"`
	Temperature      *float64          `json:"temperature,omitempty"`
	Autofocuser      string            `json:"autofocuser,omitempty"`
	Method           string            `json:"method,omitempty"`
	Fitting          string            `json:"fitting,omitempty"`
	TargetName       string            `json:"target_name,omitempty"`
	InitialPosition  *float64          `json:"initial_position,omitempty"`
	PreviousPosition *float64          `json:"previous_position,omitempty"`
	PreviousValue    *float64          `json:"previous_value,omitempty"`
	ChosenPosition   *float64          `json:"chosen_position,omitempty"`
	ChosenValue      *float64          `json:"chosen_value,omitempty"`
	Points           []autofocus.Point `json:"points"`
	Fit              json.RawMessage   `json:"fit,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type Status struct {
	Version        string           `json:"version,omitempty"`
	Time           *time.Time       `json:"time,omitempty"`
	WebEditing     *bool            `json:"web_editing,omitempty"`
	API            *bool            `json:"api,omitempty"`
	State          string           `json:"state,omitempty"`
	Paused         *bool            `json:"paused,omitempty"`
	PauseRequested *bool            `json:"pause_requested,omitempty"`
	Pause          *Pause           `json:"pause,omitempty"`
	Target         *Target          `json:"target,omitempty"`
	Exposure       *Exposure        `json:"exposure,omitempty"`
	Wait           *Wait            `json:"wait,omitempty"`
	Activity       string           `json:"activity,omitempty"`
	ActivityDetail string           `json:"activity_detail,omitempty"`
	ActivitySince  *time.Time       `json:"activity_since,omitempty"`
	Scores         []Score          `json:"scores,omitempty"`
	ScoreTotal     *float64         `json:"score_total,omitempty"`
	ScoreNote      string           `json:"score_note,omitempty"`
	Skips          []Skip           `json:"skips,omitempty"`
	Pending        []PendingCommand `json:"pending,omitempty"`
	LastPlanAt     *time.Time       `json:"last_plan_at,omitempty"`
	Autofocus      *Autofocus       `json:"autofocus,omitempty"`
	Calibration    *Calibration     `json:"calibration,omitempty"`
}

// Calibration is the plugin's report of the night's calibration run: a
// camera cool-down, darks, bias or flats, or the end of the last run.
type Calibration struct {
	Kind            string             `json:"kind,omitempty"`
	Placement       string             `json:"placement,omitempty"`
	Purpose         string             `json:"purpose,omitempty"`
	Since           *time.Time         `json:"since,omitempty"`
	UpdatedAt       *time.Time         `json:"updated_at,omitempty"`
	Camera          *CalibrationCamera `json:"camera,omitempty"`
	Ambient         *float64           `json:"ambient,omitempty"`
	SunLimitAt      *time.Time         `json:"sun_limit_at,omitempty"`
	SunLimitDegrees *float64           `json:"sun_limit_degrees,omitempty"`
	Cooling         *CalibrationCool   `json:"cooling,omitempty"`
	Set             *CalibrationSet    `json:"set,omitempty"`
	Frame           *CalibrationFrame  `json:"frame,omitempty"`
	Queue           []CalibrationQueue `json:"queue,omitempty"`
	Taken           []CalibrationTaken `json:"taken,omitempty"`
	Stop            *CalibrationStop   `json:"stop,omitempty"`
	Log             []CalibrationLog   `json:"log,omitempty"`
}

// CalibrationCamera is the plugin's own read of the camera.
type CalibrationCamera struct {
	Connected   *bool    `json:"connected,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Setpoint    *float64 `json:"setpoint,omitempty"`
	CoolerPower *float64 `json:"cooler_power,omitempty"`
}

// CalibrationCool describes a camera cool-down the plugin watches.
type CalibrationCool struct {
	Target      *float64   `json:"target,omitempty"`
	From        *float64   `json:"from,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	RateCPerMin *float64   `json:"rate_c_per_min,omitempty"`
	Source      string     `json:"source,omitempty"`
}

// CalibrationSet is the dark, bias or flat set being taken.
type CalibrationSet struct {
	ComboKey        string     `json:"combo_key,omitempty"`
	Exposure        *float64   `json:"exposure,omitempty"`
	Gain            *float64   `json:"gain,omitempty"`
	Offset          *float64   `json:"offset,omitempty"`
	Binning         *float64   `json:"binning,omitempty"`
	ReadoutMode     string     `json:"readout_mode,omitempty"`
	SetTemp         *float64   `json:"set_temp,omitempty"`
	Filter          string     `json:"filter,omitempty"`
	TargetName      string     `json:"target_name,omitempty"`
	TargetID        *int64     `json:"target_id,omitempty"`
	SessionDate     *time.Time `json:"session_date,omitempty"`
	Rotation        *float64   `json:"rotation,omitempty"`
	PanelBrightness *int       `json:"panel_brightness,omitempty"`
	FramesTotal     *int       `json:"frames_total,omitempty"`
	FramesBefore    *int       `json:"frames_before,omitempty"`
	FramesDone      *int       `json:"frames_done,omitempty"`
	Index           *int       `json:"index,omitempty"`
	Count           *int       `json:"count,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndsAt          *time.Time `json:"ends_at,omitempty"`
}

// CalibrationFrame is the calibration exposure in progress.
type CalibrationFrame struct {
	Number    *int       `json:"number,omitempty"`
	Seconds   *float64   `json:"seconds,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
}

// CalibrationQueue is one set in the run's order.
type CalibrationQueue struct {
	Kind          string     `json:"kind,omitempty"`
	ComboKey      string     `json:"combo_key,omitempty"`
	Exposure      *float64   `json:"exposure,omitempty"`
	Gain          *float64   `json:"gain,omitempty"`
	Offset        *float64   `json:"offset,omitempty"`
	Binning       *float64   `json:"binning,omitempty"`
	SetTemp       *float64   `json:"set_temp,omitempty"`
	Filter        string     `json:"filter,omitempty"`
	TargetName    string     `json:"target_name,omitempty"`
	FramesTotal   *int       `json:"frames_total,omitempty"`
	FramesHave    *int       `json:"frames_have,omitempty"`
	Remaining     *int       `json:"remaining,omitempty"`
	LightsBlocked *int       `json:"lights_blocked,omitempty"`
	LightsScaled  *int       `json:"lights_scaled,omitempty"`
	Status        string     `json:"status,omitempty"`
	ReasonCode    string     `json:"reason_code,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	PlannedStart  *time.Time `json:"planned_start,omitempty"`
	PlannedEnd    *time.Time `json:"planned_end,omitempty"`
	FitFrames     *int       `json:"fit_frames,omitempty"`
}

// CalibrationTaken is one set taken in this run.
type CalibrationTaken struct {
	Kind       string     `json:"kind,omitempty"`
	ComboKey   string     `json:"combo_key,omitempty"`
	Exposure   *float64   `json:"exposure,omitempty"`
	Gain       *float64   `json:"gain,omitempty"`
	Offset     *float64   `json:"offset,omitempty"`
	Binning    *float64   `json:"binning,omitempty"`
	SetTemp    *float64   `json:"set_temp,omitempty"`
	Filter     string     `json:"filter,omitempty"`
	TargetName string     `json:"target_name,omitempty"`
	Frames     *int       `json:"frames,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

// CalibrationStop says why the last run stopped or why nothing is left.
type CalibrationStop struct {
	At     *time.Time `json:"at,omitempty"`
	Code   string     `json:"code,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

// CalibrationLog is one plain-words step of the night's calibration.
type CalibrationLog struct {
	At   *time.Time `json:"at,omitempty"`
	Text string     `json:"text,omitempty"`
}

const (
	OverrideProject    = "project"
	OverrideRuleWeight = "ruleweight"
)

type Override struct {
	Entity string          `json:"entity"`
	ID     int64           `json:"id"`
	Field  string          `json:"field"`
	Value  json.RawMessage `json:"value"`
}

type PreviewRequest struct {
	Start     *time.Time `json:"start,omitempty"`
	Overrides []Override `json:"overrides"`
}

type Reachability string

const (
	Online       Reachability = "online"
	Offline      Reachability = "offline"
	Unconfigured Reachability = "unconfigured"
)

type View struct {
	*Status
	Reachable  Reachability `json:"reachable"`
	LastAnswer *time.Time   `json:"last_answer,omitempty"`
	Since      *time.Time   `json:"since,omitempty"`
	Live       bool         `json:"live"`
	Error      string       `json:"error,omitempty"`
}

type frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
