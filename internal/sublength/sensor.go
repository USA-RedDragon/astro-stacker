package sublength

import (
	"math"
	"strings"
)

type Sensor struct {
	Camera    string  `json:"camera"`
	Gain      float64 `json:"gain"`
	EPerADU   float64 `json:"e_per_adu"`
	ReadNoise float64 `json:"read_noise_e"`
	FullWell  float64 `json:"full_well_e"`
	Basis     string  `json:"basis"`
}

const sensorBasis = "Photon transfer on this camera's own frames at offset 50: read noise is the σ of the difference of two bias frames " +
	"(2025-02-05) over √2, and e⁻/ADU is the mean signal over the shot-noise variance of the difference of two 3 s flats taken seconds " +
	"apart (2026-10-06 and 2026-10-08, centre half of the frame, 5σ clipped; the two nights agree to 0.1%). Full well is the 16-bit " +
	"ADC range above the 502 ADU bias level."

func knownSensors() []Sensor {
	return []Sensor{
		{Camera: "ASI2600MM", Gain: 0, EPerADU: 0.763, ReadNoise: 2.94, FullWell: 49600, Basis: sensorBasis},
		{Camera: "ASI2600MM", Gain: 100, EPerADU: 0.252, ReadNoise: 1.20, FullWell: 16400, Basis: sensorBasis},
	}
}

func SensorAt(camera string, gain float64) (Sensor, bool) {
	c := strings.ToUpper(strings.ReplaceAll(camera, " ", ""))
	for _, s := range knownSensors() {
		if strings.Contains(c, s.Camera) && math.Abs(s.Gain-gain) < 0.5 {
			return s, true
		}
	}
	return Sensor{}, false
}
