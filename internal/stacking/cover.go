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
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/ninapath"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

const (
	paletteRGBHa = "RGB+Ha"
	paletteRGB   = "RGB"
	paletteSHO   = "SHO"
	paletteHOO   = "HOO"
)

// palette maps filters to red, green and blue; HaRed adds H-a to red, and
// OIIIGB O-III to green and blue, where their masks are on (addLine). Lum
// offers a luminance master for the lightness of a red, green and blue
// palette; composeCover uses it only where it measures better, and the
// name then gets an L (LRGB, LRGB+Ha).
type palette struct {
	Name    string
	R, G, B string
	HaRed   bool
	OIIIGB  bool
	Lum     bool
}

// filters lists each filter the palette reads once.
func (p palette) filters() []string {
	out := []string{p.R}
	for _, f := range []string{p.G, p.B} {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if p.HaRed && !slices.Contains(out, filterHa) {
		out = append(out, filterHa)
	}
	if p.OIIIGB && !slices.Contains(out, filterOIII) {
		out = append(out, filterOIII)
	}
	if p.Lum {
		out = append(out, filterLuminance)
	}
	return out
}

// lrgbPrefix starts the name of a palette whose lightness took a
// luminance master.
const lrgbPrefix = "L"

// PaletteFilters lists the filters a cover of the named palette is composed
// from, or nil for a name that isn't a palette.
func PaletteFilters(name string) []string {
	base, oiii := strings.CutSuffix(name, "+OIII")
	lum := strings.HasPrefix(base, lrgbPrefix+paletteRGB)
	if lum {
		base = strings.TrimPrefix(base, lrgbPrefix)
	}
	for _, p := range palettes() {
		if p.Name == base {
			p.OIIIGB, p.Lum = oiii, lum && p.R == filterRed
			return p.filters()
		}
	}
	return nil
}

// shoSubject matches a target or project shot for the SHO palette.
var shoSubject = regexp.MustCompile(`\bSHO\b`)

// palettes a cover can use; choosePalette orders them.
func palettes() []palette {
	return []palette{
		{Name: paletteRGBHa, R: filterRed, G: filterGreen, B: filterBlue, HaRed: true},
		{Name: paletteRGB, R: filterRed, G: filterGreen, B: filterBlue},
		{Name: paletteSHO, R: filterSII, G: filterHa, B: filterOIII},
		{Name: paletteHOO, R: filterHa, G: filterOIII, B: filterOIII},
	}
}

// minShare is the least effective exposure a palette channel may have,
// relative to the palette's best channel: a channel with much less data
// only adds noise to the cover.
const minShare = 0.25

// layer is one filter's linear preview and how much data backs it.
type layer struct {
	Key       string
	Effective float64 // seconds, score-weighted
	// Panels is how many of a mosaic's panels the layer has; 0 for a
	// target's master.
	Panels int
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

// haBlue bounds how much H-a goes into blue, relative to red, for a hint
// of pink: the H-a filter's line (H-a with H-b alongside) showed in the
// Cygnus Loop's broadband as 1 red to 0.36 blue, but at that the covers
// came out magenta, and the manual North America version added none;
// 0.15 still turned IC 434 and M42 magenta.
const haBlue = 0.0

// oiiiBlue bounds how much O-III goes into blue, relative to green; the
// Cygnus Loop's broadband saw it about equally in both.
const oiiiBlue = 1.2

// choosePalette picks the first palette whose filters all have a linear
// preview and comparable data: every channel at least minShare of the
// best one. RGB+Ha needs H-a at minShare of the average of R, G and B.
// HOO comes first when H-a, O-III and S-II together hold more effective
// exposure than red, green and blue: a target shot mostly in narrowband
// (Dolphin Head, the Cygnus Loop) shows its nebula rather than a little H-a
// in a star field. SHO is the last resort, but for a subject shot for it
// (sho: "SHO" in its name, as Heart and Soul Nebula SHO). A broadband
// palette also takes O-III, into green and blue, when there is as much of
// it as RGB+Ha asks of H-a. For a mosaic, a palette's red, green and blue
// must each have as many panels as the filter with the most, so a cover
// shows the whole field when any filter does; without such a palette the
// mono cover, from the filter with the most panels, is used.
func choosePalette(have map[string]layer, sho bool) (palette, bool) {
	most := 0
	for _, l := range have {
		most = max(most, l.Panels)
	}
	var narrow, broad float64
	for _, f := range []string{filterHa, filterOIII, filterSII} {
		narrow += have[f].Effective
	}
	for _, f := range []string{filterRed, filterGreen, filterBlue} {
		broad += have[f].Effective
	}
	byName := map[string]palette{}
	for _, p := range palettes() {
		byName[p.Name] = p
	}
	names := []string{paletteRGBHa, paletteRGB, paletteHOO, paletteSHO}
	switch {
	case sho:
		names = []string{paletteSHO, paletteHOO, paletteRGBHa, paletteRGB}
	case narrow > broad:
		names = []string{paletteHOO, paletteRGBHa, paletteRGB, paletteSHO}
	}
	order := make([]palette, 0, len(names))
	for _, n := range names {
		order = append(order, byName[n])
	}
	for _, p := range order {
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
			ok = ok && l.Panels >= most
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
			ha, found := have[filterHa]
			ok = ok && found && ha.Key != "" && ha.Effective >= minShare*sum/3
		}
		if ok {
			if p.R == filterRed && p.G == filterGreen && p.B == filterBlue {
				if o, found := have[filterOIII]; found && o.Key != "" && o.Effective >= minShare*sum/3 {
					p.OIIIGB = true
					p.Name += "+OIII"
				}
				l, found := have[filterLuminance]
				p.Lum = found && l.Key != "" && l.Panels >= most
			}
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
	pal, ok := choosePalette(linear, shoSubject.MatchString(subject))
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
	pal, planes, w, h, ok := framePlanes(subject, pal, linear, planes)
	if !ok {
		return p.db.WithContext(ctx).Where("subject = ?", subject).Delete(&app.Cover{}).Error
	}
	jpg, lrgb, err := composeCover(pal, planes, w, h)
	if err != nil {
		return err
	}
	if pal.Lum {
		slog.Info("Cover luminance", "subject", subject, "used", lrgb.Used, "why", lrgb.Why, "weight", lrgb.Weight,
			"noise_rgb", lrgb.NoiseRGB, "noise_l", lrgb.NoiseL, "noise_out", lrgb.NoiseOut, "shift", lrgb.Shift, "stars", lrgb.Stars)
	}
	if lrgb.Used {
		pal.Name = lrgbPrefix + pal.Name
	}
	key := path.Join(prefix, "color.jpg")
	if err := p.putBytes(ctx, key, jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG}); err != nil {
		return err
	}
	var c app.Cover
	err = p.db.WithContext(ctx).Where("subject = ?", subject).First(&c).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	moved := c.PreviewKey
	c.Subject, c.Palette, c.PreviewKey, c.UpdatedAt, c.Version = subject, pal.Name, key, time.Now(), CoverVersion
	metrics.Covers.WithLabelValues("colour").Inc()
	if err := p.db.WithContext(ctx).Save(&c).Error; err != nil {
		return err
	}
	p.removeMoved(ctx, &moved, prefix, "color.jpg")
	return nil
}

// framePlanes brings a cover's linear previews to one frame: trimmed to
// the colour channels' common size and their common well-covered crop. A
// luminance that doesn't fit the frame is left out, and one is used only
// inside its own well-covered crop. ok is false when the colour channels
// themselves don't line up.
func framePlanes(subject string, pal palette, linear map[string]layer, planes map[string]*linearImage) (palette, map[string]*linearImage, int, int, bool) {
	// Mosaic canvases of different filters are framed from each filter's
	// own plate solutions, so they can differ by a pixel or two; trim them
	// to the common size. Anything more means they don't line up.
	w, h := planes[pal.R].W, planes[pal.R].H
	for f, img := range planes {
		if f != filterLuminance {
			w, h = min(w, img.W), min(h, img.H)
		}
	}
	if l, ok := planes[filterLuminance]; ok && (l.W < w || l.H < h || float64(l.W-w) > 0.01*float64(l.W) || float64(l.H-h) > 0.01*float64(l.H)) {
		slog.Info("Luminance differs in size from the colour; cover without it", "subject", subject)
		delete(planes, filterLuminance)
		pal.Lum = false
	}
	for f, img := range planes {
		if float64(img.W-w) > 0.01*float64(img.W) || float64(img.H-h) > 0.01*float64(img.H) {
			slog.Info("Filters differ in size; no colour cover", "subject", subject, "filter", f)
			return pal, nil, 0, 0, false
		}
		if img.W != w || img.H != h {
			planes[f] = &linearImage{W: w, H: h, Data: crop(img.Data, img.W, Rect{0, 0, w, h})}
		}
	}
	if l, ok := planes[filterLuminance]; ok {
		// Only where the luminance is well covered; elsewhere the
		// colour's own lightness.
		keep := commonCrop([]layer{linear[filterLuminance]}, w, h)
		for y := range h {
			for x := range w {
				if x < keep.X || x >= keep.X+keep.W || y < keep.Y || y >= keep.Y+keep.H {
					l.Data[y*w+x] = 0
				}
			}
		}
	}
	var layers []layer
	for _, f := range pal.filters() {
		if f != filterLuminance {
			layers = append(layers, linear[f])
		}
	}
	if r := commonCrop(layers, w, h); r.W != w || r.H != h {
		for f, img := range planes {
			planes[f] = &linearImage{W: r.W, H: r.H, Data: crop(img.Data, img.W, r)}
		}
		w, h = r.W, r.H
	}
	return pal, planes, w, h, true
}

// composeCover adds the narrowband lines, balances and stretches the
// channels together (colourStretch), with the luminance in the lightness
// when the palette offers one, and encodes a JPEG.
func composeCover(pal palette, planes map[string]*linearImage, w, h int) ([]byte, lrgbReport, error) {
	// Only where every channel has data: a mosaic panel missing a filter,
	// or a master's edge another doesn't reach, would otherwise show in the
	// remaining channels' colours. H-a only adds to red, so it isn't needed.
	rawR, rawG, rawB := commonData(planes[pal.R].Data, planes[pal.G].Data, planes[pal.B].Data)
	red, green, blue := rawR, rawG, rawB
	if pal.HaRed {
		// H-a into red, and a little into blue, as the camera sees the
		// line with H-b alongside: pink rather than pure red.
		out := addLine(lineInputs{Name: filterHa, Line: planes[filterHa].Data, Cont: rawR, Proxy: rawG, Gate: rawB,
			Into: []lineTarget{{Data: red}, {Data: blue, MaxRel: haBlue}}}, w, h)
		red, blue = out[0], out[1]
	}
	if pal.OIIIGB {
		out := addLine(lineInputs{Name: filterOIII, Line: planes[filterOIII].Data, Cont: rawG, Proxy: rawR,
			Into: []lineTarget{{Data: green}, {Data: blue, MaxRel: oiiiBlue}}}, w, h)
		green, blue = out[0], out[1]
	}
	// Balanced and stretched as without the lines, so where the mask is
	// off the colour is exactly the broadband's.
	var lum []float32
	if l, ok := planes[filterLuminance]; ok && pal.Lum {
		lum = l.Data
	}
	rgb, lrgb := colourStretch([3][]float32{red, green, blue}, [3][]float32{rawR, rawG, rawB}, lum, w, h)
	r, g, b := rgb[0], rgb[1], rgb[2]
	fillUncovered([][]float32{r, g, b}, rawR, w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range r {
		img.Pix[4*i] = to8(r[i])
		img.Pix[4*i+1] = to8(g[i])
		img.Pix[4*i+2] = to8(b[i])
		img.Pix[4*i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, lrgb, err
	}
	return buf.Bytes(), lrgb, nil
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
	return linearOf(raw)
}

// linearOf decodes a linear preview as stored: gzip-encoded for browsers,
// which MinIO returns as stored.
func linearOf(raw []byte) (*linearImage, error) {
	if zr, zerr := gzip.NewReader(bytes.NewReader(raw)); zerr == nil {
		var err error
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
func stretchNonZero(p []float32) []float32 { return stretchAs(p, p) }

// stretchAs stretches p with the STF auto-stretch computed from ref's
// covered pixels, leaving uncovered pixels black.
func stretchAs(p, ref []float32) []float32 {
	med, sigma := statsNonZero(ref)
	var hi float64
	for i := 0; i < len(ref); i += 7 {
		hi = max(hi, float64(ref[i]))
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

// refreshCover re-renders a target's colour cover from its masters.
func (p *Pipeline) refreshCover(ctx context.Context, object string) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("object = ? AND linear_key IS NOT NULL", object).Find(&stacks).Error; err != nil {
		slog.Warn("Could not load masters for the cover", "object", object, "error", err)
		return
	}
	comet, wait := cometCover(stacks, p.storedDesignations(ctx))
	if wait {
		var n int64
		if err := p.db.WithContext(ctx).Model(&app.Cover{}).Where("subject = ?", object).Count(&n).Error; err != nil {
			slog.Warn("Could not look for the cover", "object", object, "error", err)
			return
		}
		if n > 0 {
			return
		}
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
	if err := p.renderCover(ctx, object, path.Join("stacks", ninapath.Folder(object)), linear); err != nil {
		slog.Warn("Could not render the cover", "object", object, "error", err)
	}
}

// CometCover reports whether a target's cover shows its comet masters,
// each comet stacked on itself; stacks are its masters with a linear
// preview.
func CometCover(stacks []app.Stack) bool {
	comet, _ := cometCover(stacks, nil)
	return comet
}

// cometCover decides how a target's cover shows a comet. It is shown as its
// comet masters see it, sharp, but only once every filter has one made by
// the current cometMethod: comet-aligned and star-aligned channels put the
// comet and the stars in different places, and masters made by different
// methods differ in colour (with the stars taken out of some channels
// only, they came out yellow). While comet masters are still to be made
// for the current masters, wait is set and the cover as it is kept, rather
// than shown half and half; with nothing on its way, the star-aligned
// masters make it.
func cometCover(stacks []app.Stack, stored map[string]bodies.Target) (comet, wait bool) {
	some, current := false, len(stacks) > 0
	for i := range stacks {
		s := &stacks[i]
		some = some || s.CometLinearKey != nil
		current = current && s.CometLinearKey != nil && s.CometMethod != nil && *s.CometMethod == cometMethod
	}
	if current || !some {
		return current, false
	}
	for i := range stacks {
		s := &stacks[i]
		// cometsOnce makes one for a master of three subs or more, once
		// its signature has changed.
		if s.Subs >= 3 && s.MasterKey != nil && s.CometSignature != cometSignature(s, stored[s.Object]) {
			return false, true
		}
	}
	return false, false
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
		linear[m.Filter] = fracCrop(layer{Key: *m.LinearKey, Effective: m.EffectiveSeconds, Panels: m.Panels},
			m.CropX, m.CropY, m.CropW, m.CropH, m.Width, m.Height)
	}
	if err := p.renderCover(ctx, app.MosaicSubject(project), path.Join("mosaics", ninapath.Folder(project)), linear); err != nil {
		slog.Warn("Could not render the mosaic cover", "project", project, "error", err)
	}
}

// CoverVersion is how covers are composed; older ones are rendered again at
// startup. 1: pixels missing any channel are black. 2: H-a goes into red
// only above the noise (haFloor). 3: narrowband palettes first when
// narrowband holds more exposure. 4: SHO only for subjects named for it,
// or when nothing else fits. 5: H-a (and O-III) added only where a mask
// from the continuum-subtracted line is on (addLine), and the channels
// balanced on the sky and the stars and stretched together (colourStretch).
// 6: black point no higher than the darkest sky, over-bright colour taken
// partway to white, and less H-a (lineGain 1.5, none into blue). 7: what
// no channel covers is sky grey, not black, and a mosaic's palette needs
// as many panels as its best filter. 8: a luminance master goes into the
// lightness of a red, green and blue cover where it lines up with them and
// lowers the noise (LRGB).
const CoverVersion = 8

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
		if done[s.Object] || s.UpdatedAt.Before(covers[s.Object]) || p.stopping(ctx) {
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
		if done[app.MosaicSubject(m.Project)] || m.UpdatedAt.Before(covers[app.MosaicSubject(m.Project)]) || p.stopping(ctx) {
			continue
		}
		done[app.MosaicSubject(m.Project)] = true
		p.refreshMosaicCover(ctx, m.Project)
	}
	if len(done) > 0 {
		slog.Info("Rendered covers", "subjects", len(done))
	}
}
