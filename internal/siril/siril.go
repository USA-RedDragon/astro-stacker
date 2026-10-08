// Package siril runs Siril scripts headless. Siril does the pixel work the
// worker doesn't: building calibration masters, calibrating subs, and
// registering them to a reference.
package siril

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
)

// Runner invokes siril-cli. Siril ships as an AppImage; extracted, its AppRun
// takes the binary name as the first argument.
type Runner struct {
	// Command and Args start siril-cli: "siril-cli", or an extracted
	// AppImage's AppRun with "siril-cli" as its argument.
	Command string
	Args    []string
}

// Result is what a script run printed, for diagnostics and parsing.
type Result struct {
	Log      string
	Duration time.Duration
}

// Run executes script with dir as Siril's working directory.
func (r Runner) Run(ctx context.Context, dir, script string) (Result, error) {
	path := filepath.Join(dir, "script.ssf")
	if err := os.WriteFile(path, []byte("requires 1.4.0\n"+script), 0o600); err != nil {
		return Result{}, err
	}
	args := append(append([]string{}, r.Args...), "-d", dir, "-s", path)
	cmd := exec.CommandContext(ctx, r.Command, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// Headless: never try to reach a display.
	cmd.Env = append(os.Environ(), "DISPLAY=", "WAYLAND_DISPLAY=")
	start := time.Now()
	err := cmd.Run()
	res := Result{Log: out.String(), Duration: time.Since(start)}
	result := "ok"
	if err != nil || strings.Contains(res.Log, "Script execution failed") {
		result = "failed"
	}
	metrics.SirilSeconds.WithLabelValues(mainCommand(script), result).Observe(res.Duration.Seconds())
	if err != nil || strings.Contains(res.Log, "Script execution failed") {
		slog.Debug("Siril script failed", "dir", dir, "script", script, "log", res.Log)
		if err == nil {
			err = fmt.Errorf("script failed")
		}
		return res, fmt.Errorf("siril: %w: %s", err, tail(res.Log, 12))
	}
	slog.Debug("Siril script finished", "dir", dir, "duration", res.Duration.Round(time.Millisecond))
	return res, nil
}

// mainCommand names a script by the first of its commands that does the
// work, for metrics.
func mainCommand(script string) string {
	for _, line := range strings.Split(script, "\n") {
		cmd, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch cmd {
		case "calibrate_single", "calibrate", "register", "seqplatesolve", "platesolve", "stack", "seqapplyreg":
			return cmd
		}
	}
	return "other"
}

// tail returns the last n meaningful lines of Siril's log, which hold the
// failure and what Siril was doing when it happened. Progress bars and the
// harmless Python environment warnings are dropped.
func tail(log string, n int) string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "log: ")
		switch {
		case l == "",
			strings.HasPrefix(l, "progress:"),
			strings.Contains(l, "Python"),
			strings.Contains(l, "virtual environment"):
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Path checks a path for use in an option like -bias=path. Siril splits
// arguments on whitespace and keeps quotes inside an option as part of the
// value, so such paths can't contain whitespace or quotes at all.
func Path(p string) (string, error) {
	if strings.ContainsAny(p, " \t\n\"'") {
		return "", fmt.Errorf("siril can't take %q as an option value", p)
	}
	return p, nil
}

var darkCoef = regexp.MustCompile(`Dark optimization of image \d+: k0=([0-9.eE+-]+)`)

// DarkScales returns the dark optimization coefficients Siril logged, in
// the order the images were calibrated.
func DarkScales(log string) []float64 {
	matches := darkCoef.FindAllStringSubmatch(log, -1)
	out := make([]float64, 0, len(matches))
	for _, m := range matches {
		v, _ := strconv.ParseFloat(m[1], 64)
		out = append(out, v)
	}
	return out
}

var registered = regexp.MustCompile(`Total: (\d+) failed, (\d+) registered`)

// Registered returns how many frames Siril registered and how many failed.
func Registered(log string) (ok, failed int) {
	m := registered.FindStringSubmatch(log)
	if m == nil {
		return 0, 0
	}
	failed, _ = strconv.Atoi(m[1])
	ok, _ = strconv.Atoi(m[2])
	return ok, failed
}
