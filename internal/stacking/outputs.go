package stacking

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// LinearMaxWidth is the width of the linear preview the browser mixes
// palettes from. Every filter of a target shares the reference geometry, so
// their linear previews line up pixel for pixel.
const LinearMaxWidth = 1600

// LinearMagic starts a linear preview: "APLP", then little-endian uint32
// width and height, then width×height little-endian float32 samples.
const LinearMagic = "APLP"

func stackPrefix(s *app.Stack) string {
	return path.Join("stacks", s.Object, s.Filter)
}

// publish writes the master, its previews and the accumulator state, then
// updates the stack row.
func (p *Pipeline) publish(ctx context.Context, stack *app.Stack, acc *Accumulator) error {
	var totals struct {
		Exposure  float64
		Effective float64
		Longest   float64
	}
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Select("COALESCE(SUM(exposure),0) as exposure, COALESCE(SUM(score*exposure),0) as effective, COALESCE(MAX(exposure),0) as longest").
		Where("stack_id = ? AND status = ?", stack.ID, app.StackStatusAdded).Scan(&totals).Error; err != nil {
		return err
	}
	stack.Width, stack.Height = acc.W, acc.H
	stack.Subs = acc.Subs
	stack.WeightSum, stack.BackgroundSum = acc.WeightSum, acc.BackgroundSum
	stack.ExposureSeconds, stack.EffectiveSeconds = totals.Exposure, totals.Effective
	stack.ScaleExposure = totals.Longest

	dir, err := os.MkdirTemp(p.workDir, "publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	prefix := stackPrefix(stack)
	master := acc.Master(stack.ScaleExposure)

	cards := []imagedata.Card{
		imagedata.StringCard("OBJECT", stack.Object, "target"),
		imagedata.StringCard("FILTER", stack.Filter, "filter"),
		imagedata.StringCard("IMAGETYP", "Master Light", ""),
		imagedata.IntCard("NCOMBINE", acc.Subs, "subs stacked"),
		imagedata.FloatCard("EXPTIME", stack.ScaleExposure, "[s] scaled to one sub of this length"),
		imagedata.FloatCard("TOTALEXP", stack.ExposureSeconds, "[s] total exposure stacked"),
		imagedata.FloatCard("EFFEXP", stack.EffectiveSeconds, "[s] exposure weighted by sub score"),
		imagedata.FloatCard("MINSCORE", p.opts.MinScore, "lowest sub score stacked"),
		imagedata.StringCard("DATE", time.Now().UTC().Format("2006-01-02T15:04:05"), "file written"),
		imagedata.StringCard("SWCREATE", "astro-stacker (Siril 1.4 calibration)", ""),
	}
	masterFile := filepath.Join(dir, "master.fit")
	if err := writeFITSFile(masterFile, acc.W, acc.H, 1, master, cards); err != nil {
		return err
	}
	// Plate solving needs the master on disk, so it's written again with
	// the full header.
	header := p.masterHeader(ctx, stack, cards, dir, masterFile)
	if err := writeFITSFile(masterFile, acc.W, acc.H, 1, master, header); err != nil {
		return err
	}
	xisfFile := filepath.Join(dir, "master.xisf")
	if err := writeXISFFile(xisfFile, acc.W, acc.H, master, header); err != nil {
		return err
	}
	stack.MasterVersion = MasterVersion
	stateFile := filepath.Join(dir, "state.fit")
	if err := writeFITSFile(stateFile, acc.W, acc.H, 4, acc.Planes(), nil); err != nil {
		return err
	}

	im := &imagedata.Image{W: acc.W, H: acc.H, C: 1, Data: master}
	r := coverageCrop(acc)
	stack.CropX, stack.CropY, stack.CropW, stack.CropH = r.X, r.Y, r.W, r.H
	stack.CropVersion = CropVersion
	cropped := &imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(master, acc.W, r)}
	jpg, err := preview.Render(cropped, preview.DefaultOptions())
	if err != nil {
		return err
	}
	linear, err := linearPreview(im)
	if err != nil {
		return err
	}

	keys := map[string]*string{}
	for name, up := range map[string]func(key string) error{
		"master.fit":  func(k string) error { return p.upload(ctx, masterFile, k, "application/fits") },
		"master.xisf": func(k string) error { return p.upload(ctx, xisfFile, k, contentTypeOctetStream) },
		"state.fit":   func(k string) error { return p.upload(ctx, stateFile, k, "application/fits") },
		"preview.jpg": func(k string) error {
			return p.putBytes(ctx, k, jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG})
		},
		"linear.bin": func(k string) error {
			return p.putBytes(ctx, k, linear, minio.PutObjectOptions{ContentType: contentTypeOctetStream, ContentEncoding: contentEncodingGzip})
		},
	} {
		k := path.Join(prefix, name)
		if err := up(k); err != nil {
			return err
		}
		keys[name] = &k
	}
	stack.MasterKey, stack.StateKey, stack.XISFKey = keys["master.fit"], keys["state.fit"], keys["master.xisf"]
	stack.PreviewKey, stack.LinearKey = keys["preview.jpg"], keys["linear.bin"]
	stack.UpdatedAt = time.Now()
	if err := p.db.WithContext(ctx).Save(stack).Error; err != nil {
		return err
	}
	metrics.MastersUpdated.Inc()
	p.refreshCover(ctx, stack.Object)
	p.Events.Publish(events.Event{Type: events.TypeMaster, Object: stack.Object, Filter: stack.Filter})
	return nil
}

func writeFITSFile(name string, w, h, c int, data []float32, cards []imagedata.Card) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if err := imagedata.WriteFITS(f, w, h, c, data, cards); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (p *Pipeline) putBytes(ctx context.Context, key string, b []byte, opts minio.PutObjectOptions) error {
	if _, err := p.s3.PutObject(ctx, p.dest, key, bytes.NewReader(b), int64(len(b)), opts); err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

// linearPreview bins the master to LinearMaxWidth and gzips it in the
// LinearMagic format.
func linearPreview(im *imagedata.Image) ([]byte, error) {
	factor := max(1, int(math.Ceil(float64(im.W)/LinearMaxWidth)))
	b := preview.Bin(im, factor)
	if b.W < 0 || b.W > math.MaxUint32 || b.H < 0 || b.H > math.MaxUint32 {
		return nil, fmt.Errorf("linear preview is %dx%d", b.W, b.H)
	}
	var raw bytes.Buffer
	raw.WriteString(LinearMagic)
	_ = binary.Write(&raw, binary.LittleEndian, uint32(b.W))
	_ = binary.Write(&raw, binary.LittleEndian, uint32(b.H))
	_ = binary.Write(&raw, binary.LittleEndian, b.Plane(0))
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// loadState restores a stack's accumulator from its stored planes.
func (p *Pipeline) loadState(ctx context.Context, stack *app.Stack) (*Accumulator, error) {
	if stack.StateKey == nil {
		return nil, fmt.Errorf("no saved state")
	}
	dir, err := os.MkdirTemp(p.workDir, "state-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "state.fit")
	if err := p.download(ctx, p.dest, *stack.StateKey, local); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return nil, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, err
	}
	if im.C != 4 {
		return nil, fmt.Errorf("state has %d planes, want 4", im.C)
	}
	return FromPlanes(im.W, im.H, im.Data, stack.BackgroundSum, stack.WeightSum, stack.Subs)
}
