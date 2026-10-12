package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey/sim"
)

const usage = `horizon-survey measures the observatory horizon with the PHD2 guide camera.

  horizon-survey plan     [flags]   dry run: print the planned positions and Sun checks; moves nothing
  horizon-survey run      [flags]   drive the mount through NINA and capture guide frames
  horizon-survey analyze  [flags]   turn a survey folder into a NINA horizon file and a plot
  horizon-survey simulate [flags]   run the whole pipeline against a simulated mount and camera

Run "horizon-survey <command> -h" for the flags of a command.
`

var errUsage = errors.New("usage")

type common struct {
	plan    hs.PlanConfig
	safety  hs.Safety
	exclude string
}

func addCommon(fs *flag.FlagSet) *common {
	c := &common{safety: hs.DefaultSafety()}
	fs.Float64Var(&c.plan.AzStart, "az-start", 0, "first azimuth column (deg)")
	fs.Float64Var(&c.plan.AzStep, "az-step", 10, "azimuth step between columns (deg)")
	fs.StringVar(&c.exclude, "exclude", "", "azimuth sectors to skip, as from-to pairs separated by commas, e.g. 100-140,350-10")
	fs.Float64Var(&c.plan.AltMin, "alt-min", 0, "lowest altitude the frames reach (deg)")
	fs.Float64Var(&c.plan.AltMax, "alt-max", 35, "highest altitude the frames reach (deg)")
	fs.Float64Var(&c.plan.Overlap, "overlap", 0.3, "overlap between frames in a column (fraction of the short side of the field)")
	fs.Float64Var(&c.safety.MinSunSep, "min-sun-sep", c.safety.MinSunSep, "minimum angle from the Sun for every position and slew path (deg, at least 45)")
	fs.Float64Var(&c.safety.MaxSunAlt, "max-sun-alt", c.safety.MaxSunAlt, "refuse to move while the Sun is higher than this (deg, at most 0)")
	fs.Float64Var(&c.safety.MinAlt, "min-alt", c.safety.MinAlt, "lowest altitude the frame centre may be sent to (deg, at least 0); set it to the mount driver's limit if that is higher")
	fs.Float64Var(&c.safety.PathMargin, "path-margin", c.safety.PathMargin, "how far below -min-alt the modelled slew path may dip (deg)")
	fs.Float64Var(&c.safety.MeridianZone, "meridian-zone", c.safety.MeridianZone, "hour angle zone (deg) around the meridian where either pier side is assumed")
	return c
}

