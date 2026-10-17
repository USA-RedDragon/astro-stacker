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
		TESS:      "TESS",
		OwnTiming: "Your dip lands at the predicted time",
		ATLAS:     "ATLAS",
		NEOWISE:   "NEOWISE",
		VSX:       "VSX",
		GaiaVari:  "Gaia variability",
		SurveyVar: "ATLAS, ZTF and ASAS-SN variable lists",
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
	}
}

// New is a check with its standard name.
func New(id string, s Status, evidence string) Check {
	return Check{ID: id, Name: Names()[id], Status: s, Evidence: evidence}
}

// Skipped is a check that did not run, with the reason as its evidence.
func Skipped(id, why string) Check { return New(id, NotRun, why) }
