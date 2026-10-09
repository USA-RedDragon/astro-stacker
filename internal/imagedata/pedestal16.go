package imagedata

import "math"

const (
	Pedestal16Offset = 0.02
	Pedestal16Step   = (1 + Pedestal16Offset) / math.MaxUint16
	Pedestal16Zero   = -Pedestal16Offset - Pedestal16Step/2
	Pedestal16Empty  = 0
)

const (
	PropCodeStep  = "AstroStacker:CodeStep"
	PropCodeZero  = "AstroStacker:CodeZero"
	PropEmptyCode = "AstroStacker:EmptyCode"
)

func Quantize16(v float32) uint16 {
	if v == 0 {
		return Pedestal16Empty
	}
	q := math.Round((float64(v) - Pedestal16Zero) / Pedestal16Step)
	return uint16(min(max(q, 1), math.MaxUint16))
}

func Dequantize16(q uint16) float32 {
	return decodeCode(q, Pedestal16Step, Pedestal16Zero, Pedestal16Empty)
}

func decodeCode(q uint16, step, zero float64, empty int) float32 {
	if int(q) == empty {
		return 0
	}
	return float32(float64(q)*step + zero)
}

func Pedestal16Properties() []Property {
	return []Property{
		{ID: PropCodeStep, Value: Pedestal16Step},
		{ID: PropCodeZero, Value: Pedestal16Zero},
		{ID: PropEmptyCode, Value: float64(Pedestal16Empty)},
	}
}
