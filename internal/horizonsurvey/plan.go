package horizonsurvey

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	HardMinSunSep  = 45.0
	HardMaxSunAlt  = -3.0
	HardMinAlt     = 0.0
	maxPathMargin  = 5.0
	pathStep       = 0.5
	maxWaypoint    = 60.0
	sunHiddenBelow = -2.0
	boxStep        = 1.0
)

var (
	ErrUnsafe      = errors.New("unsafe")
	ErrBadConfig   = errors.New("invalid configuration")
	ErrSunTooHigh  = errors.New("the Sun is too high")
	ErrNoSafeRoute = errors.New("no safe route")
)

type AzRange struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

func (r AzRange) Contains(az float64) bool {
	from, to, a := wrap360(r.From), wrap360(r.To), wrap360(az)
	if from <= to {
		return a >= from && a <= to
	}
	return a >= from || a <= to
}

type PlanConfig struct {
	AzStart float64   `json:"az_start"`
	AzStep  float64   `json:"az_step"`
	Exclude []AzRange `json:"exclude,omitempty"`
	AltMin  float64   `json:"alt_min"`
	AltMax  float64   `json:"alt_max"`
	Overlap float64   `json:"overlap"`
	FieldW  float64   `json:"field_w"`
	FieldH  float64   `json:"field_h"`
}

func (p PlanConfig) Validate() error {
	switch {
	case p.AzStep <= 0 || p.AzStep > 90:
		return fmt.Errorf("%w: azimuth step %v must be in (0, 90]", ErrBadConfig, p.AzStep)
	case p.AltMax <= p.AltMin || p.AltMax > 80:
		return fmt.Errorf("%w: altitude range %v..%v (maximum 80)", ErrBadConfig, p.AltMin, p.AltMax)
	case p.Overlap < 0.1 || p.Overlap > 0.8:
		return fmt.Errorf("%w: overlap %v must be in [0.1, 0.8]", ErrBadConfig, p.Overlap)
	case p.FieldW <= 0 || p.FieldH <= 0 || p.FieldW > 30 || p.FieldH > 30:
		return fmt.Errorf("%w: field %v x %v degrees", ErrBadConfig, p.FieldW, p.FieldH)
	}
	return nil
}

func (p PlanConfig) Rows(minAlt float64) []float64 {
	half := math.Min(p.FieldW, p.FieldH) / 2
	step := 2 * half * (1 - p.Overlap)
	first := math.Max(p.AltMin+half, minAlt)
	last := math.Max(p.AltMax-half, first)
	var rows []float64
	for a := first; a < last+1e-9; a += step {
		rows = append(rows, math.Round(a*100)/100)
	}
	if rows[len(rows)-1] < last-1e-6 {
		rows = append(rows, math.Round(last*100)/100)
	}
	return rows
}

func (p PlanConfig) Columns() []float64 {
	n := int(math.Round(360 / p.AzStep))
	cols := make([]float64, 0, n)
	for i := range n {
		az := wrap360(p.AzStart + float64(i)*p.AzStep)
		skip := false
		for _, r := range p.Exclude {
			if r.Contains(az) {
				skip = true
			}
		}
		if !skip {
			cols = append(cols, math.Round(az*100)/100)
		}
	}
	return cols
}

type Safety struct {
	MinSunSep    float64       `json:"min_sun_separation"`
	MaxSunAlt    float64       `json:"max_sun_altitude"`
	MinAlt       float64       `json:"min_altitude"`
	PathMargin   float64       `json:"path_margin"`
	Window       time.Duration `json:"sun_window"`
	MeridianZone float64       `json:"meridian_zone"`
	WaypointLift float64       `json:"waypoint_lift"`
}

func DefaultSafety() Safety {
	return Safety{MinSunSep: 45, MaxSunAlt: -3, MinAlt: 0, PathMargin: 2, Window: 15 * time.Minute, MeridianZone: 20, WaypointLift: 15}
}

