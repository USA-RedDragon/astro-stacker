package planning

import "github.com/USA-RedDragon/astro-stacker/internal/rigsource"

type Frame struct {
	WidthDeg    *float64        `json:"widthDeg"`
	HeightDeg   *float64        `json:"heightDeg"`
	Scale       *float64        `json:"scale"`
	FocalLength *float64        `json:"focalLength"`
	PixelSize   *float64        `json:"pixelSize"`
	WidthPx     *int            `json:"widthPx"`
	HeightPx    *int            `json:"heightPx"`
	Basis       rigsource.Basis `json:"basis"`
	Reason      *string         `json:"reason"`
}

func FrameFromRig(r rigsource.Rig) Frame {
	f := Frame{WidthDeg: r.WidthDeg, HeightDeg: r.HeightDeg, Scale: r.Scale, FocalLength: r.FocalLength, PixelSize: r.PixelSize,
		WidthPx: r.WidthPx, HeightPx: r.HeightPx, Basis: r.Basis}
	if f.WidthDeg == nil || f.HeightDeg == nil {
		msg := rigsource.ErrUnknown.Error()
		f.Reason = &msg
	}
	return f
}
