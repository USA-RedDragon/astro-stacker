package binaries

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries/minima"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// num is a number a debug dump prints to a few significant digits, and
// as null when it is not finite.
type num float64

// MarshalJSON writes n to 7 significant digits, or null.
func (n num) MarshalJSON() ([]byte, error) {
	v := float64(n)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, v, 'g', 7, 64), nil
}

// bjdNum is a BJD a debug dump prints to the 0.00001 day.
type bjdNum float64

// MarshalJSON writes b to 5 decimals, or null.
func (b bjdNum) MarshalJSON() ([]byte, error) {
	v := float64(b)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, v, 'f', 5, 64), nil
}

// DebugPoint is one frame of a debug dump: its measurement, if any, and
// its time from the predicted middle.
type DebugPoint struct {
	Frame   int    `json:"frame"`
	Filter  string `json:"filter"`
	BJD     bjdNum `json:"bjd"`
	Minutes num    `json:"minutes"`
	DMag    *num   `json:"dmag,omitempty"`
	Err     *num   `json:"err,omitempty"`
}

// DebugTry is one timing a dry run tried: one filter or all together.
type DebugTry struct {
	Filter     string `json:"filter"`
	Joint      bool   `json:"joint"`
	Points     int    `json:"points"`
	OK         bool   `json:"ok"`
	Why        string `json:"why,omitempty"`
	OCMinutes  num    `json:"oc_minutes"`
	ErrMinutes num    `json:"err_minutes"`
	Formal     num    `json:"formal_minutes"`
	Bead       num    `json:"bead_minutes"`
	Red        num    `json:"red"`
	Depth      num    `json:"depth"`
	DepthErr   num    `json:"depth_err"`
	Width      num    `json:"width"`
	Slope      bool   `json:"slope"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	CheckOC    *num   `json:"check_oc_minutes,omitempty"`
	CheckErr   *num   `json:"check_err_minutes,omitempty"`
	Agree      bool   `json:"agree"`
}

// DebugScan is the dip fitted with its middle Minutes from the predicted
// middle.
type DebugScan struct {
	Minutes num `json:"minutes"`
	Depth   num `json:"depth"`
	Err     num `json:"err"`
	Chi     num `json:"chi"`
	InDip   int `json:"in_dip"`
}

// TimingDebug is everything one eclipse's timing is based on: the
// ephemeris, the frames and their photometry, each timing tried, and
// the whole night's light curve with the dip's depth fitted at every
// middle through it.
type TimingDebug struct {
	SourceID     int                    `json:"source_id"`
	Kind         string                 `json:"kind"`
	Night        string                 `json:"night"`
	PredictedUTC time.Time              `json:"predicted_utc"`
	PredictedBJD bjdNum                 `json:"predicted_bjd"`
	Cycle        float64                `json:"cycle"`
	Ephemeris    *period.Ephemeris      `json:"ephemeris"`
	Dip          int                    `json:"dip"`
	HalfMinutes  num                    `json:"half_minutes"`
	ModelDepth   num                    `json:"model_depth"`
	GaiaG        num                    `json:"gaia_g"`
	Frames       int                    `json:"frames"`
	Note         string                 `json:"note"`
	Error        string                 `json:"error,omitempty"`
	Comps        int                    `json:"comps"`
	Dropped      int                    `json:"dropped"`
	Points       []DebugPoint           `json:"points"`
	Unmeasured   []DebugPoint           `json:"unmeasured"`
	Tries        []DebugTry             `json:"tries"`
	NightFrames  int                    `json:"night_frames"`
	NightError   string                 `json:"night_error,omitempty"`
	NightPoints  []DebugPoint           `json:"night_points"`
	Scan         map[string][]DebugScan `json:"scan"`
}

// scanSteps is how many scan steps a dip's half width is cut into.
const scanSteps = 6

// DebugTimings is DryTimings with everything each timing is based on,
// writing nothing.
func (s *Service) DebugTimings(ctx context.Context, ids []int) ([]TimingDebug, error) {
	if s.Photometry == nil {
		return nil, errors.New("no photometry: the local Gaia copy is off")
	}
	stars, err := s.Binaries(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []TimingDebug{}
	for i, st := range stars {
		if len(ids) > 0 && !slices.Contains(ids, st.Source.ID) {
			continue
		}
		subs, err := s.Photometry.Subs(ctx, st.Source.Object)
		if err != nil {
			return out, err
		}
		for _, ev := range events(i, st, subs, nil, now) {
			out = append(out, s.debugEvent(ctx, st, ev, subs))
		}
	}
	return out, nil
}

func (s *Service) debugEvent(ctx context.Context, st Star, ev event, subs []ownimage.Sub) TimingDebug {
	e, _ := st.ephemeris()
	ra, dec := st.Facts.Gaia.Now(ev.utc)
	d := dipOf(st, ev)
	out := TimingDebug{SourceID: st.Source.ID, Kind: ev.kind, Night: nightOf(ev.utc, s.loc()), PredictedUTC: ev.utc,
		PredictedBJD: bjdNum(ev.mid), Cycle: ev.cycle, Ephemeris: e, Dip: ev.dip, HalfMinutes: num(ev.half * 1440),
		ModelDepth: num(e.Predict(ev.mid, "")), GaiaG: num(st.Facts.Gaia.G), Frames: len(ev.subs), Scan: map[string][]DebugScan{}}
	minutes := func(b float64) num { return num((b - ev.mid) * 1440) }
	curve, err := s.Photometry.Curve(ctx, st.Source.Object, spotOf(st, ev), ev.subs)
	switch {
	case errors.Is(err, ErrNoCurve):
		out.Note = "Not timed: the frames gave the star no light curve to time."
	case err != nil:
		out.Error = err.Error()
	default:
		out.Comps, out.Dropped = curve.Comps, curve.Dropped
		out.Points = debugPoints(curve, minutes)
		got := map[int]bool{}
		for _, p := range curve.Points {
			got[p.FrameID] = true
		}
		for _, sub := range ev.subs {
			if !got[sub.FrameID] {
				b := lc.BJD(midOf(sub), ra, dec)
				out.Unmeasured = append(out.Unmeasured, DebugPoint{Frame: sub.FrameID, Filter: sub.Filter, BJD: bjdNum(b), Minutes: minutes(b)})
			}
		}
		for _, a := range attempts(curve, d) {
			out.Tries = append(out.Tries, debugTry(a, ev.mid))
		}
		if m, filter, why, ok := bestMinimum(curve, d); ok {
			out.Note = "Timed in " + filter + ": " + earlyLate((m.T-ev.mid)*1440) + "."
		} else {
			out.Note = "Not timed: " + why + "." + unmeasured(curve, ev, st)
		}
	}
	var night []ownimage.Sub
	for _, sub := range subs {
		if slices.ContainsFunc(ev.subs, func(o ownimage.Sub) bool { return o.Night.Equal(sub.Night) }) {
			night = append(night, sub)
		}
	}
	out.NightFrames = len(night)
	nc, err := s.Photometry.Curve(ctx, st.Source.Object, spotOf(st, ev), night)
	if err != nil {
		out.NightError = err.Error()
		return out
	}
	out.NightPoints = debugPoints(nc, minutes)
	byFilter := map[string][]minima.Point{}
	for _, p := range nc.Points {
		byFilter[p.Filter] = append(byFilter[p.Filter], minima.Point{T: p.BJD, Y: p.DMag, E: p.Err})
	}
	for f, pts := range byFilter {
		for _, r := range minima.Scan(pts, d, ev.half/scanSteps) {
			out.Scan[f] = append(out.Scan[f], DebugScan{Minutes: num(r.Offset * 1440), Depth: num(r.Depth), Err: num(r.Err), Chi: num(r.Chi), InDip: r.InDip})
		}
	}
	return out
}

func debugPoints(c *ownimage.Curve, minutes func(float64) num) []DebugPoint {
	out := make([]DebugPoint, len(c.Points))
	for i, p := range c.Points {
		dm, e := num(p.DMag), num(p.Err)
		out[i] = DebugPoint{Frame: p.FrameID, Filter: p.Filter, BJD: bjdNum(p.BJD), Minutes: minutes(p.BJD), DMag: &dm, Err: &e}
	}
	return out
}

func debugTry(a attempt, mid float64) DebugTry {
	m := a.Min
	t := DebugTry{Filter: a.Filter, Joint: a.Joint, Points: a.Points, OK: a.OK, Why: a.Why, OCMinutes: num((m.T - mid) * 1440),
		ErrMinutes: num(m.Err * 1440), Formal: num(m.Formal * 1440), Bead: num(m.Bead * 1440), Red: num(m.Red), Depth: num(m.Depth),
		DepthErr: num(m.DepthErr), Width: num(m.Width), Slope: m.Slope, Before: m.Before, After: m.After, Agree: m.Agree}
	if m.T == 0 {
		t.OCMinutes = num(math.NaN())
	}
	if c := m.Check; c != nil {
		oc, ce := num((c.T-mid)*1440), num(c.Err*1440)
		t.CheckOC, t.CheckErr = &oc, &ce
	}
	return t
}
