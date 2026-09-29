package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

var nan = math.NaN()

// WarmUpSubs is how many subs a master needs before it is updated
// incrementally. Below it, every batch rebuilds the master with
// median-anchored rejection, so an early satellite trail never gets in.
const WarmUpSubs = 10

// calibrated is one sub after Siril's calibration, before registration.
type calibrated struct {
	c         candidate
	path      string
	darkScale float64
}

func (p *Pipeline) stackBatch(ctx context.Context, object, filter string, batch []candidate, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) error {
	start := time.Now()
	stack, err := p.loadStack(ctx, object, filter)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(p.workDir, "batch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	ref, err := p.reference(ctx, object, sets, scores, positions)
	if err != nil {
		return fmt.Errorf("reference for %s: %w", object, err)
	}

	p.progress(object, filter, StageCalibrating, 0, len(batch))
	cals, err := p.calibrate(ctx, dir, batch, sets)
	if err != nil {
		return err
	}
	p.progress(object, filter, StageRegistering, 0, len(cals))
	registered, err := p.register(ctx, dir, ref, cals)
	if err != nil {
		return err
	}

	// Store the registered subs; they're the input for rebuilds and for
	// integrating at the desk.
	var added []addedSub
	unregistered := 0
	for i, c := range cals {
		reg := registered[i]
		if reg == "" {
			unregistered++
			msg := "Siril could not register the sub to the target reference"
			if err := p.record(ctx, app.StackFrame{FrameID: c.c.frame.ID, Status: app.StackStatusRegistration,
				Score: c.c.score.Score, Exposure: *c.c.frame.Exposure, DarkScale: c.darkScale, Error: &msg}); err != nil {
				return err
			}
			continue
		}
		key := path.Join("registered", object, filter, strings.TrimSuffix(path.Base(c.c.frame.Key), path.Ext(c.c.frame.Key))+".fit")
		if err := p.upload(ctx, reg, key, "application/fits"); err != nil {
			return err
		}
		added = append(added, addedSub{c: c, local: reg, key: key})
	}
	if unregistered > 0 {
		// Many failures mean a poor reference: replace it and restack.
		if replaced, err := p.maybeReReference(ctx, object); err != nil || replaced {
			return err
		}
	}
	if len(added) == 0 {
		return nil
	}

	newTotal := stack.Subs + len(added)
	// A master holding subs calibrated again (a dark came for them) is
	// rebuilt, so their old versions go.
	rebuild := newTotal < WarmUpSubs || stack.RebuiltAtSubs < WarmUpSubs || newTotal >= 2*stack.RebuiltAtSubs || stack.NeedsRebuild
	var acc *Accumulator
	if !rebuild {
		if acc, err = p.loadState(ctx, stack); err != nil {
			slog.Warn("Could not load master state, rebuilding", "object", object, "filter", filter, "error", err)
			rebuild = true
		}
	}

	for i, a := range added {
		p.progress(object, filter, StageAdding, i, len(added))
		exp := *a.c.c.frame.Exposure
		sf := app.StackFrame{
			FrameID: a.c.c.frame.ID, StackID: &stack.ID, Status: app.StackStatusAdded,
			Score: a.c.c.score.Score, Weight: a.c.c.score.Score * exp, Exposure: exp,
			RegisteredKey: &a.key, DarkScale: a.c.darkScale, NoDark: a.c.c.cal.Dark.Set == nil && !precalibrated(a.c.c.frame),
		}
		if !rebuild {
			sub, w, h, err := readSub(a.local)
			if err != nil {
				return err
			}
			if w != acc.W || h != acc.H {
				return fmt.Errorf("registered sub is %dx%d, master %dx%d", w, h, acc.W, acc.H)
			}
			res, err := acc.Add(sub, exp, sf.Weight, p.opts.Stack)
			if err != nil {
				return err
			}
			sf.Used, sf.Rejected, sf.Saturated = res.Used, res.Rejected, res.Saturated
		}
		if err := p.record(ctx, sf); err != nil {
			return err
		}
	}

	if rebuild {
		if acc, err = p.rebuild(ctx, stack); err != nil {
			return fmt.Errorf("rebuild %s %s: %w", object, filter, err)
		}
		stack.RebuiltAtSubs = acc.Subs
		stack.NeedsRebuild = false
	}
	p.progress(object, filter, StagePublishing, 0, 0)
	if err := p.publish(ctx, stack, acc); err != nil {
		return err
	}
	slog.Info("Updated master", "object", object, "filter", filter, "added", len(added), "subs", acc.Subs,
		"rebuilt", rebuild, "duration", time.Since(start).Round(time.Second))
	return nil
}

type addedSub struct {
	c     calibrated
	local string
	key   string
}

func (p *Pipeline) loadStack(ctx context.Context, object, filter string) (*app.Stack, error) {
	var s app.Stack
	err := p.db.WithContext(ctx).Where("object = ? AND filter = ?", object, filter).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s = app.Stack{Object: object, Filter: filter, UpdatedAt: time.Now()}
		err = p.db.WithContext(ctx).Create(&s).Error
	}
	return &s, err
}

