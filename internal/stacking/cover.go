package stacking

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"path"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// palette maps filters to red, green and blue; HaRed blends H-a into red.
type palette struct {
	Name    string
	R, G, B string
	HaRed   bool
}

// filters lists each filter the palette reads once.
func (p palette) filters() []string {
	out := []string{p.R}
	for _, f := range []string{p.G, p.B} {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if p.HaRed && !slices.Contains(out, "H-a") {
		out = append(out, "H-a")
	}
	return out
}

// palettes in order of preference for a cover.
var palettes = []palette{
	{Name: "RGB+Ha", R: "Red", G: "Green", B: "Blue", HaRed: true},
	{Name: "RGB", R: "Red", G: "Green", B: "Blue"},
	{Name: "SHO", R: "S-II", G: "H-a", B: "O-III"},
	{Name: "HOO", R: "H-a", G: "O-III", B: "O-III"},
}

// minShare is the least effective exposure a palette channel may have,
// relative to the palette's best channel: a channel with much less data
// only adds noise to the cover.
const minShare = 0.25

// layer is one filter's linear preview and how much data backs it.
type layer struct {
	Key       string
	Effective float64 // seconds, score-weighted
	// Crop is the well-covered part as fractions of the frame; zero W
	// means the whole frame.
	CropX, CropY, CropW, CropH float64
}

// fracCrop turns a crop in pixels of a width×height image into fractions.
func fracCrop(l layer, x, y, w, h, width, height int) layer {
	if w > 0 && h > 0 && width > 0 && height > 0 {
		l.CropX, l.CropY = float64(x)/float64(width), float64(y)/float64(height)
		l.CropW, l.CropH = float64(w)/float64(width), float64(h)/float64(height)
	}
	return l
}

// commonCrop is where every filter's crop overlaps, in pixels of a w×h
// linear preview.
func commonCrop(layers []layer, w, h int) Rect {
	x0, y0, x1, y1 := 0.0, 0.0, 1.0, 1.0
	for _, l := range layers {
		if l.CropW <= 0 {
			continue
		}
		x0, y0 = max(x0, l.CropX), max(y0, l.CropY)
		x1, y1 = min(x1, l.CropX+l.CropW), min(y1, l.CropY+l.CropH)
	}
	r := Rect{X: int(math.Ceil(x0 * float64(w))), Y: int(math.Ceil(y0 * float64(h)))}
	r.W, r.H = int(x1*float64(w))-r.X, int(y1*float64(h))-r.Y
	if r.W <= 0 || r.H <= 0 {
		return Rect{0, 0, w, h}
	}
	return r
}

// haBlend is how much of H-a's excess over red goes into red, in units of
// each channel's noise, as in the browser's palette mixer.
const haBlend = 0.6

// haFloor is how far, in noise, H-a must stand over red before any of it
// goes into red. Taking every excess, noise alone reddened the sky, the more
// the fewer H-a subs: a mosaic's shallow panels came out as red patches.
const haFloor = 2.0

// choosePalette picks the first palette whose filters all have a linear
// preview and comparable data: every channel at least minShare of the
// best one. RGB+Ha needs H-a at minShare of the average of R, G and B.
func choosePalette(have map[string]layer) (palette, bool) {
	for _, p := range palettes {
		channels := []string{p.R, p.G, p.B}
		ok := true
		best, sum := 0.0, 0.0
		for _, f := range channels {
			l, found := have[f]
			if !found || l.Key == "" {
				ok = false
				break
			}
			best = max(best, l.Effective)
			sum += l.Effective
		}
		if !ok {
			continue
		}
		for _, f := range channels {
			if have[f].Effective < minShare*best {
				ok = false
			}
		}
		if p.HaRed {
			ha, found := have["H-a"]
			ok = ok && found && ha.Key != "" && ha.Effective >= minShare*sum/3
		}
		if ok {
			return p, true
		}
	}
	return palette{}, false
}

// renderCover renders a colour preview for subject (a target, or a mosaic
// project) from the linear previews of its filters, keyed by filter, and
// stores it at prefix/color.jpg. Without a full palette it clears the cover,
// and the mono preview is used.
func (p *Pipeline) renderCover(ctx context.Context, subject, prefix string, linear map[string]layer) error {
	pal, ok := choosePalette(linear)
	if !ok {
		metrics.Covers.WithLabelValues("mono").Inc()
		return p.db.WithContext(ctx).Where("subject = ?", subject).Delete(&app.Cover{}).Error
	}
	planes := map[string]*linearImage{}
	for _, f := range pal.filters() {
		img, err := p.readLinear(ctx, linear[f].Key)
		if err != nil {
			return fmt.Errorf("%s %s: %w", subject, f, err)
		}
		planes[f] = img
	}
	// Mosaic canvases of different filters are framed from each filter's
	// own plate solutions, so they can differ by a pixel or two; trim them
	// to the common size. Anything more means they don't line up.
	w, h := planes[pal.R].W, planes[pal.R].H
	for _, img := range planes {
		w, h = min(w, img.W), min(h, img.H)
	}
	for f, img := range planes {
		if float64(img.W-w) > 0.01*float64(img.W) || float64(img.H-h) > 0.01*float64(img.H) {
			slog.Info("Filters differ in size; no colour cover", "subject", subject, "filter", f)
			return p.db.WithContext(ctx).Where("subject = ?", subject).Delete(&app.Cover{}).Error
		}
		if img.W != w || img.H != h {
			planes[f] = &linearImage{W: w, H: h, Data: crop(img.Data, img.W, Rect{0, 0, w, h})}
		}
	}
	var layers []layer
	for _, f := range pal.filters() {
		layers = append(layers, linear[f])
	}
	if r := commonCrop(layers, w, h); r.W != w || r.H != h {
		for f, img := range planes {
			planes[f] = &linearImage{W: r.W, H: r.H, Data: crop(img.Data, img.W, r)}
		}
		w, h = r.W, r.H
	}
	jpg, err := composeCover(pal, planes, w, h)
	if err != nil {
		return err
	}
	key := path.Join(prefix, "color.jpg")
	if err := p.putBytes(ctx, key, jpg, minio.PutObjectOptions{ContentType: "image/jpeg"}); err != nil {
		return err
	}
	var c app.Cover
	err = p.db.WithContext(ctx).Where("subject = ?", subject).First(&c).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	c.Subject, c.Palette, c.PreviewKey, c.UpdatedAt, c.Version = subject, pal.Name, key, time.Now(), CoverVersion
	metrics.Covers.WithLabelValues("colour").Inc()
	return p.db.WithContext(ctx).Save(&c).Error
}

// composeCover stretches each channel on its own and encodes a JPEG.
func composeCover(pal palette, planes map[string]*linearImage, w, h int) ([]byte, error) {
	// Only where every channel has data: a mosaic panel missing a filter,
	// or a master's edge another doesn't reach, would otherwise show in the
	// remaining channels' colours. H-a only adds to red, so it isn't needed.
	rawR, rawG, rawB := commonData(planes[pal.R].Data, planes[pal.G].Data, planes[pal.B].Data)
	red := rawR
	if pal.HaRed {
		red = blendHa(red, planes["H-a"].Data)
	}
	r, g, b := stretchNonZero(red), stretchNonZero(rawG), stretchNonZero(rawB)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range r {
		img.Pix[4*i] = to8(r[i])
		img.Pix[4*i+1] = to8(g[i])
		img.Pix[4*i+2] = to8(b[i])
		img.Pix[4*i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// commonData returns copies of the planes with every pixel that is empty
// (0) in any of them emptied in all.
func commonData(planes ...[]float32) (r, g, b []float32) {
	out := make([][]float32, len(planes))
	for k, p := range planes {
		out[k] = slices.Clone(p)
	}
	for i := range out[0] {
		for _, p := range planes {
			if p[i] == 0 {
				for _, o := range out {
					o[i] = 0
				}
				break
			}
		}
	}
	return out[0], out[1], out[2]
}

func to8(v float32) uint8 {
	return uint8(math.Round(float64(max(0, min(1, v))) * 255))
}

type linearImage struct {
	W, H int
	Data []float32
}

// readLinear downloads and decodes a linear preview (LinearMagic format).
func (p *Pipeline) readLinear(ctx context.Context, key string) (*linearImage, error) {
	obj, err := p.s3.GetObject(ctx, p.dest, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	raw, err := io.ReadAll(obj)
	if err != nil {
		return nil, err
	}
	// Stored gzip-encoded for browsers; MinIO returns it as stored.
	if zr, zerr := gzip.NewReader(bytes.NewReader(raw)); zerr == nil {
		if raw, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	return decodeLinear(raw)
}

func decodeLinear(b []byte) (*linearImage, error) {
	if len(b) < 12 || string(b[:4]) != LinearMagic {
		return nil, fmt.Errorf("not a linear preview")
	}
	w := int(binary.LittleEndian.Uint32(b[4:8]))
	h := int(binary.LittleEndian.Uint32(b[8:12]))
	if len(b) < 12+4*w*h {
		return nil, fmt.Errorf("linear preview is truncated")
	}
	data := make([]float32, w*h)
	if err := binary.Read(bytes.NewReader(b[12:12+4*w*h]), binary.LittleEndian, data); err != nil {
		return nil, err
	}
	return &linearImage{W: w, H: h, Data: data}, nil
}

// statsNonZero is the median and MAD-derived σ of the covered pixels.
func statsNonZero(p []float32) (med, sigma float64) {
	stride := max(1, len(p)/200_000)
	s := make([]float64, 0, len(p)/stride+1)
	for i := 0; i < len(p); i += stride {
		if v := p[i]; v != 0 && !math.IsNaN(float64(v)) {
			s = append(s, float64(v))
		}
	}
	if len(s) == 0 {
		return 0, 0
	}
	slices.Sort(s)
	med = s[len(s)/2]
	for i := range s {
		s[i] = math.Abs(s[i] - med)
	}
	slices.Sort(s)
	return med, s[len(s)/2] * madToSigma
}

// stretchNonZero applies the STF auto-stretch from the covered pixels'
// statistics, leaving uncovered pixels black.
func stretchNonZero(p []float32) []float32 {
	med, sigma := statsNonZero(p)
	var hi float64
	for i := 0; i < len(p); i += 7 {
		hi = max(hi, float64(p[i]))
	}
	if hi <= 0 {
		hi = 1
	}
	c0 := math.Max(0, math.Min(hi, med+preview.ShadowsClip*sigma))
	span := hi - c0
	if span <= 0 {
		span = 1
	}
	m := preview.MTF(preview.TargetBackground, (med-c0)/span)
	out := make([]float32, len(p))
	for i, v := range p {
		if v == 0 {
			continue
		}
		out[i] = float32(preview.MTF(m, math.Max(0, math.Min(1, (float64(v)-c0)/span))))
	}
	return out
}

// blendHa adds H-a signal brighter than red into red, comparing both on a
// common scale (median 0, σ 1), and returns it on red's scale.
func blendHa(red, ha []float32) []float32 {
	rm, rs := statsNonZero(red)
	hm, hs := statsNonZero(ha)
	if rs == 0 || hs == 0 {
		return red
	}
	out := make([]float32, len(red))
	for i, v := range red {
		if v == 0 {
			continue
		}
		r := (float64(v) - rm) / rs
		h := (float64(ha[i]) - hm) / hs
		out[i] = float32((r+haBlend*math.Max(0, h-r-haFloor))*rs + rm)
	}
	return out
}

// refreshCover re-renders a target's colour cover from its masters.
func (p *Pipeline) refreshCover(ctx context.Context, object string) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("object = ? AND linear_key IS NOT NULL", object).Find(&stacks).Error; err != nil {
		slog.Warn("Could not load masters for the cover", "object", object, "error", err)
		return
	}
	// A comet is shown as its comet masters see it, sharp, but only once
	// every filter has one: comet-aligned and star-aligned channels put the
	// comet and the stars in different places.
	comet := len(stacks) > 0
	for _, s := range stacks {
		comet = comet && s.CometLinearKey != nil
	}
	linear := map[string]layer{}
	for _, s := range stacks {
		linear[s.Filter] = fracCrop(layer{Key: *s.LinearKey, Effective: s.EffectiveSeconds},
			s.CropX, s.CropY, s.CropW, s.CropH, s.Width, s.Height)
		if comet {
			linear[s.Filter] = fracCrop(layer{Key: *s.CometLinearKey, Effective: s.EffectiveSeconds},
				s.CometCropX, s.CometCropY, s.CometCropW, s.CometCropH, s.Width, s.Height)
		}
	}
	if err := p.renderCover(ctx, object, path.Join("stacks", object), linear); err != nil {
		slog.Warn("Could not render the cover", "object", object, "error", err)
	}
}

// refreshMosaicCover re-renders a project's colour cover from its mosaics.
func (p *Pipeline) refreshMosaicCover(ctx context.Context, project string) {
	var mosaics []app.Mosaic
	if err := p.db.WithContext(ctx).Where("project = ? AND linear_key IS NOT NULL", project).Find(&mosaics).Error; err != nil {
		slog.Warn("Could not load mosaics for the cover", "project", project, "error", err)
		return
	}
	linear := map[string]layer{}
	for _, m := range mosaics {
		linear[m.Filter] = fracCrop(layer{Key: *m.LinearKey, Effective: m.EffectiveSeconds},
			m.CropX, m.CropY, m.CropW, m.CropH, m.Width, m.Height)
	}
	if err := p.renderCover(ctx, app.MosaicSubject(project), path.Join("mosaics", project), linear); err != nil {
		slog.Warn("Could not render the mosaic cover", "project", project, "error", err)
	}
}

// CoverVersion is how covers are composed; older ones are rendered again at
// startup. 1: pixels missing any channel are black. 2: H-a goes into red
// only above the noise (haFloor).
const CoverVersion = 2

// backfillCovers renders covers for targets and projects whose masters are
// newer than their cover, such as those stacked before covers existed, or
// whose cover was composed by an older CoverVersion.
func (p *Pipeline) backfillCovers(ctx context.Context) {
	covers := map[string]time.Time{}
	var cs []app.Cover
	if err := p.db.WithContext(ctx).Find(&cs).Error; err != nil {
		slog.Warn("Could not load covers", "error", err)
		return
	}
	for _, c := range cs {
		if c.Version >= CoverVersion {
			covers[c.Subject] = c.UpdatedAt
		}
	}
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("linear_key IS NOT NULL").Find(&stacks).Error; err != nil {
		slog.Warn("Could not load masters", "error", err)
		return
	}
	done := map[string]bool{}
	for _, s := range stacks {
		if done[s.Object] || s.UpdatedAt.Before(covers[s.Object]) || ctx.Err() != nil {
			continue
		}
		done[s.Object] = true
		p.refreshCover(ctx, s.Object)
	}
	var mosaics []app.Mosaic
	if err := p.db.WithContext(ctx).Where("linear_key IS NOT NULL").Find(&mosaics).Error; err != nil {
		return
	}
	for _, m := range mosaics {
		if done[app.MosaicSubject(m.Project)] || m.UpdatedAt.Before(covers[app.MosaicSubject(m.Project)]) || ctx.Err() != nil {
			continue
		}
		done[app.MosaicSubject(m.Project)] = true
		p.refreshMosaicCover(ctx, m.Project)
	}
	if len(done) > 0 {
		slog.Info("Rendered covers", "subjects", len(done))
	}
}
