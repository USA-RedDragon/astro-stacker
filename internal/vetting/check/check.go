// Package check holds the result of one vetting check, shared by the
// packages that run them and the report that shows them.
package check

// Status is how one check came out.
type Status string

const (
	// Pass means the check ran and found nothing against the spot being real and new.
	Pass Status = "pass"
	// Fail means the check ran and found a reason against it.
	Fail Status = "fail"
	// NotRun means the check did not run or could not decide; Evidence says why.
	NotRun Status = "skip"
)

// Check is one row of the vetting panel. Evidence is one or two plain
// sentences a person reads: measured values only, no source tags or jargon.
type Check struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   Status `json:"status"`
	Evidence string `json:"evidence"`
}

// Group IDs, in the order the panel shows them.
const (
	GroupImage   = "image"
	GroupOwn     = "own"
	GroupSurveys = "surveys"
	GroupCatalog = "catalogs"
	GroupMoving  = "moving"
	// GroupMover holds a moving object's own checks along its track.
	GroupMover = "mover"
	// GroupTransient holds the checks of new light where no star was.
	GroupTransient = "transient"
)

// Check IDs. The group a check belongs to is in Groups.
const (
	HotPixel  = "hot_pixel"
	RefClean  = "ref_clean"
	Trail     = "trail"
	PSF       = "psf"
	Edge      = "edge"
	OwnLC     = "own_lc"
	OwnChange = "own_change"
	OwnQuiet  = "own_quiet"
	ZTF       = "ztf"
	Period    = "period"
	Fold      = "fold"
	Half      = "half"
	Double    = "double"
	Echo      = "echo"
	Compare   = "compare"
	TESS      = "tess"
	OwnTiming = "own_timing"
	ATLAS     = "atlas"
	NEOWISE   = "neowise"
	VSX       = "vsx"
	GaiaVari  = "gaia_vari"
	SurveyVar = "survey_vars"
	TNS       = "tns"
	SIMBAD    = "simbad"
	Stays     = "stays"

	MoverFrames = "mover_frames"
	MoverLine   = "mover_line"
	MoverSensor = "mover_sensor"
	MoverShape  = "mover_shape"
	MoverSteady = "mover_steady"
	MoverKnown  = "mover_known"
	MoverMotion = "mover_motion"
	MoverNights = "mover_nights"
	// MoverStack and TransStack stack the frames along the motion, or at
	// the spot, against fakes of the same light and controls.
	MoverStack  = "mover_stack"
	TransStack  = "trans_stack"
	TransFlux   = "trans_flux"
	TransNights = "trans_nights"
	TransBefore = "trans_before"
	TransATLAS  = "trans_atlas"
	TransZTF    = "trans_ztf"
	TransHost   = "trans_host"
	// TransNext and the checks after it weigh new light seen in the
	// user's own frames only.
	TransNext      = "trans_next"
	TransFresh     = "trans_fresh"
	TransColor     = "trans_color"
	TransBroad     = "trans_broad"
	TransWing      = "trans_wing"
	TransNucleus   = "trans_nucleus"
	TransClear     = "trans_clear"
	TransZTFImages = "trans_ztf_images"
)

// Names are the row titles the panel shows for each check ID.
func Names() map[string]string {
	return map[string]string{
		HotPixel:  "Not a hot pixel",
		RefClean:  "Reference image clean at that spot",
		Trail:     "No satellite trail",
		PSF:       "Star-shaped",
		Edge:      "Not saturated or at the edge",
		OwnLC:     "Light curve against steady stars",
		OwnChange: "Change is large and clear",
		OwnQuiet:  "Other nights quiet",
		ZTF:       "ZTF",
		Period:    "Period search",
		Fold:      "Folded dips line up",
		Half:      "Half the period ruled out",
		Double:    "Double the period ruled out",
		Echo:      "Not an echo of the day, month or year",
		Compare:   "Nearby steady stars stay flat",
		TESS:      "TESS",
		OwnTiming: "Your dip lands at the predicted time",
		ATLAS:     "ATLAS",
		NEOWISE:   "NEOWISE",
		VSX:       "VSX",
		GaiaVari:  "Gaia variability",
		SurveyVar: "Survey variable star lists",
		TNS:       "Transient reports (TNS)",
		SIMBAD:    "SIMBAD",
		Stays:     "Stays in place",

		MoverFrames: "Seen in enough frames",
		MoverLine:   "Moves in a straight line at a steady speed",
		MoverSensor: "Moves across the sky, not with the camera",
		MoverShape:  "Star-shaped for its speed",
		MoverSteady: "Steady from frame to frame, not a flash",
		MoverKnown:  "Known asteroid or comet",
		MoverMotion: "Moves like the asteroids around it",
		MoverNights: "Seen on other nights",
		MoverStack:  "Shows when its frames are stacked along its motion",
		TransStack:  "Shows when its frames are stacked at the spot",
		TransFlux:   "Your own brightness at the spot",
		TransNights: "Rise and fade across nights",
		TransBefore: "What was at the spot before",
		TransATLAS:  "ATLAS on the same dates",
		TransZTF:    "ZTF alerts on the same dates",
		TransHost:   "Host galaxy",

		TransNext:      "Still there on the next nights",
		TransFresh:     "Seen in frames the detection did not use",
		TransColor:     "Seen in Luminance as in colour",
		TransBroad:     "Seen in broadband light",
		TransWing:      "Not a bright star's wing",
		TransNucleus:   "Not a galaxy nucleus residual",
		TransClear:     "Clear of bright stars and the edge",
		TransZTFImages: "ZTF images on the same night",
	}
}

// New is a check with its standard name.
func New(id string, s Status, evidence string) Check {
	return Check{ID: id, Name: Names()[id], Status: s, Evidence: evidence}
}

// Skipped is a check that did not run, with the reason as its evidence.
func Skipped(id, why string) Check { return New(id, NotRun, why) }
