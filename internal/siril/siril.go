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
	if err != nil {
		return res, fmt.Errorf("siril: %w: %s", err, lastErrors(res.Log))
	}
	if strings.Contains(res.Log, "Script execution failed") {
		return res, fmt.Errorf("siril: %s", lastErrors(res.Log))
	}
	slog.Debug("Siril script finished", "dir", dir, "duration", res.Duration.Round(time.Millisecond))
	return res, nil
}

// lastErrors pulls the lines that explain a failure out of Siril's log.
func lastErrors(log string) string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "log: ")
		lower := strings.ToLower(l)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			lines = append(lines, l)
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	if len(lines) == 0 {
		return "no error lines in log"
	}
	return strings.Join(lines, "; ")
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
	var out []float64
	for _, m := range darkCoef.FindAllStringSubmatch(log, -1) {
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
