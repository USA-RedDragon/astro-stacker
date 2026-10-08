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

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// masterVersion changes when the way masters are built changes, so old
// masters are rebuilt instead of reused.
const masterVersion = "2"

// masterFor returns a local path to the master for a calibration set, and
// its key, building it from the raw frames the first time. Darks and flats are
// calibrated with the bias matched to their own gain and offset before
// stacking: Siril computes light - bias - k·dark, so the master dark must
// hold only the thermal signal.
func (p *Pipeline) masterFor(ctx context.Context, set calmatch.Set, all []calmatch.Set) (string, string, error) {
	if set.Master != "" {
		return p.importedMaster(ctx, set, all)
	}
	frames, err := coverage.SetFrames(ctx, p.db, set)
	if err != nil {
		return "", "", err
	}
	if len(frames) < 3 {
		return "", "", fmt.Errorf("%s set has %d frames, need at least 3", set.Type, len(frames))
	}
	key := setKey(set.Type, frames)
	defer p.lockKey(key)()
	local := filepath.Join(p.workDir, "masters", key+".fit")

	var cm app.CalibrationMaster
	err = p.db.WithContext(ctx).Where("set_key = ?", key).First(&cm).Error
	switch {
	case err == nil:
		if cm.Exposure == nil && !math.IsNaN(set.Exposure) {
			// Built before masters recorded their setup.
			if err := p.db.WithContext(ctx).Model(&cm).UpdateColumns(masterSetup(set)).Error; err != nil {
				return "", "", fmt.Errorf("record master setup: %w", err)
			}
		}
		if _, err := os.Stat(local); err == nil {
			return local, key, nil
		}
		if err := p.download(ctx, p.dest, cm.ObjectKey, local); err != nil {
			return "", "", err
		}
		return local, key, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", "", err
	}
	// Lights matched to a set still arriving wait for it (classify); this
	// catches the rest, such as the bias a flat or dark is calibrated with.
	if !p.settled(set, time.Now()) {
		return "", "", fmt.Errorf("%s set of %d frames is still arriving, the last uploaded %s",
			strings.ToLower(set.Type), len(frames), set.Uploaded.UTC().Format(time.RFC3339))
	}

	var bias string
	if set.Type == "FLAT" || set.Type == frameTypeDark {
		m := calmatch.Choose(calmatch.Group{
			Night: set.Night, Filter: set.Filter, Exposure: set.Exposure, Gain: set.Gain,
			Offset: set.Offset, SetTemp: set.SetTemp, BinX: set.BinX, Rotator: set.Rotator,
		}, all).Bias
		if m.Set == nil {
			return "", "", fmt.Errorf("no bias for %s at gain %v offset %v", strings.ToLower(set.Type), set.Gain, set.Offset)
		}
		if bias, _, err = p.masterFor(ctx, *m.Set, all); err != nil {
			return "", "", fmt.Errorf("bias for %s: %w", strings.ToLower(set.Type), err)
		}
	}

	start := time.Now()
	if err := p.buildMaster(ctx, set.Type, frames, bias, local); err != nil {
		return "", "", err
	}
	objectKey := path.Join("calibration", strings.ToLower(set.Type), key+".fit")
	if err := p.upload(ctx, local, objectKey, "application/fits"); err != nil {
		return "", "", err
	}
	cm = app.CalibrationMaster{SetKey: key, Type: set.Type, ObjectKey: objectKey, Frames: len(frames), BuiltAt: time.Now()}
	if err := p.db.WithContext(ctx).Create(&cm).Error; err != nil {
		return "", "", fmt.Errorf("record master: %w", err)
	}
	if err := p.db.WithContext(ctx).Model(&cm).UpdateColumns(masterSetup(set)).Error; err != nil {
		return "", "", fmt.Errorf("record master setup: %w", err)
	}
	slog.Info("Built calibration master", "type", set.Type, "night", set.Night.Format("2006-01-02"),
		"filter", set.Filter, "frames", len(frames), "duration", time.Since(start).Round(time.Second))
	return local, key, nil
}

