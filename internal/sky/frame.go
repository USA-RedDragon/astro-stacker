package sky

import "math"

const DefaultOverlap = 0.15

type Frame struct {
	FocalLength float64 `json:"focalLength"`
	PixelSize   float64 `json:"pixelSize"`
	WidthPx     int     `json:"widthPx"`
	HeightPx    int     `json:"heightPx"`
}

func (f Frame) Scale() float64 {
	if f.FocalLength <= 0 {
		return 0
	}
	return 206.264806 * f.PixelSize / f.FocalLength
}

func (f Frame) WidthDeg() float64 { return f.Scale() * float64(f.WidthPx) / 3600 }

func (f Frame) HeightDeg() float64 { return f.Scale() * float64(f.HeightPx) / 3600 }

type Fit struct {
	Fill     float64 `json:"fill"`
	Panels   int     `json:"panels"`
	Columns  int     `json:"columns"`
	Rows     int     `json:"rows"`
	Category string  `json:"category"`
}

const (
	FitOne  = "one"
	FitFew  = "few"
	FitMany = "many"
)

func panelsFor(size, frame, overlap float64) int {
	if size <= frame {
		return 1
	}
	step := frame * (1 - overlap)
	return int(math.Ceil((size - frame*overlap) / step))
}

func (f Frame) Fit(majorArcmin, minorArcmin, overlap float64) Fit {
	w, h := f.WidthDeg(), f.HeightDeg()
	major, minor := majorArcmin/60, minorArcmin/60
	if minor <= 0 {
		minor = major
	}
	if minor > major {
		major, minor = minor, major
	}
	out := Fit{}
	if w > 0 {
		out.Fill = major / w
	}
	cols, rows := panelsFor(major, w, overlap), panelsFor(minor, h, overlap)
	altCols, altRows := panelsFor(minor, w, overlap), panelsFor(major, h, overlap)
	if altCols*altRows < cols*rows {
		cols, rows = altCols, altRows
	}
	out.Columns, out.Rows, out.Panels = cols, rows, cols*rows
	switch {
	case out.Panels <= 1:
		out.Category = FitOne
	case out.Panels <= 4:
		out.Category = FitFew
	default:
		out.Category = FitMany
	}
	return out
}

func (f Frame) Coverage(widthDeg, heightDeg float64) float64 {
	area := widthDeg * heightDeg
	if area <= 0 {
		return 1
	}
	return math.Min(1, f.WidthDeg()*f.HeightDeg()/area)
}