func (c *common) finish() error {
	if c.exclude == "" {
		return nil
	}
	for _, part := range strings.Split(c.exclude, ",") {
		a, b, ok := strings.Cut(strings.TrimSpace(part), "-")
		from, err1 := strconv.ParseFloat(a, 64)
		to, err2 := strconv.ParseFloat(b, 64)
		if !ok || err1 != nil || err2 != nil {
			return fmt.Errorf("%w: bad -exclude sector %q", errUsage, part)
		}
		c.plan.Exclude = append(c.plan.Exclude, hs.AzRange{From: from, To: to})
	}
	return nil
}

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	var err error
	switch os.Args[1] {
	case "plan":
		err = planCmd(ctx, os.Args[2:], os.Stdout)
	case "run":
		err = runCmd(ctx, os.Args[2:], os.Stdout)
	case "analyze":
		err = analyzeCmd(os.Args[2:], os.Stdout)
	case "simulate":
		err = simulateCmd(ctx, os.Args[2:], os.Stdout)
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func planCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	c := addCommon(fs)
	ninaURL := fs.String("nina", "http://localhost:1888/v2/api", "NINA Advanced API base URL, read for the site (only GET requests)")
	phdAddr := fs.String("phd2", "localhost:4400", "PHD2 server, read for the pixel scale and frame size")
	lat := fs.Float64("lat", math.NaN(), "site latitude; with -lon, NINA is not asked")
	lon := fs.Float64("lon", math.NaN(), "site longitude, east positive")
	scale := fs.Float64("pixel-scale", 0, "guide camera pixel scale (arcsec/px); 0 asks PHD2")
	frame := fs.String("frame", "", "guide frame size WxH; empty asks PHD2")
	at := fs.String("time", "", "plan for this time (RFC 3339); default now")
	perFrame := fs.Duration("per-frame", 15*time.Second, "time per frame used for the estimate and the Sun look-ahead")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := c.finish(); err != nil {
		return err
	}
	t := time.Now()
	if *at != "" {
		var err error
		if t, err = time.Parse(time.RFC3339, *at); err != nil {
			return err
		}
	}
	site := hs.Site{Lat: *lat, Lon: *lon}
	if math.IsNaN(*lat) || math.IsNaN(*lon) {
		mi, err := hs.NewNINA(*ninaURL).MountInfo(ctx)
		if err != nil {
			return fmt.Errorf("read the site from NINA (or pass -lat and -lon): %w", err)
		}
		site = hs.Site{Lat: mi.SiteLatitude, Lon: mi.SiteLongitude}
	}
	w, h := 0, 0
	if *frame != "" {
		if _, err := fmt.Sscanf(*frame, "%dx%d", &w, &h); err != nil {
			return fmt.Errorf("%w: -frame %q", errUsage, *frame)
		}
	}
	if *scale <= 0 || w == 0 {
		phd, err := hs.DialPHD2(ctx, *phdAddr)
		if err != nil {
			return fmt.Errorf("read the camera from PHD2 (or pass -pixel-scale and -frame): %w", err)
		}
		defer func() { _ = phd.Close() }()
		if *scale <= 0 {
			if *scale, err = phd.PixelScale(ctx); err != nil {
				return err
			}
		}
		if w == 0 {
			if w, h, err = phd.FrameSize(ctx); err != nil {
				return err
			}
		}
	}
	if *scale <= 0 {
		return hs.ErrNoPixelScale
	}
	c.plan.FieldW, c.plan.FieldH = float64(w)**scale/3600, float64(h)**scale/3600
	_, _ = fmt.Fprintf(out, "Site %.4f %.4f; field %.2f x %.2f deg (%dx%d at %.2f arcsec/px)\n", site.Lat, site.Lon, c.plan.FieldW, c.plan.FieldH, w, h, *scale)
	p, err := hs.MakePlan(site, c.plan, c.safety, t, *perFrame)
	if err != nil {
		return err
	}
	p.Write(out)
	return nil
}

type runOpts struct {
	c    *common
	cfg  *hs.RunConfig
	nina *string
	phd2 *string
	exp  *hs.Exposure
	lat  *float64
	lon  *float64
}

func runFlags(fs *flag.FlagSet) runOpts {
	c := addCommon(fs)
	cfg := &hs.RunConfig{}
	ninaURL := fs.String("nina", "http://localhost:1888/v2/api", "NINA Advanced API base URL")
	phdAddr := fs.String("phd2", "localhost:4400", "PHD2 server address")
	fs.StringVar(&cfg.Out, "out", "", "folder for the frames and manifest (default horizon-survey-<UTC time>)")
	fs.StringVar(&cfg.Mode, "mode", hs.ModeAuto, "dusk, night or auto (dusk while the Sun is above -12)")
	fs.DurationVar(&cfg.Settle, "settle", 2*time.Second, "wait after each slew before the exposure")
	fs.Float64Var(&cfg.PixelScale, "pixel-scale", 0, "guide camera pixel scale (arcsec/px); 0 asks PHD2")
	fs.IntVar(&cfg.MaxFrames, "max-frames", 400, "stop after this many frames")
	fs.BoolVar(&cfg.Full, "full", false, "take every row of every column instead of following the edge")
	fs.StringVar(&cfg.Astap, "astap", "", "path to astap.exe to plate solve night frames (writes the solution into each FITS header)")
	e := &hs.Exposure{}
	fs.IntVar(&e.MinMS, "exp-min", 0, "shortest exposure (ms); 0 uses the mode default")
	fs.IntVar(&e.MaxMS, "exp-max", 0, "longest exposure (ms); 0 uses the mode default")
	fs.IntVar(&e.StartMS, "exp-start", 0, "first exposure (ms); 0 uses the mode default")
	fs.Float64Var(&e.Target, "sky-level", 0, "target sky level as a fraction of full scale; 0 uses the mode default")
	lat := fs.Float64("lat", math.NaN(), "expected site latitude; the run refuses if the mount's site differs by more than 0.05 deg")
	lon := fs.Float64("lon", math.NaN(), "expected site longitude, east positive")
	return runOpts{c: c, cfg: cfg, nina: ninaURL, phd2: phdAddr, exp: e, lat: lat, lon: lon}
}