func (s Safety) Validate() error {
	switch {
	case s.MinSunSep < HardMinSunSep || s.MinSunSep > 120:
		return fmt.Errorf("%w: Sun separation %v must be in [%v, 120]", ErrBadConfig, s.MinSunSep, HardMinSunSep)
	case s.MaxSunAlt > HardMaxSunAlt:
		return fmt.Errorf("%w: maximum Sun altitude %v must be at most %v", ErrBadConfig, s.MaxSunAlt, HardMaxSunAlt)
	case s.MinAlt < HardMinAlt || s.MinAlt > 60:
		return fmt.Errorf("%w: minimum altitude %v must be in [%v, 60]", ErrBadConfig, s.MinAlt, HardMinAlt)
	case s.PathMargin < 0 || s.PathMargin > maxPathMargin:
		return fmt.Errorf("%w: path margin %v must be in [0, %v]", ErrBadConfig, s.PathMargin, maxPathMargin)
	case s.Window < 5*time.Minute:
		return fmt.Errorf("%w: Sun look-ahead %v must be at least 5m", ErrBadConfig, s.Window)
	case s.MeridianZone < 0 || s.MeridianZone > 45:
		return fmt.Errorf("%w: meridian zone %v must be in [0, 45]", ErrBadConfig, s.MeridianZone)
	case s.WaypointLift < 5 || s.WaypointLift > 40:
		return fmt.Errorf("%w: waypoint lift %v must be in [5, 40]", ErrBadConfig, s.WaypointLift)
	}
	return nil
}

type Mech struct {
	Phi float64
	Psi float64
}

func Home() Mech { return Mech{} }

func MechFor(ha, dec float64, pier string) (Mech, bool) {
	switch pier {
	case PierWest:
		return Mech{Phi: wrap180(ha + 90), Psi: 90 - dec}, true
	case PierEast:
		return Mech{Phi: wrap180(ha - 90), Psi: -(90 - dec)}, true
	default:
		return Mech{}, false
	}
}

func (m Mech) HADec() (float64, float64) {
	if m.Psi >= 0 {
		return wrap180(m.Phi - 90), 90 - m.Psi
	}
	return wrap180(m.Phi + 90), 90 + m.Psi
}

func (m Mech) Pier() string {
	if m.Psi >= 0 {
		return PierWest
	}
	return PierEast
}

func (s Safety) Candidates(ha, dec float64) []Mech {
	var out []Mech
	for _, p := range []string{PierWest, PierEast} {
		m, _ := MechFor(ha, dec, p)
		if math.Abs(m.Phi) <= 90+s.MeridianZone {
			out = append(out, m)
		}
	}
	return out
}

type Checker struct {
	Site   Site
	Safety Safety
	sun    []Vec
	at     time.Time
}

func NewChecker(site Site, s Safety, t time.Time) *Checker {
	c := &Checker{Site: site, Safety: s, at: t}
	for d := time.Duration(0); d <= s.Window; d += 5 * time.Minute {
		az, alt := site.SunAltAz(t.Add(d))
		c.sun = append(c.sun, HorVec(az, alt))
	}
	return c
}

func (c *Checker) SunOK() error {
	for d := time.Duration(0); d <= c.Safety.Window; d += 5 * time.Minute {
		if _, alt := c.Site.SunAltAz(c.at.Add(d)); alt > c.Safety.MaxSunAlt {
			return fmt.Errorf("%w: Sun altitude %.1f° at %s is above the %.1f° limit", ErrSunTooHigh, alt, c.at.Add(d).Format(time.RFC3339), c.Safety.MaxSunAlt)
		}
	}
	return nil
}

func (c *Checker) MinSunSep(v Vec) float64 {
	m := 180.0
	for _, s := range c.sun {
		m = math.Min(m, Separation(v, s))
	}
	return m
}

func (c *Checker) PointOK(az, alt float64) error {
	if alt < c.Safety.MinAlt {
		return fmt.Errorf("%w: altitude %.2f° is below the %.2f° limit", ErrUnsafe, alt, c.Safety.MinAlt)
	}
	if sep := c.MinSunSep(HorVec(az, alt)); sep < c.Safety.MinSunSep {
		return fmt.Errorf("%w: az %.1f alt %.1f comes within %.1f° of the Sun", ErrUnsafe, az, alt, sep)
	}
	return nil
}

func (c *Checker) mechHor(m Mech) Vec {
	ha, dec := m.HADec()
	return EqToHor(EqVec(ha, dec), c.Site.Lat)
}

func (c *Checker) PathOK(a, b Mech) error { return c.pathOK(a, b, true) }

func (c *Checker) HomePathOK(a Mech) error { return c.pathOK(a, Home(), false) }

