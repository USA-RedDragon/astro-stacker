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
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/pixinsight-worker/internal/calmatch"
	"github.com/USA-RedDragon/pixinsight-worker/internal/coverage"
	"github.com/USA-RedDragon/pixinsight-worker/internal/siril"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
	"gorm.io/gorm"
)

// masterVersion changes when the way masters are built changes, so old
// masters are rebuilt instead of reused.
const masterVersion = "2"

// masterFor returns a local path to the master for a calibration set,
// building it from the raw frames the first time. Darks and flats are
// calibrated with the bias matched to their own gain and offset before
// stacking: Siril computes light - bias - k·dark, so the master dark must
// hold only the thermal signal.
func (p *Pipeline) masterFor(ctx context.Context, set calmatch.Set, all []calmatch.Set) (string, error) {
	frames, err := coverage.SetFrames(ctx, p.db, set)
	if err != nil {
		return "", err
	}
	if len(frames) < 3 {
		return "", fmt.Errorf("%s set has %d frames, need at least 3", set.Type, len(frames))
	}
	key := setKey(set.Type, frames)
	local := filepath.Join(p.workDir, "masters", key+".fit")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}

	var cm app.CalibrationMaster
	err = p.db.WithContext(ctx).Where("set_key = ?", key).First(&cm).Error
	switch {
	case err == nil:
		if err := p.download(ctx, p.dest, cm.ObjectKey, local); err != nil {
			return "", err
		}
		return local, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", err
	}

	var bias string
	if set.Type == "FLAT" || set.Type == "DARK" {
		m := calmatch.Choose(calmatch.Group{
			Night: set.Night, Filter: set.Filter, Exposure: set.Exposure, Gain: set.Gain,
			Offset: set.Offset, SetTemp: set.SetTemp, BinX: set.BinX, Rotator: set.Rotator,
		}, all).Bias
		if m.Set == nil {
			return "", fmt.Errorf("no bias for %s at gain %v offset %v", strings.ToLower(set.Type), set.Gain, set.Offset)
		}
		if bias, err = p.masterFor(ctx, *m.Set, all); err != nil {
			return "", fmt.Errorf("bias for %s: %w", strings.ToLower(set.Type), err)
		}
	}

	start := time.Now()
	if err := p.buildMaster(ctx, set.Type, frames, bias, local); err != nil {
		return "", err
	}
	objectKey := path.Join("calibration", strings.ToLower(set.Type), key+".fit")
	if err := p.upload(ctx, local, objectKey, "application/fits"); err != nil {
		return "", err
	}
	cm = app.CalibrationMaster{SetKey: key, Type: set.Type, ObjectKey: objectKey, Frames: len(frames), BuiltAt: time.Now()}
	if err := p.db.WithContext(ctx).Create(&cm).Error; err != nil {
		return "", fmt.Errorf("record master: %w", err)
	}
	slog.Info("Built calibration master", "type", set.Type, "night", set.Night.Format("2006-01-02"),
		"filter", set.Filter, "frames", len(frames), "duration", time.Since(start).Round(time.Second))
	return local, nil
}

// setKey identifies a set by its frames' keys and ETags.
func setKey(typ string, frames []app.Frame) string {
	ids := make([]string, 0, len(frames))
	for _, f := range frames {
		ids = append(ids, f.Key+"\x00"+f.ETag)
	}
	sort.Strings(ids)
	h := sha256.New()
	h.Write([]byte(masterVersion))
	h.Write([]byte(typ))
	for _, id := range ids {
		h.Write([]byte{0})
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (p *Pipeline) buildMaster(ctx context.Context, typ string, frames []app.Frame, bias, out string) error {
	dir, err := os.MkdirTemp(p.workDir, "master-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in")
	if err := os.Mkdir(in, 0o700); err != nil {
		return err
	}
	for i, f := range frames {
		// Siril's convert picks up every image in the directory; plain
		// names keep spaces in target names out of the script.
		name := fmt.Sprintf("f%04d%s", i, strings.ToLower(path.Ext(f.Key)))
		if err := p.download(ctx, p.source, f.Key, filepath.Join(in, name)); err != nil {
			return err
		}
	}

	var sb strings.Builder
	sb.WriteString(p.sirilPreamble(true))
	sb.WriteString("cd in\nconvert f -out=../w\ncd ../w\n")
	// The output is relative to Siril's working directory, w.
	result := filepath.Join(dir, "w", "master")
	switch typ {
	case "FLAT", "DARK":
		b, err := siril.Path(bias)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "calibrate f_ -bias=%s\n", b)
		norm := "-norm=mul"
		if typ == "DARK" {
			norm = "-nonorm"
		}
		fmt.Fprintf(&sb, "stack pp_f_ rej w 3 3 %s -out=master\n", norm)
	case "BIAS":
		sb.WriteString("stack f_ rej w 3 3 -nonorm -out=master\n")
	default:
		return fmt.Errorf("no master for %q frames", typ)
	}
	if _, err := p.siril.Run(ctx, dir, sb.String()); err != nil {
		return fmt.Errorf("build %s master: %w", strings.ToLower(typ), err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	return os.Rename(result+".fit", out)
}

// sirilPreamble limits Siril's threads and memory so it shares the pod with
// the worker, and sets the output bit depth.
func (p *Pipeline) sirilPreamble(float32bit bool) string {
	depth := "set16bits"
	if float32bit {
		depth = "set32bits"
	}
	ratio := math.Max(0.05, math.Min(0.9, p.opts.SirilMemoryRatio))
	return fmt.Sprintf("%s\nsetext fit\nsetcpu %d\nsetmem %.2f\n", depth, max(1, p.opts.SirilThreads), ratio)
}
