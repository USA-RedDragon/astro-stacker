package horizonsurvey

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

var ErrNoUsableFrames = errors.New("no frame could be mapped to the sky")

func FITSHeader(b []byte) map[string]string {
	kw := map[string]string{}
	for off := 0; off+80 <= len(b); off += 80 {
		card := string(b[off : off+80])
		key := strings.TrimSpace(card[:8])
		if key == "END" {
			break
		}
		if len(card) < 10 || card[8:10] != "= " {
			continue
		}
		v := strings.TrimSpace(card[10:])
		if strings.HasPrefix(v, "'") {
			if i := strings.Index(v[1:], "'"); i >= 0 {
				v = v[1 : i+1]
			}
		} else if i := strings.Index(v, "/"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		kw[key] = strings.TrimSpace(v)
	}
	return kw
}

func LoadFrame(path string) (*imagedata.Image, map[string]string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return im, FITSHeader(b), nil
}

type AnalyzeOptions struct {
	Dirs        []string
	Calibration *Calibration
	HRZStep     float64
	Log         io.Writer
}

type FrameResult struct {
	Dir     string
	Site    Site
	Record  FrameRecord
	Class   string
	Source  string
	Bins    []FrameBin
	Outline [][2]float64
	Err     error
}

func (f FrameResult) Path() string { return filepath.Join(f.Dir, filepath.Base(f.Record.File)) }

type Result struct {
	Header      RunHeader
	Headers     []RunHeader
	Frames      []FrameResult
	Skyline     *Skyline
	Points      []HorizonPoint
	Calibration *Calibration
}

func Analyze(opts AnalyzeOptions) (*Result, error) {
	logf := func(format string, a ...any) {
		if opts.Log != nil {
			_, _ = fmt.Fprintf(opts.Log, format+"\n", a...)
		}
	}
	if len(opts.Dirs) == 0 {
		return nil, ErrNoUsableFrames
	}
	res := &Result{}
	bottom, top := math.Inf(1), math.Inf(-1)
	var cals []Calibration
	for _, dir := range opts.Dirs {
		h, frames, err := ReadRun(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		res.Headers = append(res.Headers, h)
		bottom = math.Min(bottom, math.Max(h.Plan.AltMin, h.Safety.MinAlt))
		top = math.Max(top, h.Plan.AltMax)
		fillSkyRef(frames)
		for _, f := range frames {
			fr := FrameResult{Dir: dir, Site: h.Site, Record: f}
			b, err := os.ReadFile(filepath.Clean(fr.Path()))
			if err != nil {
				fr.Err = err
				res.Frames = append(res.Frames, fr)
				logf("%s: %v", fr.Path(), err)
				continue
			}
			res.Frames = append(res.Frames, fr)
			cards := FITSHeader(b)
			w, errW := strconv.Atoi(cards["NAXIS1"])
			hh, errH := strconv.Atoi(cards["NAXIS2"])
			if wcs, ok := ParseWCS(cards); ok && errW == nil && errH == nil && (f.Mount.PierSide == PierEast || f.Mount.PierSide == PierWest) {
				th, par, sc := NewWCSGeometry(w, hh, h.Site, wcs, f.MidTime()).Orientation()
				cals = append(cals, Calibration{Theta: th, Parity: par, Scale: sc, PierSide: f.Mount.PierSide})
			}
		}
	}
	res.Header = res.Headers[0]
	if len(cals) > 0 {
		c := mergeCalibrations(cals)
		res.Calibration = &c
		logf("calibration from %d solved frames: theta %.2f° on %s, parity %d, %.3f arcsec/px, spread %.2f°", c.Frames, c.Theta, c.PierSide, c.Parity, c.Scale, c.Spread)
	}
	cal := opts.Calibration
	if cal == nil {
		cal = res.Calibration
	}
	var all [][]FrameBin
	for i := range res.Frames {
		fr := &res.Frames[i]
		if fr.Err != nil {
			continue
		}
		im, cards, err := LoadFrame(fr.Path())
		if err != nil {
			fr.Err = err
			continue
		}
		g, src, err := FrameGeometry(fr.Record, im.W, im.H, fr.Site, cards, cal)
		if err != nil {
			fr.Err = err
			logf("%v", err)
			continue
		}
		seg := Segment(im.Plane(0), im.W, im.H, SegmentOptions{Mode: fr.Record.Mode, SkyRef: fr.Record.SkyRef, StarDensity: fr.Record.StarRef})
		fr.Class, fr.Source = seg.Class, src
		fr.Bins = FrameSkyline(g, seg.Mask)
		fr.Outline = FrameOutline(g)
		all = append(all, fr.Bins)
		logf("%s: %s, %d azimuth bins, geometry from %s", fr.Path(), seg.Class, len(fr.Bins), src)
	}
	if len(all) == 0 {
		return res, ErrNoUsableFrames
	}
	res.Skyline = Combine(all, bottom, top)
	step := opts.HRZStep
	if step <= 0 {
		step = 1
	}
	res.Points = res.Skyline.Points(step)
	return res, nil
}

func fillSkyRef(frames []FrameRecord) {
	var rates []float64
	for _, f := range frames {
		if f.Mode == ModeNight && f.Class == ClassSky && f.SkyLevel > 0 && f.ExposureMS > 0 {
			rates = append(rates, f.SkyLevel/float64(f.ExposureMS))
		}
	}
	if len(rates) == 0 {
		return
	}
	rate := percentile(rates, 0.5)
	for i := range frames {
		if frames[i].Mode == ModeNight && frames[i].SkyRef <= 0 {
			frames[i].SkyRef = rate * float64(frames[i].ExposureMS)
		}
	}
}

func mergeCalibrations(cals []Calibration) Calibration {
	ref := cals[0].PierSide
	var sx, sy, scale float64
	parity := 0
	thetas := make([]float64, 0, len(cals))
	for _, c := range cals {
		th := c.Theta
		if c.PierSide != ref {
			th = wrap360(th + 180)
		}
		thetas = append(thetas, th)
		sx += math.Cos(th * rad)
		sy += math.Sin(th * rad)
		scale += c.Scale
		parity += c.Parity
	}
	mean := wrap360(math.Atan2(sy, sx) / rad)
	spread := 0.0
	for _, th := range thetas {
		spread = math.Max(spread, math.Abs(wrap180(th-mean)))
	}
	p := 1
	if parity < 0 {
		p = -1
	}
	return Calibration{Theta: mean, PierSide: ref, Parity: p, Scale: scale / float64(len(cals)), Frames: len(cals), Spread: spread, Source: "plate solves"}
}

func (r *Result) HRZHeader() []string {
	measured := 0
	for _, p := range r.Points {
		if p.Measured {
			measured++
		}
	}
	solved, mapped := 0, 0
	for _, f := range r.Frames {
		if f.Err == nil && f.Source != "" {
			mapped++
			if strings.HasPrefix(f.Source, "plate") {
				solved++
			}
		}
	}
	return []string{
		"NINA horizon file: azimuth altitude, degrees",
		fmt.Sprintf("horizon-survey of %s, site %.4f %.4f", r.Header.Started.UTC().Format(time.RFC3339), r.Header.Site.Lat, r.Header.Site.Lon),
		fmt.Sprintf("%d frames mapped (%d plate solved), %d of %d points measured, the rest interpolated", mapped, solved, measured, len(r.Points)),
	}
}

func (r *Result) Unmeasured() []AzRange {
	var out []AzRange
	start := -1.0
	for _, b := range r.Skyline.Bins {
		if !b.Measured && start < 0 {
			start = b.Az - BinWidth/2
		}
		if b.Measured && start >= 0 {
			out = append(out, AzRange{From: start, To: b.Az - BinWidth/2})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, AzRange{From: start, To: 360})
	}
	return slices.Clip(out)
}
