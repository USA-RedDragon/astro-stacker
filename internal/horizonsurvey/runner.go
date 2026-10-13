package horizonsurvey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

const (
	ModeAuto          = "auto"
	nightSunAlt       = -12.0
	pointingTolerance = 0.5
	siteTolerance     = 0.05
	clockTolerance    = 0.5
	homeAttempts      = 3
	homeTimeout       = 4 * time.Minute
	maxRetakes        = 6
)

var (
	ErrPreflight    = errors.New("preflight check failed")
	ErrRoofUnsafe   = errors.New("the safety monitor reports unsafe")
	ErrSequence     = errors.New("a NINA sequence is running; stop it before the survey")
	ErrPointing     = errors.New("the mount is not where it was sent")
	ErrMeter        = errors.New("could not meter the sky")
	ErrHomeUnsafe   = errors.New("the path home is not Sun-safe; the mount was left stopped where it is")
	ErrFrameLimit   = errors.New("frame limit reached")
	ErrNoPixelScale = errors.New("PHD2 does not know its pixel scale; pass -pixel-scale")
	ErrPier         = errors.New("the mount is on a pier side the safety checks did not cover")
	ErrSite         = errors.New("the mount's site or clock does not match")
	ErrStopped      = errors.New("stopped on request")
	ErrStopFile     = errors.New("a STOP file is already in the output folder; remove it to start")
)

const (
	StopFile     = "STOP"
	coarseStep   = 3
	estPerColumn = 45 * time.Second
	stopPoll     = time.Second
)

type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type Exposure struct {
	MinMS, MaxMS, StartMS int
	Target                float64
}

func DefaultExposure(mode string) Exposure {
	if mode == ModeNight {
		return Exposure{MinMS: 500, MaxMS: 2000, StartMS: 2000, Target: 0.12}
	}
	return Exposure{MinMS: 1, MaxMS: 2000, StartMS: 5, Target: 0.4}
}

type RunConfig struct {
	Out        string
	Mode       string
	Plan       PlanConfig
	Safety     Safety
	Settle     time.Duration
	PixelScale float64
	Exposure   *Exposure
	MaxFrames  int
	Full       bool
	Site       *Site
	Astap      string
	Log        io.Writer
	StopPoll   time.Duration
}

type Runner struct {
	cfg         RunConfig
	nina        *NINA
	phd         *PHD2
	clock       Clock
	site        Site
	mode        string
	exp         Exposure
	mech        Mech
	moved       bool
	skyRate     float64
	starDensity float64
	man         *Manifest
	header      RunHeader
	frames      int
	startRow    int
	side        string
}

func NewRunner(cfg RunConfig, nina *NINA, phd *PHD2, clock Clock) *Runner {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	return &Runner{cfg: cfg, nina: nina, phd: phd, clock: clock}
}

func (r *Runner) logf(format string, a ...any) {
	_, _ = fmt.Fprintf(r.cfg.Log, "%s "+format+"\n", append([]any{r.clock.Now().UTC().Format("15:04:05Z")}, a...)...)
}

