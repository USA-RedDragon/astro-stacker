package stacking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/astrometry"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// MasterVersion is how masters are written: their header, how saturated
// cores are filled and the XISF copy for PixInsight. Masters written by an older version are republished at
// startup. Version 3 derives the optics of references that lack them
// (derivedOptics); version 2 masters whose header that leaves unchanged
// are only marked.
const MasterVersion = 3

// headerOnlyVersion is the oldest MasterVersion whose masters differ from
// the current only in derived optics.
const headerOnlyVersion = 2

// maxSolveAttempts is how many times a target's master is plate solved
// before giving up on that reference.
const maxSolveAttempts = 3

// wcsKey matches the keywords of a plate solution.
var wcsKey = regexp.MustCompile(`^(WCSAXES|CTYPE\d|CUNIT\d|CRPIX\d|CRVAL\d|CDELT\d|CROTA\d|CD\d_\d|PC\d_\d|LONPOLE|LATPOLE|RADESYS|EQUINOX|PLTSOLVD|(A|B|AP|BP)_(ORDER|\d_\d|DMAX))$`)

// replacedKeywords are reference keywords a master doesn't copy: the file's own
// structure, and what describes one sub rather than the master.
func replacedKeywords() map[string]bool {
	return map[string]bool{
		keywordSIMPLE: true, keywordBITPIX: true, "NAXIS": true, "NAXIS1": true, "NAXIS2": true, "NAXIS3": true,
		keywordEXTEND: true, keywordBZERO: true, keywordBSCALE: true, "ROWORDER": true,
		"IMAGETYP": true, "EXPOSURE": true, "EXPTIME": true, "DATE": true, "SWCREATE": true,
	}
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
	ref, ninaCards := p.referenceCards(ctx, &tr)

	solution := p.plateSolution(ctx, &tr, stack, ref.Key, ninaCards, dir, masterFile)
	derived := derivedOptics(cardKeywords(ninaCards), ref.Key, solution)
	skip := map[string]bool{}
	for _, c := range own {
		skip[c.Key] = true
	}
	for _, c := range solution {
		skip[c.Name] = true
	}
	for _, c := range derived {
		skip[c.Key] = true
	}
	replaced := replacedKeywords()
	var out []imagedata.Card
	for _, c := range ninaCards {
		if replaced[c.Name] || skip[c.Name] || wcsKey.MatchString(c.Name) {
			continue
		}
		skip[c.Name] = true // NINA writes each once; guard anyway
		out = append(out, imageCard(c))
	}
	out = append(out, derived...)
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

// referenceCards are the header cards of a target's registration reference
// sub, as NINA or Telescope.live wrote them.
func (p *Pipeline) referenceCards(ctx context.Context, tr *app.TargetReference) (app.Frame, []frameheader.Card) {
	var ref app.Frame
	if err := p.db.WithContext(ctx).First(&ref, tr.FrameID).Error; err != nil {
		slog.Warn("Could not find reference frame", "object", tr.Object, "error", err)
		return ref, nil
	}
	cards, err := indexer.ReadCards(ctx, p.s3, p.source, minio.ObjectInfo{Key: ref.Key, Size: ref.Size})
	if err != nil {
		slog.Warn("Could not read reference header", "object", tr.Object, "key", ref.Key, "error", err)
	}
	return ref, cards
}

// derivedOptics are FOCALLEN and XPIXSZ cards for a master whose reference
// sub lacks them, so ImageSolver and SPCC have its optics: Telescope.live
// writes FOCALLEN 0 or none, and no XPIXSZ on its CMOS subs. The pixel size
// is the telescope's published one (binned as the sub was); the focal
// length is the one that gives the plate solution's pixel scale with that
// pixel, else the published one. Each card's comment says it is derived.
func derivedOptics(kw frameheader.Keywords, key string, solution []frameheader.Card) []imagedata.Card {
	focal, pixel := kw.Float("FOCALLEN"), kw.Float("XPIXSZ")
	if focal > 0 && pixel > 0 {
		return nil
	}
	t, known := knownTelescope(kw, key)
	var out []imagedata.Card
	if !(pixel > 0) {
		if !known {
			return nil
		}
		pixel = t.PixelUM * binning(kw)
		out = append(out, imagedata.FloatCard("XPIXSZ", math.Round(pixel*1000)/1000,
			"[um] derived: "+t.label()+", binned"))
	}
	if focal > 0 {
		return out
	}
	if f, _ := solutionOptics(cardKeywords(solution), pixel); f > 0 {
		return append(out, imagedata.FloatCard("FOCALLEN", math.Round(f*10)/10,
			"[mm] derived: plate solution scale and XPIXSZ"))
	}
	if known {
		return append(out, imagedata.FloatCard("FOCALLEN", t.FocalMM, "[mm] derived: "+t.label()))
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

// noPosition starts the error of a master that couldn't be solved for want
// of anywhere to start. It doesn't count against maxSolveAttempts: masters
// of targets whose subs had no pointing before it was read from their plate
// solutions were given up on that way.
const noPosition = "no position for"

// cardKeywords indexes cards by name, the first of each.
func cardKeywords(cards []frameheader.Card) frameheader.Keywords {
	kw := frameheader.Keywords{}
	for _, c := range cards {
		if _, ok := kw[c.Name]; !ok {
			kw[c.Name] = c.Value
		}
	}
	return kw
}

// solutionOptics is a focal length and pixel size, in mm and µm, that
// give the pixel scale of the plate solution in kw; pixel is kept when
// known. Both are 0 without a solution.
func solutionOptics(kw frameheader.Keywords, pixel float64) (float64, float64) {
	scale, ok := frameheader.PixelScale(kw)
	if !ok {
		return 0, 0
	}
	if !(pixel > 0) {
		pixel = 1
	}
	return pixel / 1000 / math.Tan(scale*deg), pixel
}

// plateSolveRevision counts changes to how masters are plate solved; a
// target given up on under an older one is tried again.
const plateSolveRevision = 1

// solveInput is what a master is plate solved from: where its reference
// pointed (NaN when nothing says) and its optics (0 when unknown).
type solveInput struct {
	ra, dec      float64
	focal, pixel float64
	optics       string
	opticsErr    error
	solver       string
}

// opticsOtherLight says a master's optics came from another light of its
// filter rather than its reference.
const opticsOtherLight = "another light"

// fingerprint names the inputs, so a target given up on is tried again
// once they change: a new way of finding its optics or position, or a new
// solver. Optics taken from another light depend on the filter, so they
// are named by where they came from, the same for every filter.
func (in solveInput) fingerprint() string {
	focal, pixel := in.focal, in.pixel
	if in.optics == opticsOtherLight {
		focal, pixel = 0, 0
	}
	return fmt.Sprintf("r%d ra=%.2f dec=%.2f focal=%.1f pixel=%.2f optics=%s solver=%s",
		plateSolveRevision, in.ra, in.dec, focal, pixel, in.optics, in.solver)
}

// solveDue reports whether a target without a plate solution should be
// solved with inputs fp: it has tries left, it was given up on for want of
// a position, or its inputs changed since it was given up on.
func solveDue(tr *app.TargetReference, fp string) bool {
	if tr.WCS != nil {
		return false
	}
	return tr.SolveAttempts < maxSolveAttempts ||
		(tr.SolveError != nil && strings.HasPrefix(*tr.SolveError, noPosition)) ||
		tr.SolveInputs != fp
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
func (p *Pipeline) plateSolution(ctx context.Context, tr *app.TargetReference, stack *app.Stack, refKey string, nina []frameheader.Card, dir, masterFile string) []frameheader.Card {
	if tr.WCS != nil {
		var cards []frameheader.Card
		if err := json.Unmarshal([]byte(*tr.WCS), &cards); err == nil {
			return cards
		}
	}
	if masterFile == "" {
		return nil
	}
	in := p.solveInputs(ctx, stack, refKey, nina)
	fp := in.fingerprint()
	if !solveDue(tr, fp) {
		return nil
	}
	if tr.SolveInputs != fp && tr.SolveAttempts >= maxSolveAttempts {
		slog.Info("Plate solving master again with new inputs", "object", stack.Object, "inputs", fp, "was", tr.SolveInputs)
		tr.SolveAttempts = 0
	}
	cards, err := p.solveMaster(ctx, in, stack, dir, masterFile)
	updates := map[string]any{colSolveAttempts: tr.SolveAttempts + 1, "solve_inputs": fp}
	if err != nil {
		msg := err.Error()
		updates["solve_error"] = msg
		tr.SolveError = &msg
		slog.Warn("Could not plate solve master", "object", stack.Object, "filter", stack.Filter, "error", err)
	} else {
		b, err := json.Marshal(cards)
		if err != nil {
			slog.Warn("Could not encode plate solution", "object", stack.Object, "filter", stack.Filter, "error", err)
		}
		wcs := string(b)
		updates["wcs"], updates["solve_error"] = wcs, nil
		tr.WCS, tr.SolveError = &wcs, nil
		slog.Info("Plate solved master", "object", stack.Object, "filter", stack.Filter)
	}
	tr.SolveAttempts++
	tr.SolveInputs = fp
	if err := p.db.WithContext(ctx).Model(tr).Updates(updates).Error; err != nil {
		slog.Warn("Could not save plate solution", "object", stack.Object, "error", err)
	}
	return cards
}

// solveInputs finds where a master's reference pointed (NINA's RA and DEC,
// the centre of the plate solution Telescope.live writes instead, or the
// target's position) and its optics (frameOptics, else another light of the
// target's).
func (p *Pipeline) solveInputs(ctx context.Context, stack *app.Stack, refKey string, nina []frameheader.Card) solveInput {
	kw := cardKeywords(nina)
	ref := frameheader.FromKeywords(kw)
	in := solveInput{ra: ref.RA, dec: ref.Dec}
	if math.IsNaN(in.ra) || math.IsNaN(in.dec) {
		in.ra, in.dec = math.NaN(), math.NaN()
		if pos, ok := p.targetPosition(ctx, stack.Object); ok {
			in.ra, in.dec = pos[0], pos[1]
		}
	}
	in.focal, in.pixel, in.optics = frameOptics(kw, refKey)
	if !(in.focal > 0) || !(in.pixel > 0) {
		in.focal, in.pixel, in.opticsErr = p.optics(ctx, stack.Object, stack.Filter)
		in.optics = opticsOtherLight
	}
	if p.opts.Astrometry != nil {
		in.solver = p.opts.Astrometry.Fingerprint()
	}
	return in
}

// solveMaster plate solves a master with Siril, starting from where its
// reference pointed with its optics, and returns the solution's keywords.
// When Siril can't (no position or optics, or it finds no match), a local
// astrometry.net solves it, searching near that position and pixel scale
// when they are known and blind otherwise.
func (p *Pipeline) solveMaster(ctx context.Context, in solveInput, stack *app.Stack, dir, masterFile string) ([]frameheader.Card, error) {
	positioned := !math.IsNaN(in.ra) && !math.IsNaN(in.dec)
	optics := in.focal > 0 && in.pixel > 0
	var sirilErr error
	switch {
	case !positioned:
		sirilErr = fmt.Errorf("%s %s", noPosition, stack.Object)
	case !optics:
		sirilErr = in.opticsErr
		if sirilErr == nil {
			sirilErr = fmt.Errorf("no optics for %s", stack.Object)
		}
	default:
		cards, err := p.sirilSolve(ctx, in, dir, masterFile)
		if err == nil {
			return cards, nil
		}
		sirilErr = err
	}
	asnet := p.opts.Astrometry
	if asnet == nil || !asnet.Ready() {
		return nil, sirilErr
	}
	hint := astrometry.Hint{RA: math.NaN(), Dec: math.NaN()}
	hint.Width, hint.Height, _ = fitsSize(masterFile)
	if positioned {
		hint.RA, hint.Dec, hint.Radius = in.ra, in.dec, searchRadius
	}
	if optics {
		scale := pixelScale(in.focal, in.pixel)
		hint.ScaleLow, hint.ScaleHigh = scale*scaleSlackLow, scale*scaleSlackHigh
	}
	all, err := asnet.Solve(ctx, dir, masterFile, hint)
	if err != nil {
		return nil, fmt.Errorf("%w; siril: %w", err, sirilErr)
	}
	cards := solutionCards(all)
	if !hasCard(cards, "CRVAL1") {
		return nil, fmt.Errorf("solve-field wrote no WCS; siril: %w", sirilErr)
	}
	slog.Info("Solved master with astrometry.net after Siril could not", "object", stack.Object, "filter", stack.Filter, "siril", sirilErr)
	return append(cards, frameheader.Card{Name: "PLTSOLVD", Value: "T", Comment: "solved by astrometry.net solve-field"}), nil
}

// searchRadius is how far, in degrees, solve-field searches from where the
// reference pointed: wider than the widest field (5.4°).
const searchRadius = 5

// scaleSlackLow and scaleSlackHigh bound solve-field's pixel scale around
// the one the optics give: Telescope.live's published optics can be a setup
// off (SPA-2 with and without its 0.75x reducer).
const (
	scaleSlackLow  = 0.7
	scaleSlackHigh = 1.4
)

func (p *Pipeline) sirilSolve(ctx context.Context, in solveInput, dir, masterFile string) ([]frameheader.Card, error) {
	solveDir, err := os.MkdirTemp(dir, "solve-")
	if err != nil {
		return nil, err
	}
	if err := os.Link(masterFile, filepath.Join(solveDir, "master.fit")); err != nil {
		return nil, err
	}
	var runErr error
	for _, extra := range []string{"", solveDownscale} {
		script := p.sirilPreamble(true) + fmt.Sprintf("load master\nplatesolve %.6f,%.6f -focal=%.2f -pixelsize=%.3f -force%s\nsave solved\n",
			in.ra, in.dec, in.focal, in.pixel, extra)
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
	cards := solutionCards(all)
	if !hasCard(cards, "CRVAL1") {
		return nil, fmt.Errorf("solved master has no WCS")
	}
	return cards, nil
}

// solutionCards are the plate solution's keywords among cards.
func solutionCards(all []frameheader.Card) []frameheader.Card {
	var cards []frameheader.Card
	for _, c := range all {
		if wcsKey.MatchString(c.Name) {
			cards = append(cards, c)
		}
	}
	return cards
}

// fitsSize is the width and height of the FITS image in file.
func fitsSize(file string) (int, int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	b := make([]byte, 2880)
	if _, err := io.ReadFull(f, b); err != nil {
		return 0, 0, err
	}
	kw, err := frameheader.Parse(b)
	if err != nil {
		return 0, 0, err
	}
	w, h := kw.Float("NAXIS1"), kw.Float("NAXIS2")
	if !(w > 0) || !(h > 0) {
		return 0, 0, fmt.Errorf("%s has no image size", file)
	}
	return int(w), int(h), nil
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
// their saved state, and those of targets with no plate solution that are
// due another try (solveDue), as when the solver's inputs changed since
// they were given up on. Targets a worker is stacking are skipped; the
// worker republishes them itself.
func (p *Pipeline) republishMasters(ctx context.Context) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).
		Where("state_key IS NOT NULL AND (master_version IS NULL OR master_version < ? OR object IN (?))", MasterVersion,
			p.db.Model(&app.TargetReference{}).Select("object").Where("wcs IS NULL")).
		Order("object, filter").Find(&stacks).Error; err != nil {
		slog.Warn("Could not find masters to republish", "error", err)
		return
	}
	plans := map[string]republishPlan{}
	done, marked, due := 0, 0, 0
	for i := range stacks {
		if p.stopping(ctx) {
			return
		}
		s := &stacks[i]
		plan, ok := plans[s.Object]
		if !ok {
			plan = p.republishPlan(ctx, s)
			plans[s.Object] = plan
		}
		if plan.unknown {
			continue
		}
		if !plan.rewrites(s.MasterVersion) {
			if s.MasterVersion < MasterVersion {
				if err := p.db.WithContext(ctx).Model(&app.Stack{}).Where("id = ?", s.ID).Update("master_version", MasterVersion).Error; err != nil {
					slog.Warn("Could not mark master", "object", s.Object, "filter", s.Filter, "error", err)
				} else {
					marked++
				}
			}
			continue
		}
		due++
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
		slog.Info("Republished masters", "masters", done, "of", due, "unchanged", marked)
	}
}

// republishPlan is why a target's masters are written again: their
// reference lacks optics derivedOptics would add, or a plate solve is due.
// unknown means the reference couldn't be read; its masters are left as
// they are until the next start.
type republishPlan struct {
	derive, solve, unknown bool
}

// rewrites reports whether a master written at version must be written
// again.
func (plan republishPlan) rewrites(version int) bool {
	switch {
	case plan.solve:
		return true
	case version >= MasterVersion:
		return false
	case version >= headerOnlyVersion:
		return plan.derive
	default:
		return true
	}
}

func (p *Pipeline) republishPlan(ctx context.Context, s *app.Stack) republishPlan {
	var tr app.TargetReference
	if err := p.db.WithContext(ctx).Where("object = ?", s.Object).First(&tr).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return republishPlan{}
	} else if err != nil {
		slog.Warn("Could not find registration reference; master left for the next start", "object", s.Object, "error", err)
		return republishPlan{unknown: true}
	}
	ref, cards := p.referenceCards(ctx, &tr)
	if cards == nil {
		return republishPlan{unknown: true}
	}
	var solution []frameheader.Card
	if tr.WCS != nil {
		_ = json.Unmarshal([]byte(*tr.WCS), &solution)
	}
	plan := republishPlan{derive: len(derivedOptics(cardKeywords(cards), ref.Key, solution)) > 0}
	if tr.WCS == nil {
		fp := p.solveInputs(ctx, s, ref.Key, cards).fingerprint()
		plan.solve = tr.SolveAttempts < maxSolveAttempts || tr.SolveInputs != fp
	}
	return plan
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
