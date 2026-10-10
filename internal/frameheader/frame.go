package frameheader

import (
	"math"
	"strings"
	"time"
)

// Frame is the acquisition metadata that calibration matching depends on.
// Numeric fields are NaN when the keyword is missing.
type Frame struct {
	Type        string // LIGHT, FLAT, DARK, BIAS, DARKFLAT
	Object      string
	Filter      string
	Exposure    float64
	Gain        float64
	Offset      float64
	SetTemp     float64
	CCDTemp     float64
	BinX        float64
	BinY        float64
	Rotator     float64
	Camera      string
	ReadoutMode string
	DateObs     time.Time
	HasDate     bool
	Longitude   float64
	// RA and Dec are where the mount pointed, in degrees, or the centre of
	// the plate solution; NaN if unknown.
	RA, Dec                             float64
	TSProject, TSTarget, TSExposurePlan string
	TSPanel                             int
	Width, Height                       int
	FocalLength                         float64
	PixelSize                           float64
	Telescope                           string
	BayerPattern                        string
}

// FromKeywords maps NINA's FITS keywords onto a Frame.
func FromKeywords(k Keywords) Frame {
	f := Frame{
		Type:         normalizeType(k.String("IMAGETYP")),
		Object:       k.String("OBJECT"),
		Filter:       NormalizeFilter(k.String("FILTER")),
		Exposure:     k.Float("EXPTIME"),
		Gain:         k.Float("GAIN"),
		Offset:       k.Float("OFFSET"),
		SetTemp:      k.Float("SET-TEMP"),
		CCDTemp:      k.Float("CCD-TEMP"),
		BinX:         k.Float("XBINNING"),
		BinY:         k.Float("YBINNING"),
		Rotator:      k.Float("ROTATANG"),
		Camera:       k.String("INSTRUME"),
		ReadoutMode:  k.String("READOUTM"),
		Longitude:    k.Float("SITELONG"),
		FocalLength:  k.Float("FOCALLEN"),
		PixelSize:    k.Float("XPIXSZ"),
		Telescope:    k.String("TELESCOP"),
		BayerPattern: k.String("BAYERPAT"),
	}
	for _, d := range []struct {
		key string
		dst *int
	}{{"NAXIS1", &f.Width}, {"NAXIS2", &f.Height}} {
		if n := k.Float(d.key); n >= 1 && n == math.Trunc(n) {
			*d.dst = int(n)
		}
	}
	if !(f.FocalLength > 0) {
		f.FocalLength = math.NaN()
	}
	if !(f.PixelSize > 0) {
		f.PixelSize = math.NaN()
	}
	f.RA, f.Dec = pointing(k)
	if math.IsNaN(f.Exposure) {
		f.Exposure = k.Float("EXPOSURE")
	}
	if math.IsNaN(f.Rotator) {
		f.Rotator = k.Float("ROTATOR")
	}
	f.DateObs, f.HasDate = k.Time("DATE-OBS")
	f.TSProject, f.TSTarget, f.TSExposurePlan = k.String("TSPROJ"), k.String("TSTARGET"), k.String("TSEXPPLN")
	if n := k.Float("TSPANEL"); n >= 1 && n == math.Trunc(n) {
		f.TSPanel = int(n)
	}
	return f
}

// filterNames maps other names for the narrowband filters, lower-cased and
// without spaces or dashes, onto NINA's: remote telescopes (Telescope.live)
// write "Halpha", "OIII", "Sii".
func filterNames() map[string]string {
	const hAlpha, oIII, sII = "H-a", "O-III", "S-II"
	return map[string]string{
		"ha": hAlpha, "halpha": hAlpha, "hydrogenalpha": hAlpha,
		"oiii": oIII, "o3": oIII,
		"sii": sII, "s2": sII,
	}
}

// NormalizeFilter names a filter as NINA does, so the same filter stacks
// into one master whatever wrote the file.
func NormalizeFilter(name string) string {
	key := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(name))
	if n, ok := filterNames()[key]; ok {
		return n
	}
	return name
}

func normalizeType(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if strings.Contains(t, "MASTER") {
		// Integrated masters are not raw frames; keep them apart so they are
		// never stacked as if they were.
		return "MASTER" + normalizeType(strings.ReplaceAll(t, "MASTER", ""))
	}
	switch {
	case strings.Contains(t, "DARKFLAT"), strings.Contains(t, "FLAT DARK"), strings.Contains(t, "FLATDARK"):
		return "DARKFLAT"
	case strings.Contains(t, "FLAT"):
		return "FLAT"
	case strings.Contains(t, "BIAS"), strings.Contains(t, "OFFSET"):
		return "BIAS"
	case strings.Contains(t, "DARK"):
		return "DARK"
	case strings.Contains(t, "LIGHT"):
		return "LIGHT"
	default:
		return t
	}
}

// Night returns the observing night a frame belongs to: the local date on
// which the night began, so a session that crosses midnight stays one night.
// It uses local mean solar time from the site longitude, which needs no time
// zone database and puts the date change near local noon.
func (f Frame) Night() (time.Time, bool) {
	if !f.HasDate {
		return time.Time{}, false
	}
	lon := f.Longitude
	if math.IsNaN(lon) {
		lon = 0
	}
	solar := f.DateObs.Add(time.Duration(lon / 15 * float64(time.Hour)))
	n := solar.Add(-12 * time.Hour)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC), true
}