func resolveExposure(mode string, e *hs.Exposure) *hs.Exposure {
	if *e == (hs.Exposure{}) {
		return nil
	}
	d := hs.DefaultExposure(mode)
	if e.MinMS > 0 {
		d.MinMS = e.MinMS
	}
	if e.MaxMS > 0 {
		d.MaxMS = e.MaxMS
	}
	if e.StartMS > 0 {
		d.StartMS = e.StartMS
	}
	if e.Target > 0 {
		d.Target = e.Target
	}
	return &d
}

func runCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	o := runFlags(fs)
	c, cfg, ninaURL, phdAddr, e := o.c, o.cfg, o.nina, o.phd2, o.exp
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := c.finish(); err != nil {
		return err
	}
	if math.IsNaN(*o.lat) || math.IsNaN(*o.lon) {
		return fmt.Errorf("%w: -lat and -lon are required; the run checks the mount's site against them", errUsage)
	}
	cfg.Site = &hs.Site{Lat: *o.lat, Lon: *o.lon}
	if cfg.Mode == hs.ModeAuto && *e != (hs.Exposure{}) {
		return fmt.Errorf("%w: exposure flags need -mode dusk or -mode night", errUsage)
	}
	cfg.Exposure = resolveExposure(cfg.Mode, e)
	if cfg.Out == "" {
		cfg.Out = "horizon-survey-" + time.Now().UTC().Format("20060102T150405Z")
	}
	abs, err := filepath.Abs(cfg.Out)
	if err != nil {
		return err
	}
	cfg.Out, cfg.Plan, cfg.Safety, cfg.Log = abs, c.plan, c.safety, out
	phd, err := hs.DialPHD2(ctx, *phdAddr)
	if err != nil {
		return err
	}
	defer func() { _ = phd.Close() }()
	r := hs.NewRunner(*cfg, hs.NewNINA(*ninaURL), phd, hs.RealClock{})
	if err := r.Run(ctx); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Frames are in %s. Next: horizon-survey analyze -dir %q\n", cfg.Out, cfg.Out)
	return nil
}

func analyzeCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	dir := fs.String("dir", "", "survey folder written by run; several folders separated by commas are combined (for example a dusk and a night survey)")
	calPath := fs.String("calibration", "", "camera calibration JSON for frames without a plate solve")
	hrz := fs.String("hrz", "", "horizon file to write (default <dir>/horizon.hrz)")
	plot := fs.String("plot", "", "plot to write (default <dir>/horizon.png)")
	writeCal := fs.String("write-calibration", "", "write the calibration derived from plate-solved frames here (default <dir>/calibration.json)")
	step := fs.Float64("hrz-step", 1, "azimuth step of the horizon file (deg)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("%w: -dir is required", errUsage)
	}
	dirs := strings.Split(*dir, ",")
	opts := hs.AnalyzeOptions{Dirs: dirs, HRZStep: *step, Log: out}
	if *calPath != "" {
		b, err := os.ReadFile(filepath.Clean(*calPath))
		if err != nil {
			return err
		}
		var cal hs.Calibration
		if err := json.Unmarshal(b, &cal); err != nil {
			return err
		}
		if !cal.Valid() {
			return fmt.Errorf("%w: calibration %s is incomplete", errUsage, *calPath)
		}
		opts.Calibration = &cal
	}
	res, err := hs.Analyze(opts)
	if res != nil && res.Calibration != nil {
		p := *writeCal
		if p == "" {
			p = filepath.Join(dirs[0], "calibration.json")
		}
		if werr := hs.WriteJSON(p, res.Calibration); werr != nil {
			return werr
		}
		_, _ = fmt.Fprintf(out, "calibration written to %s\n", p)
	}
	if err != nil {
		return err
	}
	return writeOutputs(res, dirs[0], *hrz, *plot, out)
}

