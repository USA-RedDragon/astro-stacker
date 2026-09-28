package stacking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// MasterVersion is how masters are written: their header, how saturated
// cores are filled and the XISF copy for PixInsight. Masters written by an older version are republished at
// startup.
const MasterVersion = 2

// maxSolveAttempts is how many times a target's master is plate solved
// before giving up on that reference.
const maxSolveAttempts = 3

// wcsKey matches the keywords of a plate solution.
var wcsKey = regexp.MustCompile(`^(WCSAXES|CTYPE\d|CUNIT\d|CRPIX\d|CRVAL\d|CDELT\d|CROTA\d|CD\d_\d|PC\d_\d|LONPOLE|LATPOLE|RADESYS|EQUINOX|PLTSOLVD|(A|B|AP|BP)_(ORDER|\d_\d|DMAX))$`)

// replaced are reference keywords a master doesn't copy: the file's own
// structure, and what describes one sub rather than the master.
var replaced = map[string]bool{
	"SIMPLE": true, "BITPIX": true, "NAXIS": true, "NAXIS1": true, "NAXIS2": true, "NAXIS3": true,
	"EXTEND": true, "BZERO": true, "BSCALE": true, "ROWORDER": true,
	"IMAGETYP": true, "EXPOSURE": true, "EXPTIME": true, "DATE": true, "SWCREATE": true,
}

// masterHeader is a master's FITS header: the keywords NINA wrote on the
// target's registration reference, which the master shares its framing
// with, then the master's own, then its plate solution so ImageSolver and
// SPCC need no input. masterFile is plate solved the first time.
func (p *Pipeline) masterHeader(ctx context.Context, stack *app.Stack, own []imagedata.Card, dir, masterFile string) []imagedata.Card {
	var tr app.TargetReference
	if err := p.db.WithContext(ctx).Where("object = ?", stack.Object).First(&tr).Error; err != nil {
		slog.Warn("No registration reference for master header", "object", stack.Object, "error", err)
		return own
	}
	var ninaCards []frameheader.Card
	var ref app.Frame
	if err := p.db.WithContext(ctx).First(&ref, tr.FrameID).Error; err != nil {
		slog.Warn("Could not find reference frame", "object", stack.Object, "error", err)
	} else if ninaCards, err = indexer.ReadCards(ctx, p.s3, p.source, minio.ObjectInfo{Key: ref.Key, Size: ref.Size}); err != nil {
		slog.Warn("Could not read reference header", "object", stack.Object, "key", ref.Key, "error", err)
	}

	solution := p.plateSolution(ctx, &tr, stack, ninaCards, dir, masterFile)
	skip := map[string]bool{}
	for _, c := range own {
		skip[c.Key] = true
	}
	for _, c := range solution {
		skip[c.Name] = true
	}
	var out []imagedata.Card
	for _, c := range ninaCards {
		if replaced[c.Name] || skip[c.Name] || wcsKey.MatchString(c.Name) {
			continue
		}
		skip[c.Name] = true // NINA writes each once; guard anyway
		out = append(out, imageCard(c))
	}
	out = append(out, own...)
	if !hasCard(ninaCards, "OBJCTRA") {
		if pos, ok := p.targetPosition(ctx, stack.Object); ok {
			out = append(out,
				imagedata.StringCard("OBJCTRA", sexagesimal(pos[0]/15, false), "[h] target RA (Target Scheduler)"),
				imagedata.StringCard("OBJCTDEC", sexagesimal(pos[1], true), "[deg] target Dec (Target Scheduler)"))
		}
	}
	for _, c := range solution {
		out = append(out, imageCard(c))
	}
	return out
}

func imageCard(c frameheader.Card) imagedata.Card {
	return imagedata.Card{Key: c.Name, Value: c.Value, Str: c.Quoted, Comment: c.Comment}
}

func hasCard(cards []frameheader.Card, name string) bool {
	for _, c := range cards {
		if c.Name == name {
			return true
		}
	}
	return false
}

func cardFloat(cards []frameheader.Card, name string) float64 {
	for _, c := range cards {
		if c.Name == name {
			if f, err := strconv.ParseFloat(c.Value, 64); err == nil {
				return f
			}
		}
	}
	return math.NaN()
}

