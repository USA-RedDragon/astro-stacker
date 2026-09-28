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

func (p *Pipeline) stackBatch(ctx context.Context, object, filter string, batch []candidate, sets []calmatch.Set) error {
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

	ref, err := p.reference(ctx, object, batch, sets)
	if err != nil {
		return fmt.Errorf("reference for %s: %w", object, err)
	}

	cals, err := p.calibrate(ctx, dir, batch, sets)
	if err != nil {
		return err
	}
	registered, err := p.register(ctx, dir, ref, cals)
	if err != nil {
		return err
	}

	// Store the registered subs; they're the input for rebuilds and for
	// integrating at the desk.
	var added []addedSub
	for i, c := range cals {
		reg := registered[i]
		if reg == "" {
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
	if len(added) == 0 {
		return nil
	}

	newTotal := stack.Subs + len(added)
	rebuild := newTotal < WarmUpSubs || stack.RebuiltAtSubs < WarmUpSubs || newTotal >= 2*stack.RebuiltAtSubs
	var acc *Accumulator
	if !rebuild {
		if acc, err = p.loadState(ctx, stack); err != nil {
			slog.Warn("Could not load master state, rebuilding", "object", object, "filter", filter, "error", err)
			rebuild = true
		}
	}

	for _, a := range added {
		exp := *a.c.c.frame.Exposure
		sf := app.StackFrame{
			FrameID: a.c.c.frame.ID, StackID: &stack.ID, Status: app.StackStatusAdded,
			Score: a.c.c.score.Score, Weight: a.c.c.score.Score * exp, Exposure: exp,
			RegisteredKey: &a.key, DarkScale: a.c.darkScale,
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
	}
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
		raw := fmt.Sprintf("raw%04d%s", i, strings.ToLower(path.Ext(c.frame.Key)))
		if err := p.download(ctx, p.source, c.frame.Key, filepath.Join(dir, raw)); err != nil {
			return nil, err
		}
		bias, err := p.masterFor(ctx, *c.cal.Bias.Set, sets)
		if err != nil {
			return nil, err
		}
		dark, err := p.masterFor(ctx, *c.cal.Dark.Set, sets)
		if err != nil {
			return nil, err
		}
		flat, err := p.masterFor(ctx, *c.cal.Flat.Set, sets)
		if err != nil {
			return nil, err
		}
		for _, m := range []string{bias, dark, flat} {
			if _, err := siril.Path(m); err != nil {
				return nil, err
			}
		}
		// calibrate_single only reads FITS, so convert XISF subs first.
		fits := fmt.Sprintf("raw%04d.fit", i)
		if !strings.EqualFold(path.Ext(raw), ".fit") {
			fmt.Fprintf(&sb, "load %s\nsave %s\n", raw, strings.TrimSuffix(fits, ".fit"))
		}
		fmt.Fprintf(&sb, "calibrate_single %s -bias=%s -dark=%s -flat=%s -cc=dark -opt -prefix=pp_\n",
			fits, bias, dark, flat)
		out = append(out, calibrated{c: c, path: filepath.Join(dir, fmt.Sprintf("pp_raw%04d.fit", i))})
	}
	res, err := p.siril.Run(ctx, dir, sb.String())
	if err != nil {
		return nil, fmt.Errorf("calibrate: %w", err)
	}
	scales := siril.DarkScales(res.Log)
	for i := range out {
		if _, err := os.Stat(out[i].path); err != nil {
			return nil, fmt.Errorf("calibrate: no output for %s", batch[i].frame.Key)
		}
		if i < len(scales) {
			out[i].darkScale = scales[i]
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
	if _, err := p.siril.Run(ctx, dir, script); err != nil {
		return nil, fmt.Errorf("register: %w", err)
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
// choosing the best-scored sub of the first batch the first time.
func (p *Pipeline) reference(ctx context.Context, object string, batch []candidate, sets []calmatch.Set) (string, error) {
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

	best := batch[0]
	for _, c := range batch[1:] {
		if c.score.Score > best.score.Score {
			best = c
		}
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
	slog.Info("Chose registration reference", "object", object, "frame", best.frame.Key, "score", best.score.Score)
	return local, nil
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