// masterSetup is the columns describing the set a master is built from.
func masterSetup(set calmatch.Set) map[string]any {
	num := func(v float64) *float64 {
		if math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return map[string]any{
		"night": set.Night, "filter": set.Filter, "exposure": num(set.Exposure), "gain": num(set.Gain),
		"offset": num(set.Offset), "set_temp": num(set.SetTemp), "bin_x": num(set.BinX),
	}
}

// importedKey names a master made elsewhere (coverage.Imported) in the
// work directory and on the lights calibrated with it.
func importedKey(set calmatch.Set) string {
	h := sha256.Sum256([]byte(masterVersion + "\x00" + set.Type + "\x00" + set.Master))
	return "imported-" + hex.EncodeToString(h[:16])
}

// importedMaster returns a local FITS copy of a master made elsewhere
// (coverage.Imported). A dark has the bias taken out, as the darks built
// here do: WBPP's master darks keep it.
func (p *Pipeline) importedMaster(ctx context.Context, set calmatch.Set, all []calmatch.Set) (string, string, error) {
	key := importedKey(set)
	defer p.lockKey(key)()
	local := filepath.Join(p.workDir, "masters", key+".fit")
	if _, err := os.Stat(local); err == nil {
		return local, key, nil
	}
	file, err := p.importMaster(ctx, set, all, local)
	return file, key, err
}

func (p *Pipeline) importMaster(ctx context.Context, set calmatch.Set, all []calmatch.Set, local string) (string, error) {
	dir, err := os.MkdirTemp(p.workDir, "imported-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "master"+strings.ToLower(path.Ext(set.Master)))
	if err := p.download(ctx, p.source, set.Master, src); err != nil {
		return "", err
	}
	data, w, h2, err := readSub(src)
	if err != nil {
		return "", err
	}
	if set.Type == frameTypeDark {
		m := calmatch.Choose(calmatch.Group{
			Night: set.Night, Exposure: set.Exposure, Gain: set.Gain, Offset: set.Offset,
			SetTemp: set.SetTemp, BinX: set.BinX, Rotator: set.Rotator,
		}, all).Bias
		if m.Set == nil {
			return "", fmt.Errorf("no bias for dark at gain %v offset %v", set.Gain, set.Offset)
		}
		biasFile, _, err := p.masterFor(ctx, *m.Set, all)
		if err != nil {
			return "", fmt.Errorf("bias for dark: %w", err)
		}
		bias, bw, bh, err := readSub(biasFile)
		if err != nil {
			return "", err
		}
		if bw != w || bh != h2 {
			return "", fmt.Errorf("bias is %dx%d, dark %dx%d", bw, bh, w, h2)
		}
		for i := range data {
			data[i] -= bias[i]
		}
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", err
	}
	tmp := local + ".tmp"
	if err := writeFITSFile(tmp, w, h2, 1, data, nil); err != nil {
		return "", err
	}
	slog.Info("Imported calibration master", "type", set.Type, "key", set.Master, "gain", set.Gain)
	return local, os.Rename(tmp, local)
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
	files := make([]string, len(frames))
	for i, f := range frames {
		// Siril's convert picks up every image in the directory; plain
		// names keep spaces in target names out of the script.
		files[i] = filepath.Join(in, fmt.Sprintf("f%04d%s", i, strings.ToLower(path.Ext(f.Key))))
		if err := p.download(ctx, p.source, f.Key, files[i]); err != nil {
			return err
		}
	}
	if typ == frameTypeDark {
		left, err := p.dropLeakyDarks(ctx, frames, files)
		if err != nil {
			return err
		}
		if left < 3 {
			return fmt.Errorf("%d of %d darks have a light leak, too few left", len(frames)-left, len(frames))
		}
	}

	var sb strings.Builder
	sb.WriteString(p.sirilPreamble(true))
	sb.WriteString("cd in\nconvert f -out=../w\ncd ../w\n")
	// The output is relative to Siril's working directory, w.
	result := filepath.Join(dir, "w", "master")
	switch typ {
	case "FLAT", frameTypeDark:
		b, err := siril.Path(bias)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "calibrate f_ -bias=%s\n", b)
		norm := "-norm=mul"
		if typ == frameTypeDark {
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
	ratio := math.Max(0.05, math.Min(0.9, p.opts.SirilMemoryRatio/float64(p.opts.Workers)))
	return fmt.Sprintf("%s\nsetext fit\nsetcpu %d\nsetmem %.2f\n", depth, max(1, p.opts.SirilThreads), ratio)
}
