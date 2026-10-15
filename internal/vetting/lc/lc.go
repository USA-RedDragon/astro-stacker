// Package lc holds light curves from every source the vetting job reads,
// on one time scale: barycentric Julian date in TDB.
package lc

import "time"

// Survey names, as the panel shows them.
const (
	Own     = "Own"
	ZTF     = "ZTF"
	ATLAS   = "ATLAS"
	TESS    = "TESS"
	NEOWISE = "NEOWISE"
)

// Point is one measurement. Mag is a magnitude, larger is fainter; in a
// Relative series it is magnitudes fainter than the star's normal level.
type Point struct {
	BJD float64 `json:"bjd"`
	Mag float64 `json:"mag"`
	Err float64 `json:"err"`
}

// Series is one survey's light curve of the star in one band.
type Series struct {
	Survey string  `json:"survey"`
	Band   string  `json:"band"`
	Points []Point `json:"points"`
	// Relative is set when Mag is relative to the star's normal level.
	Relative bool `json:"relative"`
	// Dilution is the share of the measured light that is the star's own,
	// 1 when nothing else falls in the measurement. A blended survey sees
	// dips shallower by this factor.
	Dilution float64 `json:"dilution"`
	// Deblended is set when the survey fitted the star and its close
	// neighbours apart.
	Deblended bool   `json:"deblended"`
	Note      string `json:"note,omitempty"`
}

// BJD is the barycentric Julian date in TDB of a UTC instant seen from
// Earth's centre, toward ra, dec in degrees.
func BJD(t time.Time, ra, dec float64) float64 {
	return bjd(t, ra, dec)
}

// MJDTime is the UTC instant of a modified Julian date in UTC.
func MJDTime(mjd float64) time.Time {
	const unixMJD = 40587.0
	ns := (mjd - unixMJD) * 86400e9
	return time.Unix(0, int64(ns)).UTC()
}