func (r *Runner) Preflight(ctx context.Context) error {
	if err := r.cfg.Safety.Validate(); err != nil {
		return err
	}
	mi, err := r.nina.MountInfo(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	switch {
	case !mi.Connected:
		return fmt.Errorf("%w: the mount is not connected in NINA", ErrPreflight)
	case mi.AtPark:
		return fmt.Errorf("%w: the mount is parked; this tool does not unpark it", ErrPreflight)
	case mi.Slewing:
		return fmt.Errorf("%w: the mount is slewing", ErrPreflight)
	case mi.SiteLatitude == 0 && mi.SiteLongitude == 0:
		return fmt.Errorf("%w: the mount reports no site coordinates", ErrPreflight)
	}
	r.site = Site{Lat: mi.SiteLatitude, Lon: mi.SiteLongitude}
	if err := r.checkSite(mi); err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	if err := r.checkSequence(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	if err := r.checkRoof(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	st, err := r.phd.AppState(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	if !phd2Idle(st) && !phd2Stoppable(st) {
		return fmt.Errorf("%w: %w (state %s)", ErrPreflight, ErrPHD2Busy, st)
	}
	scale := r.cfg.PixelScale
	if scale <= 0 {
		if scale, err = r.phd.PixelScale(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrPreflight, err)
		}
	}
	if scale <= 0 {
		return fmt.Errorf("%w: %w", ErrPreflight, ErrNoPixelScale)
	}
	w, h, err := r.phd.FrameSize(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	r.cfg.Plan.FieldW, r.cfg.Plan.FieldH = float64(w)*scale/3600, float64(h)*scale/3600
	if err := r.cfg.Plan.Validate(); err != nil {
		return err
	}
	now := r.clock.Now()
	ck := NewChecker(r.site, r.cfg.Safety, now)
	if err := ck.SunOK(); err != nil {
		return fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	_, sunAlt := r.site.SunAltAz(now)
	r.mode = r.cfg.Mode
	if r.mode == ModeAuto || r.mode == "" {
		r.mode = ModeDusk
		if sunAlt <= nightSunAlt {
			r.mode = ModeNight
		}
	}
	if r.cfg.Exposure != nil {
		r.exp = *r.cfg.Exposure
	} else {
		r.exp = DefaultExposure(r.mode)
	}
	pier := string(mi.SideOfPier)
	ha := wrap180((mi.SiderealTime - mi.RightAscension) * 15)
	if mi.AtHome {
		r.mech = Home()
	} else if m, ok := MechFor(ha, mi.Declination, pier); ok {
		r.mech = m
	} else {
		return fmt.Errorf("%w: the mount reports pier side %q and is not at home", ErrPreflight, pier)
	}
	r.header = RunHeader{Started: now, Site: r.site, Mode: r.mode, PixelScale: scale, FrameW: w, FrameH: h, PHD2Version: r.phd.Version, Plan: r.cfg.Plan, Safety: r.cfg.Safety}
	r.logf("site %.4f %.4f, Sun alt %.1f, mode %s, PHD2 %s, field %.2f x %.2f deg at %.2f arcsec/px", r.site.Lat, r.site.Lon, sunAlt, r.mode, r.phd.Version, r.cfg.Plan.FieldW, r.cfg.Plan.FieldH, scale)
	if hz, err := r.nina.Horizon(ctx); err == nil {
		r.logf("NINA profile horizon has %d points", len(hz.Altitudes))
	}
	return nil
}

func (r *Runner) checkSite(mi MountInfo) error {
	if want := r.cfg.Site; want != nil {
		if math.Abs(want.Lat-mi.SiteLatitude) > siteTolerance || math.Abs(wrap180(want.Lon-mi.SiteLongitude)) > siteTolerance {
			return fmt.Errorf("%w: the mount reports %.4f %.4f, expected %.4f %.4f", ErrSite, mi.SiteLatitude, mi.SiteLongitude, want.Lat, want.Lon)
		}
	}
	if d := math.Abs(wrap180(r.site.LST(r.clock.Now()) - mi.SiderealTime*15)); d > clockTolerance {
		return fmt.Errorf("%w: local sidereal time differs from the mount's by %.2f°; check the PC clock and the mount time", ErrSite, d)
	}
	return nil
}

func (r *Runner) checkSequence(ctx context.Context) error {
	running, err := r.nina.SequenceRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return ErrSequence
	}
	return nil
}

func (r *Runner) checkRoof(ctx context.Context) error {
	s, err := r.nina.Safety(ctx)
	if err != nil {
		return err
	}
	if !s.Connected {
		return fmt.Errorf("%w: the safety monitor is not connected, so the roof state is unknown", ErrRoofUnsafe)
	}
	if !s.IsSafe {
		return ErrRoofUnsafe
	}
	return nil
}

func (r *Runner) stopPath() string { return filepath.Join(r.cfg.Out, StopFile) }

func (r *Runner) checkStop() error {
	if _, err := os.Stat(r.stopPath()); err == nil {
		return fmt.Errorf("%w: %s exists", ErrStopped, r.stopPath())
	}
	return nil
}

func (r *Runner) watchStop(ctx context.Context, cancel context.CancelCauseFunc) {
	poll := r.cfg.StopPoll
	if poll <= 0 {
		poll = stopPoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.checkStop(); err != nil {
				cancel(err)
				return
			}
		}
	}
}

func (r *Runner) Run(parent context.Context) (err error) {
	if err := r.Preflight(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(r.cfg.Out, 0o750); err != nil {
		return err
	}
	if r.checkStop() != nil {
		return fmt.Errorf("%w: %s", ErrStopFile, r.stopPath())
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	go r.watchStop(ctx, cancel)
	if err := WriteJSON(filepath.Join(r.cfg.Out, RunFile), r.header); err != nil {
		return err
	}
	if r.man, err = CreateManifest(r.cfg.Out); err != nil {
		return err
	}
	defer func() {
		if cause := context.Cause(ctx); err != nil && cause != nil && errors.Is(err, context.Canceled) {
			err = cause
		}
		_ = r.man.Close()
		ferr := r.finish(ctx, err)
		r.header.Ended = r.clock.Now()
		r.header.Outcome = "complete"
		if err != nil {
			r.header.Outcome = err.Error()
		}
		if ferr != nil {
			r.header.Outcome += "; " + ferr.Error()
			err = errors.Join(err, ferr)
		}
		_ = WriteJSON(filepath.Join(r.cfg.Out, RunFile), r.header)
	}()
	r.logf("to stop cleanly, create the file %s (or type stop and Enter)", r.stopPath())
	if err := r.phd.EnsureIdle(ctx); err != nil {
		return err
	}
	rows := r.cfg.Plan.Rows(r.cfg.Safety.MinAlt)
	r.startRow = -1
	order := ChooseOrder(r.site, r.cfg.Safety, r.cfg.Plan.Columns(), rows, r.clock.Now(), r.mech, estPerColumn)
	r.logf("column order %v, %d pier flip(s) planned, %d of %d columns reachable from here now", order.Columns, order.Flips, order.Reachable, len(order.Columns))
	r.side = ""
	for _, p := range order.Piers {
		if p != "" {
			r.side = p
			break
		}
	}
	if r.mech != Home() {
		r.side = r.mech.Pier()
	}
	for ci, az := range order.Columns {
		if err := r.checkStop(); err != nil {
			return err
		}
		if err := r.checkSequence(ctx); err != nil {
			return err
		}
		pier := ChoosePier(r.site.ColumnPiers(az, rows), r.side)
		if pier == "" {
			r.logf("skip column az %.1f: no single pier side reaches every row", az)
			continue
		}
		if r.side != "" && pier != r.side {
			r.logf("column az %.1f is on %s; the mount flips once here (it was on %s)", az, pier, r.side)
		}
		r.side = pier
		if r.cfg.Full {
			err = r.fullColumn(ctx, ci, az, rows, pier)
		} else {
			err = r.adaptiveColumn(ctx, ci, az, rows, pier)
		}
		if err != nil {
			return err
		}
	}
	r.logf("survey complete: %d frames", r.frames)
	return nil
}

func skippable(err error) bool { return errors.Is(err, ErrUnsafe) || errors.Is(err, ErrNoSafeRoute) }

func (r *Runner) followMount(az float64, rows []float64, pier string) string {
	if r.side == "" || r.side == pier || !slices.Contains(r.site.ColumnPiers(az, rows), r.side) {
		return pier
	}
	return r.side
}

func (r *Runner) fullColumn(ctx context.Context, ci int, az float64, rows []float64, pier string) error {
	for i := range rows {
		k := len(rows) - 1 - i
		if ci%2 == 1 {
			k = i
		}
		pier = r.followMount(az, rows, pier)
		if _, err := r.shoot(ctx, ci, k, az, rows[k], pier); err != nil && !skippable(err) {
			return err
		}
	}
	return nil
}

func topObstructed(seen map[int]string, n int) int {
	for row := n - 1; row >= 0; row-- {
		if c := seen[row]; c == ClassEdge || c == ClassObst {
			return row
		}
	}
	return -1
}

func NextRow(seen map[int]string, n int, coarse bool) (int, bool) {
	top := topObstructed(seen, n)
	if top < 0 {
		low := n
		for row := range seen {
			low = min(low, row)
		}
		if low <= 0 {
			return 0, true
		}
		step := 2
		if coarse {
			step = coarseStep
		}
		return max(0, low-step), false
	}
	if top < n-1 && seen[top+1] == "" {
		return top + 1, false
	}
	if seen[top] == ClassObst {
		return 0, true
	}
	for j := 1; j <= 2; j++ {
		row := top - j
		if row < 0 {
			return 0, true
		}
		switch seen[row] {
		case "":
			return row, false
		case ClassObst:
			return 0, true
		}
	}
	return 0, true
}

func (r *Runner) adaptiveColumn(ctx context.Context, ci int, az float64, rows []float64, pier string) error {
	n := len(rows)
	coarse := r.startRow < 0
	k := n - 1
	if !coarse {
		k = min(r.startRow, n-1)
	}
	seen := map[int]string{}
	for range n {
		pier = r.followMount(az, rows, pier)
		cls, err := r.shoot(ctx, ci, k, az, rows[k], pier)
		if skippable(err) && len(seen) == 0 {
			if next := k + 1; !coarse && next < n {
				k = next
				continue
			}
			if next := k - 1; coarse && next >= 0 {
				k = next
				continue
			}
		}
		if skippable(err) {
			break
		}
		if err != nil {
			return err
		}
		seen[k] = cls
		next, done := NextRow(seen, n, coarse)
		if done {
			break
		}
		k = next
	}
	if len(seen) == 0 {
		return nil
	}
	if top := topObstructed(seen, n); top >= 0 {
		r.startRow = top
		return nil
	}
	r.startRow = n - 1
	for row := range seen {
		r.startRow = min(r.startRow, row)
	}
	return nil
}

func (r *Runner) slewTo(ctx context.Context, az, alt float64, survey bool) error {
	ra, dec := r.site.AltAzToJ2000(az, alt, r.clock.Now())
	r.logf("slew to az %.2f alt %.2f (J2000 RA %.4f° Dec %.4f°)", az, alt, ra, dec)
	r.moved = true
	if err := r.nina.Slew(ctx, ra, dec); err != nil {
		return err
	}
	if err := r.nina.TrackingOff(ctx); err != nil {
		return err
	}
	mi, err := r.nina.MountInfo(ctx)
	if err != nil {
		return err
	}
	ha := wrap180((mi.SiderealTime - mi.RightAscension) * 15)
	maz, malt := r.site.HADecToAltAz(ha, mi.Declination)
	if sep := Separation(HorVec(maz, malt), HorVec(az, alt)); sep > pointingTolerance || mi.Slewing {
		return fmt.Errorf("%w: commanded az %.2f alt %.2f, mount reports az %.2f alt %.2f (slewing %v)", ErrPointing, az, alt, maz, malt, mi.Slewing)
	}
	m, ok := MechFor(ha, mi.Declination, string(mi.SideOfPier))
	if !ok {
		return fmt.Errorf("%w: the mount reports pier side %q", ErrPier, mi.SideOfPier)
	}
	r.mech = m
	tha, tdec := r.site.AltAzToHADec(az, alt)
	for _, c := range r.cfg.Safety.Candidates(tha, tdec) {
		if c.Pier() == m.Pier() {
			if survey && r.side != "" && m.Pier() != r.side {
				r.logf("the mount chose %s, the plan expected %s; later columns follow the mount", m.Pier(), r.side)
				r.side = m.Pier()
			}
			return nil
		}
	}
	return fmt.Errorf("%w: the mount chose %s at az %.2f alt %.2f", ErrPier, m.Pier(), az, alt)
}

func (r *Runner) shoot(ctx context.Context, ci, row int, az, alt float64, pier string) (string, error) {
	if r.cfg.MaxFrames > 0 && r.frames >= r.cfg.MaxFrames {
		return "", ErrFrameLimit
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.checkStop(); err != nil {
		return "", err
	}
	now := r.clock.Now()
	ck := NewChecker(r.site, r.cfg.Safety, now)
	if err := ck.SunOK(); err != nil {
		return "", err
	}
	if err := r.checkRoof(ctx); err != nil {
		return "", err
	}
	if err := r.checkSequence(ctx); err != nil {
		return "", err
	}
	aimAz, aimAlt, _, _, ok := r.site.Aim(az, alt, pier)
	if !ok {
		err := fmt.Errorf("%w: az %.2f alt %.2f is not reachable on %s", ErrUnsafe, az, alt, pier)
		r.logf("skip: %v", err)
		return "", err
	}
	route, err := ck.RouteOn(r.mech, aimAz, aimAlt, pier)
	if err != nil {
		r.logf("skip az %.2f alt %.2f: %v", aimAz, aimAlt, err)
		return "", err
	}
	for _, wp := range route {
		if wp.Home {
			if err := r.homeWaypoint(ctx); err != nil {
				return "", err
			}
			continue
		}
		if err := r.slewTo(ctx, wp.Az, wp.Alt, wp.Survey); err != nil {
			return "", err
		}
	}
	if err := r.clock.Sleep(ctx, r.cfg.Settle); err != nil {
		return "", err
	}
	return r.expose(ctx, shot{ci: ci, row: row, az: az, alt: alt, aimAz: aimAz, aimAlt: aimAlt, pier: pier})
}

type shot struct {
	ci, row                int
	az, alt, aimAz, aimAlt float64
	pier                   string
}

func (r *Runner) exposureMS() int {
	if r.skyRate <= 0 {
		return r.exp.StartMS
	}
	return int(math.Round(math.Min(float64(r.exp.MaxMS), math.Max(float64(r.exp.MinMS), r.exp.Target/r.skyRate))))
}

func (r *Runner) expose(ctx context.Context, p shot) (string, error) {
	exp := r.exposureMS()
	for attempt := 0; ; attempt++ {
		mi, err := r.nina.MountInfo(ctx)
		if err != nil {
			return "", err
		}
		start := r.clock.Now()
		name := fmt.Sprintf("hs_%03d_%02d_%02d_%dms.fits", p.ci, p.row, attempt, exp)
		path := filepath.Join(r.cfg.Out, name)
		if err := r.phd.Capture(ctx, exp, path); err != nil {
			return "", err
		}
		im, _, err := LoadFrame(path)
		if err != nil {
			return "", err
		}
		skyRef := r.skyRate * float64(exp)
		seg := Segment(im.Plane(0), im.W, im.H, SegmentOptions{Mode: r.mode, SkyRef: skyRef, StarDensity: r.starDensity})
		retake := 0
		skySaturated := seg.Saturated && (r.mode != ModeNight || seg.Median >= saturated)
		switch {
		case skySaturated && exp > r.exp.MinMS && attempt < maxRetakes:
			retake = max(r.exp.MinMS, exp/4)
		case r.mode == ModeDusk && r.skyRate <= 0 && attempt < maxRetakes:
			level := math.Max(seg.Peak, 1e-4)
			if level < r.exp.Target/3 && exp < r.exp.MaxMS {
				retake = min(r.exp.MaxMS, int(math.Ceil(float64(exp)*r.exp.Target/level)))
			} else if level < r.exp.Target/3 {
				return "", fmt.Errorf("%w at az %.1f alt %.1f: level %.3f at the longest exposure %d ms", ErrMeter, p.az, p.alt, level, exp)
			}
		}
		if retake > 0 && retake != exp {
			r.logf("exposure %d ms gave level %.3f (saturated %v); retake at %d ms", exp, seg.Peak, seg.Saturated, retake)
			_ = os.Remove(path)
			exp = retake
			continue
		}
		if seg.Saturated && !skySaturated {
			r.logf("bright lights saturate part of %s; they count as foreground, the exposure stays %d ms", name, exp)
		}
		r.updateReference(seg, exp)
		sunAz, sunAlt := r.site.SunAltAz(start)
		rec := FrameRecord{
			File: name, Column: p.ci, Row: p.row, TargetAz: p.az, TargetAlt: p.alt, AimAz: p.aimAz, AimAlt: p.aimAlt, PlannedPier: p.pier,
			Start: start, ExposureMS: exp, Mode: r.mode, PixelScale: r.header.PixelScale, Mount: mi.State(), SkyRef: skyRef, StarRef: seg.StarRefUsed,
			Class: seg.Class, ObstFrac: seg.ObstFrac, SkyLevel: seg.SkyLevel, Stars: seg.Stars, SunAlt: sunAlt,
			SunSep: Separation(HorVec(p.aimAz, p.aimAlt), HorVec(sunAz, sunAlt)),
		}
		if r.cfg.Astap != "" && r.mode == ModeNight {
			rec.Solved, rec.SolveDetail = r.solve(ctx, path, mi)
		}
		if err := r.man.Append(rec); err != nil {
			return "", err
		}
		r.frames++
		r.logf("frame %s: %s (obstructed %.1f%%, sky level %.3f, %d stars)", name, seg.Class, 100*seg.ObstFrac, seg.SkyLevel, seg.Stars)
		return seg.Class, nil
	}
}

func (r *Runner) updateReference(seg Segmentation, exp int) {
	if r.mode == ModeNight && (seg.RefBasis != RefStars || (r.skyRate <= 0 && seg.Class != ClassSky)) {
		return
	}
	if seg.SkyLevel > 0 && seg.Class != ClassObst {
		rate := seg.SkyLevel / float64(exp)
		if r.skyRate > 0 {
			rate = 0.7*rate + 0.3*r.skyRate
		}
		r.skyRate = rate
	}
	if r.mode == ModeNight && seg.Class != ClassObst && seg.StarDensity > 0 {
		d := seg.StarDensity
		if r.starDensity > 0 {
			d = 0.7*d + 0.3*r.starDensity
		}
		r.starDensity = d
	}
}

func (r *Runner) solve(ctx context.Context, path string, mi MountInfo) (bool, string) {
	ra, dec := DateToJ2000(mi.RightAscension*15, mi.Declination, r.clock.Now())
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	args := []string{"-f", path, "-r", "15", "-fov", strconv.FormatFloat(r.cfg.Plan.FieldH, 'f', 3, 64),
		"-ra", strconv.FormatFloat(ra/15, 'f', 5, 64), "-spd", strconv.FormatFloat(dec+90, 'f', 4, 64), "-update"}
	out, err := exec.CommandContext(ctx, r.cfg.Astap, args...).CombinedOutput()
	if err != nil {
		detail := string(out)
		if len(detail) > 200 {
			detail = detail[len(detail)-200:]
		}
		return false, fmt.Sprintf("astap: %v %s", err, detail)
	}
	return true, "astap"
}

func (r *Runner) finish(parent context.Context, runErr error) error {
	if !r.moved {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Minute)
	defer cancel()
	if runErr != nil {
		r.logf("aborting: %v", runErr)
		if err := r.nina.StopSlew(ctx); err != nil {
			r.logf("stop slew: %v", err)
		}
	}
	if err := r.nina.TrackingOff(ctx); err != nil {
		r.logf("tracking off: %v", err)
	}
	from, err := r.currentMechs(ctx)
	if err != nil {
		r.logf("could not read the mount position, so the path home is checked from the last known position: %v", err)
		from = []Mech{r.mech}
	}
	ck := NewChecker(r.site, r.cfg.Safety, r.clock.Now())
	var pathErr error
	for _, m := range from {
		if err := ck.HomePathOK(m); err != nil {
			pathErr = err
		}
	}
	if pathErr != nil {
		if err := r.liftBeforeHome(ctx, ck, from); err != nil {
			r.logf("WARNING %v: %v; lift: %v. Home the mount from NINA after checking the Sun.", ErrHomeUnsafe, pathErr, err)
			return fmt.Errorf("%w: %w", ErrHomeUnsafe, pathErr)
		}
	}
	var last error
	for attempt := 1; attempt <= homeAttempts; attempt++ {
		r.logf("find home (attempt %d of %d)", attempt, homeAttempts)
		if last = r.nina.FindHome(ctx); last == nil {
			if last = r.waitHome(ctx); last == nil {
				break
			}
		}
		r.logf("find home: %v", last)
	}
	if last != nil {
		return fmt.Errorf("%w: %w", ErrHomeFailed, last)
	}
	if err := r.nina.TrackingOff(ctx); err != nil {
		return fmt.Errorf("%w: tracking off after home: %w", ErrHomeFailed, err)
	}
	mi, err := r.nina.MountInfo(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHomeFailed, err)
	}
	if mi.TrackingEnabled {
		return fmt.Errorf("%w: tracking is still on after home", ErrHomeFailed)
	}
	r.logf("mount is at home, tracking off")
	return nil
}

func (r *Runner) homeWaypoint(ctx context.Context) error {
	r.logf("route through home")
	r.moved = true
	if err := r.nina.FindHome(ctx); err != nil {
		return err
	}
	if err := r.waitHome(ctx); err != nil {
		return err
	}
	r.mech = Home()
	return nil
}

func (r *Runner) currentMechs(ctx context.Context) ([]Mech, error) {
	var last error
	for range 3 {
		mi, err := r.nina.MountInfo(ctx)
		if err != nil {
			last = err
			_ = r.clock.Sleep(ctx, time.Second)
			continue
		}
		if mi.AtHome {
			return []Mech{Home()}, nil
		}
		ha := wrap180((mi.SiderealTime - mi.RightAscension) * 15)
		if m, ok := MechFor(ha, mi.Declination, string(mi.SideOfPier)); ok {
			return []Mech{m}, nil
		}
		east, _ := MechFor(ha, mi.Declination, PierEast)
		west, _ := MechFor(ha, mi.Declination, PierWest)
		return []Mech{east, west}, nil
	}
	return nil, last
}

func (r *Runner) liftBeforeHome(ctx context.Context, ck *Checker, from []Mech) error {
	if len(from) != 1 {
		return fmt.Errorf("%w: pier side unknown", ErrPier)
	}
	ha, dec := from[0].HADec()
	az, alt := r.site.HADecToAltAz(ha, dec)
	wAlt := math.Min(math.Max(alt, 0)+30, maxWaypoint)
	if err := ck.legOK(from[0], az, wAlt); err != nil {
		return err
	}
	r.logf("the path home is not Sun-safe from here; lifting to az %.1f alt %.1f first", az, wAlt)
	return r.slewTo(ctx, az, wAlt, false)
}

func (r *Runner) waitHome(ctx context.Context) error {
	deadline := r.clock.Now().Add(homeTimeout)
	for r.clock.Now().Before(deadline) {
		mi, err := r.nina.MountInfo(ctx)
		if err != nil {
			return err
		}
		if mi.AtHome && !mi.Slewing {
			return nil
		}
		if err := r.clock.Sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: not at home after %s", ErrHomeFailed, homeTimeout)
}