func writeOutputs(res *hs.Result, dir, hrz, plot string, out io.Writer) error {
	if hrz == "" {
		hrz = filepath.Join(dir, "horizon.hrz")
	}
	if plot == "" {
		plot = filepath.Join(dir, "horizon.png")
	}
	f, err := os.Create(filepath.Clean(hrz))
	if err != nil {
		return err
	}
	if err := hs.WriteHRZ(f, res.Points, res.HRZHeader()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	var outlines [][][2]float64
	for _, fr := range res.Frames {
		if fr.Outline != nil {
			outlines = append(outlines, fr.Outline)
		}
	}
	pf, err := os.Create(filepath.Clean(plot))
	if err != nil {
		return err
	}
	title := fmt.Sprintf("Horizon survey %s, %.0f%% of azimuth measured", res.Header.Started.UTC().Format("2006-01-02 15:04Z"), 100*res.Skyline.Coverage())
	if err := hs.Plot(pf, hs.PlotInput{Skyline: res.Skyline, Outlines: outlines, Title: title, Excluded: res.Header.Plan.Exclude}); err != nil {
		_ = pf.Close()
		return err
	}
	if err := pf.Close(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "horizon file: %s\nplot: %s\nmeasured azimuth coverage: %.0f%%\n", hrz, plot, 100*res.Skyline.Coverage())
	return nil
}

func simulateCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	o := runFlags(fs)
	c, cfg := o.c, o.cfg
	at := fs.String("time", "2026-10-13T00:25:00Z", "simulated start time (RFC 3339)")
	solve := fs.Bool("solve", false, "give every simulated frame a plate solution")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := c.finish(); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return err
	}
	site := hs.Site{Lat: 35.0, Lon: -110.0}
	if !math.IsNaN(*o.lat) && !math.IsNaN(*o.lon) {
		site = hs.Site{Lat: *o.lat, Lon: *o.lon}
	}
	cfg.Site = &site
	cam := sim.DuskCamera()
	if cfg.Mode == hs.ModeNight {
		cam = sim.NightCamera()
	}
	clock := sim.NewClock(t)
	s, err := sim.Start(ctx, sim.Options{Site: site, Camera: cam, Clock: clock, Solve: *solve})
	if err != nil {
		return err
	}
	defer s.Close()
	if cfg.Out == "" {
		cfg.Out = "horizon-survey-sim"
	}
	cfg.Plan, cfg.Safety, cfg.Log, cfg.Settle = c.plan, c.safety, out, time.Second
	phd, err := hs.DialPHD2(ctx, s.PHD2Addr())
	if err != nil {
		return err
	}
	defer func() { _ = phd.Close() }()
	if err := hs.NewRunner(*cfg, hs.NewNINA(s.NINAURL()), phd, clock).Run(ctx); err != nil {
		return err
	}
	var cal *hs.Calibration
	if !*solve {
		k := cam.Calibration()
		cal = &k
	}
	res, err := hs.Analyze(hs.AnalyzeOptions{Dirs: []string{cfg.Out}, Calibration: cal, HRZStep: 1, Log: io.Discard})
	if err != nil {
		return err
	}
	worst, worstAz, n, over := 0.0, 0.0, 0, 0
	for _, b := range res.Skyline.Bins {
		if b.Kind != hs.BinEdge || !b.Measured {
			continue
		}
		n++
		e := sim.Skyline(sim.DemoSkyline).Error(b.Alt, b.Az, cam.AzResolution())
		if e > 0.5 {
			over++
		}
		if e > worst {
			worst, worstAz = e, b.Az
		}
	}
	_, _ = fmt.Fprintf(out, "simulated survey: %d measured edge bins, worst error against the true skyline %.2f deg at az %.2f, %d bins over 0.5 deg\n", n, worst, worstAz, over)
	return writeOutputs(res, cfg.Out, "", "", out)
}
