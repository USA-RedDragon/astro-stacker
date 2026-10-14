package morningreport

import "time"

// Report is what one night measured. A nil pointer, or a Missing reason, is
// a value with no measurement behind it; the message says so instead of
// guessing.
type Report struct {
	Night      time.Time
	Start, End time.Time

	// Dark is how long the Sun was at or below DarkSunAltitude in the
	// window; the safe, flap open and imaged times count only then. Nil
	// with DarkMissing when the site is unknown, and those times count over
	// the whole window.
	Dark        *time.Duration
	DarkMissing string
	DarkSpans   []Span

	// Imaged is the lights' exposure inside the dark spans.
	Imaged *time.Duration

	// Safe is how long the safety monitor read safe; SafetyConnected how
	// long it was connected at all.
	Safe            *time.Duration
	SafetyConnected *time.Duration
	SafeMissing     string

	// FlapOpen is how long the flap was open, from NINA's open and close
	// cover steps.
	FlapOpen    *time.Duration
	FlapMissing string

	Subs        *Subs
	SubsMissing string

	Unsafe        []UnsafeEvent
	Restarts      []UnsafeEvent
	UnsafeMissing string

	Dew        *Dew
	DewMissing string

	Autofocus        []AutofocusRun
	AutofocusMissing string

	Guiding        *Guiding
	GuidingMissing string

	GuiderErrors        []ErrorBurst
	GuiderErrorsMissing string

	Camera Camera

	// Discoveries are the targets whose night comparison found something
	// that passed every check and matched nothing known.
	Discoveries []Count

	Images []Image
}

// Subs are the night's lights and the stacker's verdicts on them.
type Subs struct {
	Count    int
	Exposure time.Duration
	Kept     int
	Rejected int
	Pending  int
	Targets  []TargetSubs
	// Reasons are the rejections by why, most first.
	Reasons []Count
}

// TargetSubs are one target's lights of the night by filter.
type TargetSubs struct {
	Object  string
	Filters []FilterSubs
}

// FilterSubs are one target's lights of one filter.
type FilterSubs struct {
	Filter   string
	Kept     int
	Rejected int
	Pending  int
	// ByNight counts the kept lights of a moving target, which go into a
	// stack of their own night instead of the master.
	ByNight int
	// LastProcessed is when the stacker last decided on a kept light.
	LastProcessed time.Time
}

// Count is a label and how many.
type Count struct {
	Label string
	N     int
}

// UnsafeEvent is the flap left open by an unsafe reading or a NINA restart.
// Closed is nil when the flap was still open at the window's end.
type UnsafeEvent struct {
	At     time.Time
	Closed *time.Time
}

// SlowClose is how long the flap may take to close after an unsafe reading
// before the report flags it.
const SlowClose = 30 * time.Second

// Latency is how long the flap took to close; false when it never did.
func (e UnsafeEvent) Latency() (time.Duration, bool) {
	if e.Closed == nil {
		return 0, false
	}
	return e.Closed.Sub(e.At), true
}

// Dew is the margin of air temperature over dew point while the mount
// tracked, and what the dew heaters did.
type Dew struct {
	Lowest   float64
	LowestAt time.Time
	// Under is how long the margin was under BoostMargin while tracking.
	Under time.Duration
	// HeaterMax is the highest dew heater output while the margin was under
	// BoostMargin, nil when no heater reading covers that time.
	HeaterMax *float64
	Boosted   bool
	// Tracking is false when the mount never tracked and the margin is over
	// the whole window. TrackingMeasured is false when there is no tracking
	// series at all.
	Tracking         bool
	TrackingMeasured bool
	// ImagingLow and ImagingHigh are the margin's range while lights
	// exposed, nil without lights.
	ImagingLow, ImagingHigh *float64
}

// BoostMargin is the dew margin, in °C, under which the sequence runs the
// dew heaters at 100%.
const BoostMargin = 4.0

// AutofocusRun is one autofocus run of the night.
type AutofocusRun struct {
	Started  *time.Time
	Finished time.Time
	Success  bool
	Error    string
	Target   string
	// BackToBack is set when it started soon after the previous run ended.
	BackToBack bool
}

// BackToBackGap is the shortest gap between autofocus runs that isn't
// counted as a loop.
const BackToBackGap = 10 * time.Minute

// Guiding is the total guiding RMS, in arcseconds, while lights exposed.
type Guiding struct {
	Median float64
	// WorstMeasured is false when no WorstStretch had enough readings.
	WorstMeasured bool
	Worst         float64
	WorstAt       time.Time
	WorstEnd      time.Time
	Samples       int
}

// WorstStretch is the span the worst guiding is averaged over.
const WorstStretch = 10 * time.Minute

// ErrorBurst is guider errors close together.
type ErrorBurst struct {
	From, To time.Time
	Kinds    []Count
}

// Camera is the night's cooling.
type Camera struct {
	// Setpoints are the lights' cooling setpoints, most used first.
	Setpoints []Setpoint
	// CoolerMin and CoolerMax are the cooler power range, in percent, while
	// lights exposed.
	CoolerMin, CoolerMax *float64
}

// Setpoint is a cooling setpoint, in °C, and how many lights used it.
type Setpoint struct {
	C    float64
	Subs int
}

// RestartGap is how close NINA restarts are to count as one.
const RestartGap = 5 * time.Minute

// Image is one target's current stack preview, for a photo.
type Image struct {
	Object string
	// Label is the palette of a color preview or the filter of a master.
	Label    string
	Key      string
	Exposure time.Duration
	// NewSubs is the target's lights of the night kept or still waiting.
	NewSubs int
	// Current is false when the preview doesn't hold the night's subs yet.
	Current bool
	// ByNight is set for a moving target whose night's subs are in a stack
	// of that night, not in this preview.
	ByNight bool
	// CometNights are the nights a comet cover stacks the comet from, on
	// the comet itself; empty for any other preview.
	CometNights []time.Time
	// CometSubs is how many subs those nights hold.
	CometSubs int
	// ExposureMissing is set when the preview's total exposure is unknown.
	ExposureMissing bool
	JPEG            []byte
}