// plateSolution returns the target's stored plate solution, solving
// masterFile for it when there is none yet. Every filter of a target is
// registered to the same reference, so one solution fits all its masters.
func (p *Pipeline) plateSolution(ctx context.Context, tr *app.TargetReference, stack *app.Stack, nina []frameheader.Card, dir, masterFile string) []frameheader.Card {
	if tr.WCS != nil {
		var cards []frameheader.Card
		if err := json.Unmarshal([]byte(*tr.WCS), &cards); err == nil {
			return cards
		}
	}
	if tr.SolveAttempts >= maxSolveAttempts {
		return nil
	}
	cards, err := p.solveMaster(ctx, stack, nina, dir, masterFile)
	updates := map[string]any{"solve_attempts": tr.SolveAttempts + 1}
	if err != nil {
		msg := err.Error()
		updates["solve_error"] = msg
		slog.Warn("Could not plate solve master", "object", stack.Object, "filter", stack.Filter, "error", err)
	} else {
		b, _ := json.Marshal(cards)
		wcs := string(b)
		updates["wcs"], updates["solve_error"] = wcs, nil
		tr.WCS = &wcs
		slog.Info("Plate solved master", "object", stack.Object, "filter", stack.Filter)
	}
	tr.SolveAttempts++
	if err := p.db.WithContext(ctx).Model(tr).Updates(updates).Error; err != nil {
		slog.Warn("Could not save plate solution", "object", stack.Object, "error", err)
	}
	return cards
}

// solveMaster plate solves a master with Siril, starting from where the
// reference sub pointed (or the target's position) and the optics in its
// header, and returns the solution's keywords.
func (p *Pipeline) solveMaster(ctx context.Context, stack *app.Stack, nina []frameheader.Card, dir, masterFile string) ([]frameheader.Card, error) {
	ra, dec := cardFloat(nina, "RA"), cardFloat(nina, "DEC")
	if math.IsNaN(ra) || math.IsNaN(dec) {
		pos, ok := p.targetPosition(ctx, stack.Object)
		if !ok {
			return nil, fmt.Errorf("no position for %s", stack.Object)
		}
		ra, dec = pos[0], pos[1]
	}
	focal, pixel := cardFloat(nina, "FOCALLEN"), cardFloat(nina, "XPIXSZ")
	if bin := cardFloat(nina, "XBINNING"); bin > 1 {
		pixel *= bin
	}
	if !(focal > 0) || !(pixel > 0) {
		var err error
		if focal, pixel, err = p.optics(ctx, stack.Object, stack.Filter); err != nil {
			return nil, err
		}
	}
	solveDir, err := os.MkdirTemp(dir, "solve-")
	if err != nil {
		return nil, err
	}
	if err := os.Link(masterFile, filepath.Join(solveDir, "master.fit")); err != nil {
		return nil, err
	}
	var runErr error
	for _, extra := range []string{"", " -downscale"} {
		script := p.sirilPreamble(true) + fmt.Sprintf("load master\nplatesolve %.6f,%.6f -focal=%.2f -pixelsize=%.3f -force%s\nsave solved\n",
			ra, dec, focal, pixel, extra)
		if _, runErr = p.siril.Run(ctx, solveDir, script); runErr == nil {
			break
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("plate solve: %w", runErr)
	}
	b, err := os.ReadFile(filepath.Join(solveDir, "solved.fit"))
	if err != nil {
		return nil, err
	}
	all, err := frameheader.ParseCards(b)
	if err != nil {
		return nil, err
	}
	var cards []frameheader.Card
	for _, c := range all {
		if wcsKey.MatchString(c.Name) {
			cards = append(cards, c)
		}
	}
	if !hasCard(cards, "CRVAL1") {
		return nil, fmt.Errorf("solved master has no WCS")
	}
	return cards, nil
}

func (p *Pipeline) targetPosition(ctx context.Context, object string) ([2]float64, bool) {
	positions, err := targetPositions(ctx, p.sched)
	if err != nil {
		return [2]float64{}, false
	}
	pos, ok := positions[object]
	return pos, ok
}

// sexagesimal formats hours or degrees as NINA does: "HH MM SS.ss" or
// "+DD MM SS.s".
func sexagesimal(v float64, signed bool) string {
	sign := "+"
	if v < 0 {
		sign, v = "-", -v
	}
	d := math.Floor(v)
	m := math.Floor((v - d) * 60)
	s := ((v-d)*60 - m) * 60
	if signed {
		if s >= 59.95 {
			s, m = 0, m+1
		}
		if m >= 60 {
			m, d = 0, d+1
		}
		return fmt.Sprintf("%s%02.0f %02.0f %04.1f", sign, d, m, s)
	}
	if s >= 59.995 {
		s, m = 0, m+1
	}
	if m >= 60 {
		m, d = 0, d+1
	}
	return fmt.Sprintf("%02.0f %02.0f %05.2f", math.Mod(d, 24), m, s)
}

// republishMasters rewrites masters written by an older MasterVersion from
// their saved state. Targets a worker is stacking are skipped; the worker
// republishes them itself.
func (p *Pipeline) republishMasters(ctx context.Context) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("state_key IS NOT NULL AND (master_version IS NULL OR master_version < ?)", MasterVersion).
		Order("object, filter").Find(&stacks).Error; err != nil {
		slog.Warn("Could not find masters to republish", "error", err)
		return
	}
	done := 0
	for i := range stacks {
		if ctx.Err() != nil {
			return
		}
		s := &stacks[i]
		if !p.hold(s.Object) {
			continue
		}
		err := func() error {
			defer p.release(s.Object)
			acc, err := p.loadState(ctx, s)
			if err != nil {
				return err
			}
			return p.publish(ctx, s, acc)
		}()
		if err != nil {
			slog.Warn("Could not republish master", "object", s.Object, "filter", s.Filter, "error", err)
			continue
		}
		done++
	}
	if len(stacks) > 0 {
		slog.Info("Republished masters", "masters", done, "of", len(stacks))
	}
}

