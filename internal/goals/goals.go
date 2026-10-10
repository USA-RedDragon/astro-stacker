package goals

import "time"

type Kind string

const (
	KindSNR   Kind = "snr"
	KindDepth Kind = "depth"
)

const (
	DefaultSNRGoal    = 10.0
	DepthSNR          = 3.0
	PlateauGainPct    = 1.5
	BandLowPercentile = 20.0
	BandHighPercent   = 40.0
	NoiseBin          = 4
	MinLevelSubs      = 4
)

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Goal struct {
	TargetGUID  string  `json:"targetGuid"`
	Filter      string  `json:"filter"`
	Kind        Kind    `json:"kind"`
	SNR         float64 `json:"snr"`
	Depth       float64 `json:"depth"`
	PlateauStop bool    `json:"plateauStop"`
	Region      []Point `json:"region,omitempty"`
}

type Progress struct {
	Object         string    `json:"object"`
	Filter         string    `json:"filter"`
	TargetGUID     string    `json:"targetGuid,omitempty"`
	Kind           Kind      `json:"kind"`
	Goal           float64   `json:"goal"`
	Achieved       float64   `json:"achieved"`
	Progress       float64   `json:"progress"`
	SNR            float64   `json:"snr"`
	Depth          float64   `json:"depth"`
	EffectiveHours float64   `json:"effectiveHours"`
	HoursNeeded    float64   `json:"hoursNeeded"`
	GainPerHourPct float64   `json:"gainPerHourPct"`
	Plateau        bool      `json:"plateau"`
	LowConfidence  bool      `json:"lowConfidence"`
	Region         bool      `json:"region"`
	Done           bool      `json:"done"`
	MeasuredAt     time.Time `json:"measuredAt"`
}

type Key struct {
	Object string
	Filter string
}

const MethodRevision = 1
