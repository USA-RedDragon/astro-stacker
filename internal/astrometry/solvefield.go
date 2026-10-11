package astrometry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

// Solver runs solve-field over the index files in IndexDir. It is ready once
// the index files are in place (MarkReady). One solve runs at a time:
// solve-field finds stars and matches quads on one core, and the CPU
// seconds and time each solve may take are bounded.
type Solver struct {
	Command    string
	IndexDir   string
	CPUSeconds int
	Timeout    time.Duration

	ready atomic.Bool
	mu    sync.Mutex
}

// Hint narrows the search. RA and Dec (degrees, NaN when unknown) centre a
// search within Radius degrees; ScaleLow and ScaleHigh (arcseconds per
// pixel, 0 when unknown) bound the pixel scale.
type Hint struct {
	RA, Dec             float64
	Radius              float64
	ScaleLow, ScaleHigh float64
	Width, Height       int
}

// ErrNoSolution is returned when solve-field ran but found no solution.
var ErrNoSolution = errors.New("solve-field found no solution")

// MarkReady says the index files are in place.
func (s *Solver) MarkReady() { s.ready.Store(true) }

// Ready reports whether solves can run.
func (s *Solver) Ready() bool { return s != nil && s.ready.Load() }

// Fingerprint names the solver and its index set, or "" when it can't
// solve.
func (s *Solver) Fingerprint() string {
	if !s.Ready() {
		return ""
	}
	return "astrometry.net:" + IndexSetID()
}

// downsampleAbove is the image width above which stars are found on an
// image binned 2x2: faster, and enough stars either way.
const downsampleAbove = 3000

// blindWidthLow and blindWidthHigh bound the field width, in degrees, when
// the pixel scale is unknown: the index files cover fields of about 0.2° to
// 8°.
const (
	blindWidthLow  = "0.15"
	blindWidthHigh = "10"
)

const none = "none"

// Args are solve-field's arguments for solving file into outDir.
func (s *Solver) Args(config, outDir, file string, h Hint) []string {
	args := []string{
		"--config", config,
		"--dir", outDir,
		"--temp-dir", outDir,
		"--fits-image",
		"--no-plots",
		"--no-remove-lines",
		"--uniformize", "0",
		"--overwrite",
		"--new-fits", none,
		"--index-xyls", none,
		"--rdls", none,
		"--corr", none,
		"--match", none,
		"--cpulimit", strconv.Itoa(max(1, s.CPUSeconds)),
	}
	if h.Width > downsampleAbove {
		args = append(args, "--downsample", "2")
	}
	if h.ScaleLow > 0 && h.ScaleHigh > h.ScaleLow {
		args = append(args, "--scale-units", "arcsecperpix",
			"--scale-low", strconv.FormatFloat(h.ScaleLow, 'f', 4, 64),
			"--scale-high", strconv.FormatFloat(h.ScaleHigh, 'f', 4, 64))
	} else {
		args = append(args, "--scale-units", "degwidth", "--scale-low", blindWidthLow, "--scale-high", blindWidthHigh)
	}
	if !math.IsNaN(h.RA) && !math.IsNaN(h.Dec) && h.Radius > 0 {
		args = append(args,
			"--ra", strconv.FormatFloat(h.RA, 'f', 5, 64),
			"--dec", strconv.FormatFloat(h.Dec, 'f', 5, 64),
			"--radius", strconv.FormatFloat(h.Radius, 'f', 2, 64))
	}
	return append(args, file)
}

// Solve plate solves the FITS image file, working in a new directory under
// dir, and returns the solution's header cards (TAN-SIP).
func (s *Solver) Solve(ctx context.Context, dir, file string, h Hint) ([]frameheader.Card, error) {
	if !s.Ready() {
		return nil, errors.New("solve-field: index files not ready")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	work, err := os.MkdirTemp(dir, "solve-field-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	config := filepath.Join(work, "astrometry.cfg")
	if err := os.WriteFile(config, []byte("add_path "+s.IndexDir+"\nautoindex\n"), 0o600); err != nil {
		return nil, err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Command, s.Args(config, work, file, h)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Dir = work
	killGroup(cmd)
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("solve-field: %w after %s: %s", ctx.Err(), timeout, tail(out.String(), 8))
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("solve-field: %w", ctx.Err())
	}
	b, err := os.ReadFile(filepath.Join(work, strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))+".wcs"))
	if errors.Is(err, os.ErrNotExist) {
		if runErr != nil {
			return nil, fmt.Errorf("solve-field: %w: %s", runErr, tail(out.String(), 8))
		}
		return nil, fmt.Errorf("%w: %s", ErrNoSolution, tail(out.String(), 4))
	}
	if err != nil {
		return nil, err
	}
	return frameheader.ParseCards(b)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