// pixInsightSolution turns a master's plate solution keywords, which
// describe the FITS layout (rows from the bottom), into the astrometric
// solution properties PixInsight reads from an XISF file (rows from the
// top), and returns the other keywords. PixInsight reads FITS rows from the
// top without adjusting the solution, so a solved FITS master opens
// mirrored; XISF opens upright and solved. PixInsight's solution is linear,
// so the distortion terms are left out.
func pixInsightSolution(cards []imagedata.Card, h int) ([]imagedata.Property, []imagedata.Card) {
	var rest []imagedata.Card
	kw := map[string]float64{}
	for _, c := range cards {
		if !wcsKey.MatchString(c.Key) {
			rest = append(rest, c)
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64); err == nil {
			kw[c.Key] = f
		}
	}
	get := func(k string, def float64) float64 {
		if v, ok := kw[k]; ok {
			return v
		}
		return def
	}
	if _, ok := kw["CRVAL1"]; !ok {
		return nil, rest
	}
	cd := [4]float64{get("CD1_1", 0), get("CD1_2", 0), get("CD2_1", 0), get("CD2_2", 0)}
	if _, ok := kw["CD1_1"]; !ok {
		c1, c2 := get("CDELT1", 1), get("CDELT2", 1)
		cd = [4]float64{c1 * get("PC1_1", 1), c1 * get("PC1_2", 0), c2 * get("PC2_1", 0), c2 * get("PC2_2", 1)}
	}
	// Row y from the bottom (1-based, pixel centres) is H - y + 0.5 from
	// the top in PixInsight's coordinates, which count from pixel corners;
	// columns are x - 0.5. Turning rows over negates the matrix's y column.
	return []imagedata.Property{
		{ID: "PCL:AstrometricSolution:ProjectionSystem", Value: "Gnomonic"},
		{ID: "PCL:AstrometricSolution:ReferenceCelestialCoordinates", Value: []float64{kw["CRVAL1"], get("CRVAL2", 0)}},
		{ID: "PCL:AstrometricSolution:ReferenceImageCoordinates", Value: []float64{get("CRPIX1", 0) - 0.5, float64(h) - get("CRPIX2", 0) + 0.5}},
		{ID: "PCL:AstrometricSolution:LinearTransformationMatrix", Value: []float64{cd[0], -cd[1], cd[2], -cd[3]}, Rows: 2},
		{ID: "PCL:AstrometricSolution:ReferenceNativeCoordinates", Value: []float64{0, 90}},
		{ID: "PCL:AstrometricSolution:CelestialPoleNativeCoordinates", Value: []float64{get("LONPOLE", 180), 90}},
		{ID: "Observation:CelestialReferenceSystem", Value: "ICRS"},
		{ID: "Observation:Equinox", Value: 2000.0},
	}, rest
}

// writeXISFFile writes a master for PixInsight: upright, with its plate
// solution as PixInsight's own properties.
func writeXISFFile(name string, w, h int, data []float32, cards []imagedata.Card) error {
	props, keywords := pixInsightSolution(cards, h)
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if err := imagedata.WriteXISF(f, w, h, data, keywords, props); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