func ratePath(a, b Mech, k float64) []Mech {
	dphi, dpsi := b.Phi-a.Phi, b.Psi-a.Psi
	tphi, tpsi := math.Abs(dphi), math.Abs(dpsi)/k
	span := math.Max(tphi, tpsi)
	n := int(math.Ceil(span/pathStep)) + 1
	pts := make([]Mech, 0, n+1)
	for i := range n + 1 {
		t := span * float64(i) / float64(n)
		pts = append(pts, Mech{
			Phi: a.Phi + math.Copysign(math.Min(t, tphi), dphi),
			Psi: a.Psi + math.Copysign(math.Min(t*k, math.Abs(dpsi)), dpsi),
		})
	}
	return pts
}

func axisRatios() []float64 { return []float64{0.5, 2.0 / 3, 0.8, 1, 1.25, 1.5, 2} }

func (c *Checker) pathOK(a, b Mech, dip bool) error {
	for _, k := range axisRatios() {
		for _, m := range ratePath(a, b, k) {
			v := c.mechHor(m)
			az, alt := AltAz(v)
			if sep := c.MinSunSep(v); sep < c.Safety.MinSunSep && alt >= sunHiddenBelow {
				return fmt.Errorf("%w: slew path (Dec/RA rate %.2f) passes az %.1f alt %.1f, %.1f° from the Sun", ErrUnsafe, k, az, alt, sep)
			}
			if dip && k >= 0.8 && k <= 1.25 && alt < c.Safety.MinAlt-c.Safety.PathMargin {
				return fmt.Errorf("%w: slew path (Dec/RA rate %.2f) dips to altitude %.1f°", ErrUnsafe, k, alt)
			}
		}
	}
	return c.boxSunOK(a, b)
}

func (c *Checker) boxSunOK(a, b Mech) error {
	nphi := int(math.Ceil(math.Abs(b.Phi-a.Phi)/boxStep)) + 1
	npsi := int(math.Ceil(math.Abs(b.Psi-a.Psi)/boxStep)) + 1
	for i := range nphi + 1 {
		phi := a.Phi + (b.Phi-a.Phi)*float64(i)/float64(nphi)
		for j := range npsi + 1 {
			m := Mech{Phi: phi, Psi: a.Psi + (b.Psi-a.Psi)*float64(j)/float64(npsi)}
			v := c.mechHor(m)
			az, alt := AltAz(v)
			if alt < sunHiddenBelow {
				continue
			}
			if sep := c.MinSunSep(v); sep < c.Safety.MinSunSep {
				return fmt.Errorf("%w: a possible slew path passes az %.1f alt %.1f, %.1f° from the Sun", ErrUnsafe, az, alt, sep)
			}
		}
	}
	return nil
}

type Waypoint struct {
	Az, Alt float64
	Survey  bool
	Home    bool
}

func (c *Checker) legOK(from Mech, az, alt float64) error {
	if err := c.PointOK(az, alt); err != nil {
		return err
	}
	ha, dec := c.Site.AltAzToHADec(az, alt)
	cands := c.Safety.Candidates(ha, dec)
	if len(cands) == 0 {
		return fmt.Errorf("%w: az %.1f alt %.1f has no reachable pier side", ErrUnsafe, az, alt)
	}
	for _, m := range cands {
		if err := c.PathOK(from, m); err != nil {
			return err
		}
		if err := c.HomePathOK(m); err != nil {
			return fmt.Errorf("return home from az %.1f alt %.1f: %w", az, alt, err)
		}
	}
	return nil
}

func (c *Checker) Route(from Mech, az, alt float64) ([]Waypoint, error) {
	return c.RouteOn(from, az, alt, "")
}