// calibrate runs Siril's calibrate_single on every sub with its matched
// masters, dark optimization, and hot pixel correction from the dark.
func (p *Pipeline) calibrate(ctx context.Context, dir string, batch []candidate, sets []calmatch.Set) ([]calibrated, error) {
	var sb strings.Builder
	sb.WriteString(p.sirilPreamble(true))
	out := make([]calibrated, 0, len(batch))
	for i, c := range batch {
		p.progress(c.frame.Object, c.frame.Filter, StageCalibrating, i, len(batch))
		raw := fmt.Sprintf("raw%04d%s", i, strings.ToLower(path.Ext(c.frame.Key)))
		if err := p.download(ctx, p.source, c.frame.Key, filepath.Join(dir, raw)); err != nil {
			return nil, err
		}
		if precalibrated(c.frame) {
			// Only brought to 32 bits and FITS like the calibrated ones.
			fmt.Fprintf(&sb, "load %s\nsave pp_raw%04d\n", raw, i)
			out = append(out, calibrated{c: c, path: filepath.Join(dir, fmt.Sprintf("pp_raw%04d.fit", i))})
			continue
		}
		bias, err := p.masterFor(ctx, *c.cal.Bias.Set, sets)
		if err != nil {
			return nil, err
		}
		flat, err := p.masterFor(ctx, *c.cal.Flat.Set, sets)
		if err != nil {
			return nil, err
		}
		// Without a dark for the lights' setpoint the sub is calibrated
		// with bias and flat only; it is calibrated again when one comes.
		dark := ""
		if c.cal.Dark.Set != nil {
			if dark, err = p.masterFor(ctx, *c.cal.Dark.Set, sets); err != nil {
				return nil, err
			}
		}
		for _, m := range []string{bias, dark, flat} {
			if m == "" {
				continue
			}
			if _, err := siril.Path(m); err != nil {
				return nil, err
			}
		}
		// calibrate_single only reads FITS, so convert XISF subs first.
		fits := fmt.Sprintf("raw%04d.fit", i)
		if !strings.EqualFold(path.Ext(raw), ".fit") {
			fmt.Fprintf(&sb, "load %s\nsave %s\n", raw, strings.TrimSuffix(fits, ".fit"))
		}
		if dark != "" {
			fmt.Fprintf(&sb, "calibrate_single %s -bias=%s -dark=%s -flat=%s -cc=dark -opt -prefix=pp_\n",
				fits, bias, dark, flat)
		} else {
			fmt.Fprintf(&sb, "calibrate_single %s -bias=%s -flat=%s -prefix=pp_\n", fits, bias, flat)
		}
		out = append(out, calibrated{c: c, path: filepath.Join(dir, fmt.Sprintf("pp_raw%04d.fit", i))})
	}
	res, err := p.siril.Run(ctx, dir, sb.String())
	if err != nil {
		return nil, fmt.Errorf("calibrate: %w", err)
	}
	// Siril logs a dark scale for each sub calibrated with a dark, in order.
	scales := siril.DarkScales(res.Log)
	next := 0
	for i := range out {
		if _, err := os.Stat(out[i].path); err != nil {
			return nil, fmt.Errorf("calibrate: no output for %s", batch[i].frame.Key)
		}
		if batch[i].cal.Dark.Set != nil && next < len(scales) {
			out[i].darkScale = scales[next]
			next++
		}
	}
	return out, nil
}

