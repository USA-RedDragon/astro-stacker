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
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// WarmUpSubs is how many subs a master needs before it is updated
// incrementally. Below it, every batch rebuilds the master with
// median-anchored rejection, so an early satellite trail never gets in.
const WarmUpSubs = 10

// calibrated is one sub after Siril's calibration, before registration.
type calibrated struct {
	c         candidate
	path      string
	darkScale float64
	// The keys of the masters it was calibrated with; dark is empty when
	// there was none.
	bias, dark, flat string
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

	ref, tr, err := p.reference(ctx, object, sets, scores, positions)
	if err != nil {
		return fmt.Errorf("reference for %s: %w", object, err)
	}
	// The reference's light may have left the bucket; its calibrated copy
	// is still the reference, registered by stars.
	var refFrame app.Frame
	if err := p.db.WithContext(ctx).First(&refFrame, tr.FrameID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("reference frame for %s: %w", object, err)
	}
	if retrySolve(tr, refFrame) {
		// Missed at startup (reregisterPrecalibrated) while being stacked.
		slog.Warn("Restacking target to register its calibrated subs with the optics' distortion", "object", object)
		return p.restack(ctx, object, nil)
	}
	disto := registerWithDistortion(tr, refFrame, batch)

	p.progress(object, filter, StageCalibrating, 0, len(batch))
	cals, err := p.calibrate(ctx, dir, batch, sets)
	if err != nil {
		return err
	}
	p.progress(object, filter, StageRegistering, 0, len(cals))
	registered, err := p.register(ctx, dir, ref, cals, disto)
	if err != nil {
		return err
	}

	// Store the registered subs; they're the input for rebuilds and for
	// integrating at the desk.
	added, unregistered, err := p.storeRegistered(ctx, object, filter, cals, registered)
	if err != nil {
		return err
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
	// A sub of another gain goes on the master's with the scale its last
	// rebuild measured; a gain it hasn't measured needs a rebuild.
	gains := make([]*float64, len(added))
	for i, a := range added {
		gains[i] = a.c.c.frame.Gain
	}
	scales, measured, err := p.batchGains(ctx, stack, gains)
	if err != nil {
		return err
	}
	rebuild = rebuild || !measured
	var acc *Accumulator
	if !rebuild {
		if acc, err = p.loadState(ctx, stack); err != nil {
			slog.Warn("Could not load master state, rebuilding", "object", object, "filter", filter, "error", err)
			rebuild = true
		}
	}

	if err := p.addSubs(ctx, object, filter, stack, added, gains, scales, acc, rebuild); err != nil {
		return err
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

func (p *Pipeline) addSubs(ctx context.Context, object, filter string, stack *app.Stack, added []addedSub, gains []*float64,
	scales gainTable, acc *Accumulator, rebuild bool,
) error {
	for i, a := range added {
		p.progress(object, filter, StageAdding, i, len(added))
		exp := *a.c.c.frame.Exposure
		sf := app.StackFrame{
			FrameID: a.c.c.frame.ID, StackID: &stack.ID, Status: app.StackStatusAdded,
			Score: a.c.c.score.Score, Weight: a.c.c.score.Score * exp, Exposure: exp,
			RegisteredKey: &a.key, DarkScale: a.c.darkScale, NoDark: a.c.c.cal.Dark.Set == nil && !precalibrated(a.c.c.frame),
			BiasMaster: optional(a.c.bias), DarkMaster: optional(a.c.dark), FlatMaster: optional(a.c.flat),
		}
		if !rebuild {
			sub, w, h, err := readSub(a.local)
			if err != nil {
				return err
			}
			if w != acc.W || h != acc.H {
				return fmt.Errorf("registered sub is %dx%d, master %dx%d", w, h, acc.W, acc.H)
			}
			res, err := acc.Add(sub, exp*scales.scale(gains[i]), sf.Weight, p.opts.Stack)
			if err != nil {
				return err
			}
			sf.Used, sf.Rejected, sf.Saturated = res.Used, res.Rejected, res.Saturated
		}
		if err := p.record(ctx, sf); err != nil {
			return err
		}
	}
	return nil
}

// storeRegistered uploads the subs that registered and records those that
// didn't. unregistered counts the failures that say the reference may be
// poor; a sub whose mount pointing was off target and that didn't register
// is off target instead, and isn't counted: a night of a parked mount's
// subs says nothing about the reference.
func (p *Pipeline) storeRegistered(ctx context.Context, object, filter string, cals []calibrated, registered []string) ([]addedSub, int, error) {
	var added []addedSub
	unregistered := 0
	for i, c := range cals {
		reg := registered[i]
		if reg == "" {
			sf := app.StackFrame{FrameID: c.c.frame.ID, Status: app.StackStatusRegistration,
				Score: c.c.score.Score, Exposure: *c.c.frame.Exposure, DarkScale: c.darkScale}
			if c.c.offBy > 0 {
				sf.Status, sf.Error = app.StackStatusOffTarget, offTargetError(c.c.offBy)
			} else {
				unregistered++
				msg := "Siril could not register the sub to the target reference"
				sf.Error = &msg
			}
			if err := p.record(ctx, sf); err != nil {
				return nil, 0, err
			}
			continue
		}
		if c.c.offBy > 0 {
			slog.Info("Sub registered though its mount pointed elsewhere", "object", object, "filter", filter,
				"frame", c.c.frame.Key, "off_by", fmt.Sprintf("%.1f°", c.c.offBy))
		}
		key := registeredKey(object, filter, c.c.frame.Key)
		local := strings.TrimSuffix(reg, filepath.Ext(reg)) + registeredExt
		if err := encodeRegisteredFile(reg, local); err != nil {
			return nil, 0, err
		}
		if err := p.upload(ctx, local, key, registeredContentType); err != nil {
			return nil, 0, err
		}
		added = append(added, addedSub{c: c, local: local, key: key})
	}
	return added, unregistered, nil
}

// optional is nil for an empty string.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
		// Its subs are scored as they are classified now.
		s = app.Stack{Object: object, Filter: filter, UpdatedAt: time.Now(), ScoreMethod: scoreMethod}
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
			// Brought to FITS like the calibrated ones, with the hot
			// columns and pixels their calibration left taken out.
			list, err := writeCosmeList(filepath.Join(dir, raw), filepath.Join(dir, fmt.Sprintf("raw%04d.lst", i)), p.opts.Stack.SaturationLevel)
			if err != nil {
				return nil, fmt.Errorf("find bad columns in %s: %w", c.frame.Key, err)
			}
			fmt.Fprintf(&sb, "load %s\n", raw)
			if list != "" {
				fmt.Fprintf(&sb, "cosme %s\n", filepath.Base(list))
			}
			fmt.Fprintf(&sb, "find_cosme %g %g\nsave pp_raw%04d\n", cosmeColdSigma, cosmeHotSigma, i)
			out = append(out, calibrated{c: c, path: filepath.Join(dir, fmt.Sprintf("pp_raw%04d.fit", i))})
			continue
		}
		bias, biasKey, err := p.masterFor(ctx, *c.cal.Bias.Set, sets)
		if err != nil {
			return nil, err
		}
		flat, flatKey, err := p.masterFor(ctx, *c.cal.Flat.Set, sets)
		if err != nil {
			return nil, err
		}
		// Without a dark for the lights' setpoint the sub is calibrated
		// with bias and flat only; it is calibrated again when one comes.
		dark, darkKey := "", ""
		if c.cal.Dark.Set != nil {
			if dark, darkKey, err = p.masterFor(ctx, *c.cal.Dark.Set, sets); err != nil {
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
		out = append(out, calibrated{c: c, path: filepath.Join(dir, fmt.Sprintf("pp_raw%04d.fit", i)),
			bias: biasKey, dark: darkKey, flat: flatKey})
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
			return nil, fmt.Errorf("calibrate: no output for %s", out[i].c.frame.Key)
		}
		if out[i].c.cal.Dark.Set != nil && next < len(scales) {
			out[i].darkScale = scales[next]
			next++
		}
	}
	return out, nil
}

// register aligns the calibrated subs to the target reference in one Siril
// run. It returns each sub's registered file, or "" if it didn't register.
// With disto, star positions are matched and the subs resampled free of the
// distortion in the reference's plate solution (see registerWithDistortion).
func (p *Pipeline) register(ctx context.Context, dir, ref string, cals []calibrated, disto bool) ([]string, error) {
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
	script := p.sirilPreamble(false) + "cd reg\nsetref seq_ 1\n" + registerCommand(disto)
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
// choosing it from all of the target's usable lights the first time, and
// its record.
func (p *Pipeline) reference(ctx context.Context, object string, sets []calmatch.Set, scores map[string]quality.SubScore, positions map[string][2]float64) (string, app.TargetReference, error) {
	h := sha256.Sum256([]byte(object))
	local := filepath.Join(p.workDir, "references", hex.EncodeToString(h[:8])+".fit")
	var tr app.TargetReference
	err := p.db.WithContext(ctx).Where("object = ?", object).First(&tr).Error
	if err == nil {
		if _, err := os.Stat(local); err == nil {
			return local, tr, nil
		}
		return local, tr, p.download(ctx, p.dest, tr.ObjectKey, local)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", tr, err
	}

	var frames []app.Frame
	if err := p.db.WithContext(ctx).Where("type = ? AND object = ? AND index_error IS NULL AND filter <> ''", "LIGHT", object).
		Find(&frames).Error; err != nil {
		return "", tr, fmt.Errorf("load lights: %w", err)
	}
	var usable []candidate
	for _, f := range frames {
		if c, status := p.classify(f, scores, sets, positions); status == "" {
			usable = append(usable, c)
		}
	}
	usable = referenceCandidates(usable)
	p.readPointings(ctx, usable)
	// Subs framed differently register poorly against each other, so the
	// reference comes from the framing most subs share.
	best, ok := pickReference(mainFraming(usable))
	if !ok {
		return "", tr, fmt.Errorf("no usable lights")
	}
	dir, err := os.MkdirTemp(p.workDir, "ref-")
	if err != nil {
		return "", tr, err
	}
	defer os.RemoveAll(dir)
	cals, err := p.calibrate(ctx, dir, []candidate{best}, sets)
	if err != nil {
		return "", tr, err
	}
	file, registration, solved := cals[0].path, registrationStars, 0
	if precalibrated(best.frame) {
		solved = solveRevision
		// Subs that come calibrated are from remote telescopes, pointed
		// far apart: they are registered with the optics' distortion,
		// which the reference's plate solution gives.
		if solvedFile, err := p.solveReference(ctx, dir, file, best.frame); err != nil {
			slog.Warn("Could not solve the reference for its distortion; registering by stars", "object", object, "error", err)
		} else {
			file, registration = solvedFile, registrationDistortion
		}
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", tr, err
	}
	if err := os.Rename(file, local); err != nil {
		return "", tr, err
	}
	key := path.Join("stacks", object, "reference.fit")
	if err := p.upload(ctx, local, key, "application/fits"); err != nil {
		return "", tr, err
	}
	tr = app.TargetReference{Object: object, FrameID: best.frame.ID, ObjectKey: key, CreatedAt: time.Now(),
		Registration: registration, SolveRevision: solved}
	if err := p.db.WithContext(ctx).Create(&tr).Error; err != nil {
		return "", tr, err
	}
	slog.Info("Chose registration reference", "object", object, "frame", best.frame.Key,
		"hfr", best.score.HFR, "stars", best.score.Stars, "score", best.score.Score, "registration", registration)
	return local, tr, nil
}

// readPointings reads the pointing of calibrated subs that have none
// recorded from their headers, so the reference is chosen by framing even
// before the indexer's backfill has reached them.
func (p *Pipeline) readPointings(ctx context.Context, cands []candidate) {
	for i := range cands {
		f := &cands[i].frame
		if f.MountRA != nil || !precalibrated(*f) {
			continue
		}
		kw, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: f.Key, Size: f.Size})
		if err != nil {
			continue
		}
		if h := frameheader.FromKeywords(kw); !math.IsNaN(h.RA) && !math.IsNaN(h.Dec) {
			f.MountRA, f.MountDec = &h.RA, &h.Dec
		}
	}
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