func (c *Checker) RouteOn(from Mech, az, alt float64, pier string) ([]Waypoint, error) {
	if err := c.PointOK(az, alt); err != nil {
		return nil, err
	}
	direct := c.legOK(from, az, alt)
	if direct == nil {
		return []Waypoint{{Az: az, Alt: alt, Survey: true}}, nil
	}
	fha, fdec := from.HADec()
	faz, falt := c.Site.HADecToAltAz(fha, fdec)
	lift := math.Min(math.Max(falt, alt)+c.Safety.WaypointLift, maxWaypoint)
	errs := []error{direct}
	for i, wAz := range []float64{faz, az} {
		wAlt := lift
		side := pier
		if i == 0 {
			side = from.Pier()
		}
		if a, b, _, _, ok := c.Site.Aim(wAz, wAlt, side); ok && pier != "" {
			wAz, wAlt = a, b
		}
		if err := c.legOK(from, wAz, wAlt); err != nil {
			errs = append(errs, fmt.Errorf("to waypoint az %.1f alt %.1f: %w", wAz, wAlt, err))
			continue
		}
		ha, dec := c.Site.AltAzToHADec(wAz, wAlt)
		var legErr error
		for _, w := range c.Safety.Candidates(ha, dec) {
			if err := c.legOK(w, az, alt); err != nil {
				legErr = err
				break
			}
		}
		if legErr != nil {
			errs = append(errs, fmt.Errorf("from waypoint az %.1f alt %.1f: %w", wAz, wAlt, legErr))
			continue
		}
		return []Waypoint{{Az: wAz, Alt: wAlt}, {Az: az, Alt: alt, Survey: true}}, nil
	}
	for _, w := range c.sideWaypoints(from) {
		waz, walt := c.Site.HADecToAltAz(w.HADec())
		if err := c.legOK(from, waz, walt); err != nil {
			errs = append(errs, fmt.Errorf("to side waypoint az %.1f alt %.1f: %w", waz, walt, err))
			continue
		}
		if err := c.legOK(w, az, alt); err != nil {
			errs = append(errs, fmt.Errorf("from side waypoint az %.1f alt %.1f: %w", waz, walt, err))
			continue
		}
		return []Waypoint{{Az: waz, Alt: walt}, {Az: az, Alt: alt, Survey: true}}, nil
	}
	if err := c.HomePathOK(from); err != nil {
		errs = append(errs, fmt.Errorf("via home: %w", err))
	} else if err := c.legOK(Home(), az, alt); err != nil {
		errs = append(errs, fmt.Errorf("from home: %w", err))
	} else {
		return []Waypoint{{Home: true}, {Az: az, Alt: alt, Survey: true}}, nil
	}
	return nil, fmt.Errorf("%w to az %.1f alt %.1f: %w", ErrNoSafeRoute, az, alt, errors.Join(errs...))
}

const (
	PierGuard       = 1.5
	DefaultSlewRate = 3.0
	slewSettle      = time.Second
)

func NaturalPier(ha float64) string {
	if wrap180(ha) < 0 {
		return PierWest
	}
	return PierEast
}

func (s Site) Aim(az, alt float64, pier string) (aimAz, aimAlt, ha, dec float64, ok bool) {
	ha, dec = s.AltAzToHADec(az, alt)
	ha = wrap180(ha)
	sign := -1.0
	switch pier {
	case PierWest:
	case PierEast:
		sign = 1
	default:
		return az, alt, ha, dec, true
	}
	switch {
	case math.Abs(ha) < PierGuard:
		ha = sign * PierGuard
	case math.Abs(ha) > 180-PierGuard:
		ha = sign * (180 - PierGuard)
	case sign*ha < 0:
		return az, alt, ha, dec, false
	default:
		return az, alt, ha, dec, true
	}
	dec = s.decAtAltitude(alt, ha, dec)
	aimAz, aimAlt = s.HADecToAltAz(ha, dec)
	return aimAz, aimAlt, ha, dec, true
}

func (s Site) decAtAltitude(alt, ha, near float64) float64 {
	a, b := math.Sin(s.Lat*rad), math.Cos(s.Lat*rad)*math.Cos(ha*rad)
	r := math.Hypot(a, b)
	x := math.Sin(alt*rad) / r
	if math.Abs(x) > 1 {
		return near
	}
	phi := math.Atan2(b, a)
	best := near
	bestD := math.Inf(1)
	for _, d := range []float64{math.Asin(x) - phi, math.Pi - math.Asin(x) - phi} {
		dec := wrap180(d / rad)
		if dec < -90 || dec > 90 {
			continue
		}
		if dd := math.Abs(dec - near); dd < bestD {
			best, bestD = dec, dd
		}
	}
	return best
}