// register aligns the calibrated subs to the target reference in one Siril
// run. It returns each sub's registered file, or "" if it didn't register.
func (p *Pipeline) register(ctx context.Context, dir, ref string, cals []calibrated) ([]string, error) {
	reg := filepath.Join(dir, "reg")
	if err := os.Mkdir(reg, 0o700); err != nil {
		return nil, err
	}
	if err := os.Symlink(ref, filepath.Join(reg, "seq_00001.fit")); err != nil {
		return nil, err
	}
	for i, c := range cals {
		if err := os.Rename(c.path, filepath.Join(reg, fmt.Sprintf("seq_%05d.fit", i+2))); err != nil {
			return nil, err
		}
	}
	// Registered subs are stored as 16-bit: calibrated data comes from a
	// 16-bit sensor, and the rounding is far below the noise.
	script := p.sirilPreamble(false) + "cd reg\nsetref seq_ 1\nregister seq_ -prefix=r_\n"
	if res, err := p.siril.Run(ctx, dir, script); err != nil {
		// When no sub matches the reference, Siril fails the whole script.
		// Those subs are recorded as unregistered like any other failure.
		if !strings.Contains(res.Log, "No image was registered to the reference") {
			return nil, fmt.Errorf("register: %w", err)
		}
		slog.Warn("No sub in the batch matched the reference", "subs", len(cals))
	}
	out := make([]string, len(cals))
	for i := range cals {
		f := filepath.Join(reg, fmt.Sprintf("r_seq_%05d.fit", i+2))
		if _, err := os.Stat(f); err == nil {
			out[i] = f
		}
	}
	return out, nil
}

// reference returns the calibrated frame a target's subs are registered to,
// choosing it from all of the target's usable lights the first time.
func (p *Pipeline) reference(ctx context.Context, object string, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) (string, error) {
	h := sha256.Sum256([]byte(object))
	local := filepath.Join(p.workDir, "references", hex.EncodeToString(h[:8])+".fit")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	var tr app.TargetReference
	err := p.db.WithContext(ctx).Where("object = ?", object).First(&tr).Error
	if err == nil {
		return local, p.download(ctx, p.dest, tr.ObjectKey, local)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	var frames []app.Frame
	if err := p.db.WithContext(ctx).Where("type = ? AND object = ? AND index_error IS NULL AND filter <> ''", "LIGHT", object).
		Find(&frames).Error; err != nil {
		return "", fmt.Errorf("load lights: %w", err)
	}
	var usable []candidate
	for _, f := range frames {
		if c, status := p.classify(f, scores, sets, positions); status == "" {
			usable = append(usable, c)
		}
	}
	// Subs framed differently register poorly against each other, so the
	// reference comes from the framing most subs share.
	best, ok := pickReference(mainFraming(usable))
	if !ok {
		return "", fmt.Errorf("no usable lights")
	}
	dir, err := os.MkdirTemp(p.workDir, "ref-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cals, err := p.calibrate(ctx, dir, []candidate{best}, sets)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(cals[0].path, local); err != nil {
		return "", err
	}
	key := path.Join("stacks", object, "reference.fit")
	if err := p.upload(ctx, local, key, "application/fits"); err != nil {
		return "", err
	}
	tr = app.TargetReference{Object: object, FrameID: best.frame.ID, ObjectKey: key, CreatedAt: time.Now()}
	if err := p.db.WithContext(ctx).Create(&tr).Error; err != nil {
		return "", err
	}
	slog.Info("Chose registration reference", "object", object, "frame", best.frame.Key,
		"hfr", best.score.HFR, "stars", best.score.Stars, "score", best.score.Score)
	return local, nil
}

// pickReference chooses the sharpest sub among those with plenty of stars.
// Star matching fails against a soft reference: its brightest stars are
// not the same ones a sharp sub finds. Subs with under three quarters of the
// best star count are skipped, as clouds or haze, before comparing HFR.
// maxReferenceEccentricity is the most elongated a reference's stars may be.
const maxReferenceEccentricity = 0.6

func pickReference(cands []candidate) (candidate, bool) {
	most := 0
	for _, c := range cands {
		most = max(most, c.score.Stars)
	}
	var best candidate
	found := false
	for _, c := range cands {
		if c.score.HFR <= 0 || 4*c.score.Stars < 3*most {
			continue
		}
		// Elongated stars (wind, guiding) change which stars are brightest.
		if c.score.Eccentricity > maxReferenceEccentricity {
			continue
		}
		if !found || c.score.HFR < best.score.HFR {
			best, found = c, true
		}
	}
	if !found && len(cands) > 0 {
		// No star measurements: fall back to the best score.
		best, found = cands[0], true
		for _, c := range cands[1:] {
			if c.score.Score > best.score.Score {
				best = c
			}
		}
	}
	return best, found
}

// readSub decodes a registered sub on the 0-1 scale.
func readSub(file string) ([]float32, int, int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, 0, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return im.Plane(0), im.W, im.H, nil
}