func (s Site) ColumnPiers(az float64, rows []float64) []string {
	var out []string
	for _, p := range []string{PierWest, PierEast} {
		ok := true
		for _, alt := range rows {
			if _, _, _, _, k := s.Aim(az, alt, p); !k {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func ChoosePier(feasible []string, prev string) string {
	for _, p := range feasible {
		if p == prev {
			return p
		}
	}
	if len(feasible) > 0 {
		return feasible[0]
	}
	return ""
}

type Order struct {
	Columns   []float64
	Piers     []string
	Flips     int
	Reachable int
	Slew      time.Duration
}

func cyclicOrders(cols []float64) [][]float64 {
	asc := slices.Clone(cols)
	slices.SortFunc(asc, func(a, b float64) int { return cmp.Compare(wrap360(a), wrap360(b)) })
	split := len(asc)
	for i, az := range asc {
		if wrap360(az) >= 180 {
			split = i
			break
		}
	}
	south := append(slices.Clone(asc[split:]), asc[:split]...)
	out := make([][]float64, 0, 4)
	for _, o := range [][]float64{asc, south} {
		out = append(out, o)
		r := slices.Clone(o)
		slices.Reverse(r)
		out = append(out, r)
	}
	return out
}

func better(a, b Order) bool {
	switch {
	case a.Flips != b.Flips:
		return a.Flips < b.Flips
	case a.Reachable != b.Reachable:
		return a.Reachable > b.Reachable
	}
	return a.Slew < b.Slew
}

func repAltitude(rows []float64, want float64) float64 {
	best := rows[0]
	for _, a := range rows {
		if math.Abs(a-want) < math.Abs(best-want) {
			best = a
		}
	}
	return best
}

func ChooseOrder(site Site, s Safety, cols, rows []float64, start time.Time, from Mech, perColumn time.Duration) Order {
	var best Order
	alt := repAltitude(rows, rows[0]+5)
	first := true
	for _, cand := range cyclicOrders(cols) {
		o := Order{Columns: cand}
		pier := from.Pier()
		if from == Home() {
			pier = ""
			for _, az := range cand {
				if f := site.ColumnPiers(az, rows); len(f) == 1 {
					pier = f[0]
					break
				}
			}
		}
		at := from
		for ci, az := range cand {
			next := ChoosePier(site.ColumnPiers(az, rows), pier)
			o.Piers = append(o.Piers, next)
			if next == "" {
				continue
			}
			if next != pier && (ci != 0 || from != Home()) {
				o.Flips++
			}
			pier = next
			aAz, aAlt, ha, dec, ok := site.Aim(az, alt, next)
			if !ok {
				continue
			}
			ck := NewChecker(site, s, start.Add(time.Duration(ci)*perColumn))
			if _, err := ck.RouteOn(at, aAz, aAlt, next); err != nil {
				continue
			}
			if m, ok := MechFor(ha, dec, next); ok {
				o.Slew += SlewTime(at, m, DefaultSlewRate)
				at = m
			}
			o.Reachable++
		}
		if first || better(o, best) {
			best, first = o, false
		}
	}
	return best
}

func SlewTime(a, b Mech, rate float64) time.Duration {
	if rate <= 0 {
		rate = DefaultSlewRate
	}
	travel := math.Max(math.Abs(b.Phi-a.Phi), math.Abs(b.Psi-a.Psi))
	return time.Duration(travel/rate*float64(time.Second)) + slewSettle
}

func (c *Checker) sideWaypoints(from Mech) []Mech {
	if from == Home() {
		return nil
	}
	ha, dec := from.HADec()
	edge := c.Safety.MeridianZone + 5
	pier := from.Pier()
	sign := -1.0
	if pier == PierEast {
		sign = 1
	}
	wha := sign * math.Min(math.Max(sign*ha, edge), 180-edge)
	var out []Mech
	for _, lift := range []float64{c.Safety.WaypointLift, 2 * c.Safety.WaypointLift, 3 * c.Safety.WaypointLift} {
		wdec := math.Min(dec+lift, 85)
		m, _ := MechFor(wha, wdec, pier)
		if len(c.Safety.Candidates(wha, wdec)) != 1 {
			continue
		}
		if _, alt := c.Site.HADecToAltAz(wha, wdec); alt < c.Safety.MinAlt+5 {
			continue
		}
		out = append(out, m)
	}
	return out
}

type PlannedPosition struct {
	Column, Row   int
	Az, Alt       float64
	AimAz, AimAlt float64
	HA, Dec       float64
	Pier          string
	Piers         []string
	SunSep        float64
	Slew          time.Duration
	Flip          bool
	Err           error
}

type Plan struct {
	Start     time.Time
	Columns   []float64
	Rows      []float64
	Positions []PlannedPosition
	SunAz     float64
	SunAlt    float64
	SunErr    error
	PerFrame  time.Duration
	Flips     int
	SlewRate  float64
}

func MakePlan(site Site, pc PlanConfig, s Safety, start time.Time, perFrame time.Duration) (*Plan, error) {
	if err := pc.Validate(); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	p := &Plan{Start: start, Rows: pc.Rows(s.MinAlt), PerFrame: perFrame, SlewRate: DefaultSlewRate}
	p.SunAz, p.SunAlt = site.SunAltAz(start)
	p.SunErr = NewChecker(site, s, start).SunOK()
	perColumn := time.Duration(3) * perFrame
	from := Home()
	order := ChooseOrder(site, s, pc.Columns(), p.Rows, start, from, perColumn)
	p.Columns, p.Flips = order.Columns, order.Flips
	pier := ""
	for ci, az := range p.Columns {
		ck := NewChecker(site, s, start.Add(time.Duration(ci)*perColumn))
		next := order.Piers[ci]
		flip := next != "" && pier != "" && next != pier
		if next != "" {
			pier = next
		}
		for k := range p.Rows {
			ri := len(p.Rows) - 1 - k
			if ci%2 == 1 {
				ri = k
			}
			alt := p.Rows[ri]
			pp := PlannedPosition{Column: ci, Row: ri, Az: az, Alt: alt, Pier: next, Flip: flip && k == 0}
			var ok bool
			pp.AimAz, pp.AimAlt, pp.HA, pp.Dec, ok = site.Aim(az, alt, next)
			pp.SunSep = ck.MinSunSep(HorVec(pp.AimAz, pp.AimAlt))
			for _, m := range s.Candidates(pp.HA, pp.Dec) {
				pp.Piers = append(pp.Piers, m.Pier())
			}
			switch {
			case !ok || next == "":
				pp.Err = fmt.Errorf("%w: az %.1f alt %.1f cannot be reached on the column's pier side", ErrUnsafe, az, alt)
			default:
				_, pp.Err = ck.RouteOn(from, pp.AimAz, pp.AimAlt, next)
			}
			if pp.Err == nil {
				if m, ok := MechFor(pp.HA, pp.Dec, next); ok {
					pp.Slew = SlewTime(from, m, p.SlewRate)
					from = m
				}
			}
			p.Positions = append(p.Positions, pp)
		}
	}
	return p, nil
}

func (p *Plan) Write(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Plan at %s: Sun az %.1f alt %.1f\n", p.Start.Format(time.RFC3339), p.SunAz, p.SunAlt)
	if p.SunErr != nil {
		_, _ = fmt.Fprintf(w, "REFUSE: %v\n", p.SunErr)
	}
	_, _ = fmt.Fprintf(w, "%d columns x %d rows; rows at %v\n", len(p.Columns), len(p.Rows), p.Rows)
	_, _ = fmt.Fprintf(w, "column order %v; %d pier flip(s), each between columns\n", p.Columns, p.Flips)
	_, _ = fmt.Fprintf(w, "%-4s %-4s %7s %6s %7s %6s %8s %7s %-9s %-18s %7s %6s %s\n", "col", "row", "az", "alt", "aim az", "aim", "HA", "dec", "pier", "possible", "sunsep", "slew", "status")
	ok := 0
	var slews []time.Duration
	var flipSlew time.Duration
	for _, pp := range p.Positions {
		status := "OK"
		if pp.Err != nil {
			status = "SKIP " + pp.Err.Error()
		} else {
			ok++
			if pp.Flip {
				flipSlew = max(flipSlew, pp.Slew)
				status += " (pier flip)"
			} else {
				slews = append(slews, pp.Slew)
			}
		}
		_, _ = fmt.Fprintf(w, "%-4d %-4d %7.2f %6.2f %7.2f %6.2f %8.2f %7.2f %-9s %-18s %7.1f %5.0fs %s\n", pp.Column, pp.Row, pp.Az, pp.Alt, pp.AimAz, pp.AimAlt, pp.HA, pp.Dec, pp.Pier, strings.Join(pp.Piers, ","), pp.SunSep, pp.Slew.Seconds(), status)
	}
	slices.Sort(slews)
	typical := time.Duration(0)
	if len(slews) > 0 {
		typical = slews[len(slews)/2]
	}
	adaptive := time.Duration(len(p.Columns)*3)*p.PerFrame + time.Duration(p.Flips)*flipSlew
	full := time.Duration(ok)*p.PerFrame + time.Duration(p.Flips)*flipSlew
	_, _ = fmt.Fprintf(w, "%d of %d positions pass the safety checks.\n", ok, len(p.Positions))
	_, _ = fmt.Fprintf(w, "Slew per frame at %.0f deg/s: typically %.0fs, the pier flip %.0fs. With %s per frame: about %s adaptive (3 frames per column), at most %s for every safe position.\n",
		p.SlewRate, typical.Seconds(), flipSlew.Seconds(), p.PerFrame, adaptive.Round(time.Minute), full.Round(time.Minute))
}
