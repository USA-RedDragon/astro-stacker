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
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

const (
	cometNightsMethod = 1
	colCometError     = "comet_error"
	colCometSignature = "comet_signature"
	colSolveAttempts  = "solve_attempts"
)

type nightImage struct {
	data       []float32
	w, h       int
	sky        wcs
	raC, decC  float64
	weight, bg float64
}

type combiner struct {
	out        wcs
	w, h       int
	raC0       float64
	decC0      float64
	sum, sw    []float32
	cover      []uint8
	wsum, bsum float64
}

func newCombiner(out wcs, w, h int, raC0, decC0 float64) *combiner {
	return &combiner{out: out, w: w, h: h, raC0: raC0, decC0: decC0, sum: make([]float32, w*h), sw: make([]float32, w*h), cover: make([]uint8, w*h)}
}

const combineGrid = 32

func (c *combiner) mapping(n nightImage, x, y float64) (float64, float64, bool) {
	ra, dec := c.out.toSky(x+1, float64(c.h)-y)
	xi, eta, ok := project(c.raC0, c.decC0, ra, dec)
	if !ok {
		return 0, 0, false
	}
	r, d := deproject(n.raC, n.decC, xi, eta)
	px, py, ok := n.sky.toPixel(r, d)
	if !ok {
		return 0, 0, false
	}
	return px - 1, float64(n.h) - py, true
}

func (c *combiner) add(n nightImage) {
	if n.weight <= 0 {
		return
	}
	c.wsum += n.weight
	c.bsum += n.weight * n.bg
	gw, gh := c.w/combineGrid+2, c.h/combineGrid+2
	gx := make([]float64, gw*gh)
	gy := make([]float64, gw*gh)
	gok := make([]bool, gw*gh)
	for j := range gh {
		for i := range gw {
			gx[j*gw+i], gy[j*gw+i], gok[j*gw+i] = c.mapping(n, float64(i*combineGrid), float64(j*combineGrid))
		}
	}
	wt := float32(n.weight)
	bg := float32(n.bg)
	rows := make(chan int, c.h)
	for y := range c.h {
		rows <- y
	}
	close(rows)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for y := range rows {
				j := y / combineGrid
				fy := float64(y%combineGrid) / combineGrid
				for x := range c.w {
					i := x / combineGrid
					fx := float64(x%combineGrid) / combineGrid
					a, b, d, e := j*gw+i, j*gw+i+1, (j+1)*gw+i, (j+1)*gw+i+1
					var sx, sy float64
					if gok[a] && gok[b] && gok[d] && gok[e] {
						sx = (gx[a]*(1-fx)+gx[b]*fx)*(1-fy) + (gx[d]*(1-fx)+gx[e]*fx)*fy
						sy = (gy[a]*(1-fx)+gy[b]*fx)*(1-fy) + (gy[d]*(1-fx)+gy[e]*fx)*fy
					} else {
						var ok bool
						if sx, sy, ok = c.mapping(n, float64(x), float64(y)); !ok {
							continue
						}
					}
					v, ok := bilinear(n.data, n.w, n.h, sx, sy)
					if !ok {
						continue
					}
					k := y*c.w + x
					c.sum[k] += wt * (v - bg)
					c.sw[k] += wt
					c.cover[k]++
				}
			}
		})
	}
	wg.Wait()
}

func (c *combiner) result() ([]float32, []uint8) {
	out := make([]float32, c.w*c.h)
	if c.wsum <= 0 {
		return out, c.cover
	}
	level := float32(c.bsum / c.wsum)
	for k, sw := range c.sw {
		if sw > 0 {
			out[k] = c.sum[k]/sw + level
		}
	}
	return out, c.cover
}

func combineNights(out wcs, w, h int, raC0, decC0 float64, nights []nightImage) ([]float32, []uint8) {
	c := newCombiner(out, w, h, raC0, decC0)
	for _, n := range nights {
		c.add(n)
	}
	return c.result()
}

func nonzeroMedian(d []float32) float64 {
	stride := max(1, len(d)/200_000)
	var s []float64
	for i := 0; i < len(d); i += stride {
		if d[i] != 0 {
			s = append(s, float64(d[i]))
		}
	}
	if len(s) == 0 {
		return 0
	}
	return median(s)
}

func coverRect(cover []uint8, w, h int, need uint8) Rect {
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := range h {
		for x := range w {
			if cover[y*w+x] >= need {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return Rect{W: w, H: h}
	}
	return Rect{X: minX, Y: minY, W: maxX - minX + 1, H: maxY - minY + 1}
}

func combinedSignature(rows []app.BodyNight, field time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "nights\x00%d\x00%d\x00%s", cometNightsMethod, cometMethod, field.UTC().Format(goals.NightLayout))
	for _, r := range rows {
		fmt.Fprintf(h, "\x00%s\x00%s", r.Night.UTC().Format(goals.NightLayout), r.ImageSignature)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func combineCandidate(nights map[string][]nightSub) bool {
	n := 0
	for _, rows := range nights {
		if len(rows) >= bodyNightMinSubs {
			n++
		}
	}
	return n >= 2
}

func imagedNights(bs bodyStack) ([]app.BodyNight, bool) {
	if !combineCandidate(bs.nights) {
		return nil, false
	}
	var out []app.BodyNight
	for k, subs := range bs.nights {
		if len(subs) < bodyNightMinSubs {
			continue
		}
		r, ok := bs.have[k]
		sig := bodyNightSignature(subs, bs.body)
		image := bodyImageSignature(sig)
		switch {
		case !ok:
			return nil, false
		case r.ImageError != nil && r.ImageSignature == image:
			continue
		case retryDue(r.Error, time.Time{}) && r.Signature == sig:
			continue
		case r.ImageSignature != image || r.Signature != sig || r.StarlessKey == nil || r.RefTime == nil:
			return nil, false
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b app.BodyNight) int { return a.Night.Compare(b.Night) })
	return out, len(out) > 0
}

func (p *Pipeline) combinesNights(ctx context.Context, s *app.Stack) (bool, error) {
	nights, err := p.nightSubs(ctx, s)
	if err != nil {
		return false, err
	}
	return combineCandidate(nights), nil
}

func (p *Pipeline) combineNightsIfDue(ctx context.Context, bs bodyStack) (bool, error) {
	rows, ok := imagedNights(bs)
	if !ok {
		return false, nil
	}
	chosen := p.fieldNight(ctx, bs.stack.Object, rows)
	sig := combinedSignature(rows, chosen.Night)
	if bs.stack.CometSignature == sig {
		return false, nil
	}
	if !p.hold(bs.stack.Object) {
		return false, nil
	}
	defer p.release(bs.stack.Object)
	defer p.finished(bs.stack.Object)
	var fresh app.Stack
	if err := p.db.WithContext(ctx).First(&fresh, bs.stack.ID).Error; err != nil || fresh.CometSignature == sig {
		return false, nil
	}
	bs.stack = &fresh
	err := p.combineNights(ctx, bs, rows, chosen, sig)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		slog.Error("Combining a moving body's nights failed", "object", bs.stack.Object, "filter", bs.stack.Filter, "error", err)
		msg := err.Error()
		return true, p.db.WithContext(ctx).Model(bs.stack).UpdateColumns(map[string]any{colCometSignature: sig, colCometError: msg}).Error
	}
	return true, nil
}

// combineNights combines a moving body's nights, each registered on the
// body. comet-starless holds them all as stacked; the master puts their
// comets alone (body-comet.fit), sky-free, on the field of one night
// (fieldNight), whose stars and frame it keeps. The other nights' stars,
// in other places and other fields, stay out of it. Without the layers it
// is the starless combination.
func (p *Pipeline) combineNights(ctx context.Context, bs bodyStack, rows []app.BodyNight, chosen app.BodyNight, sig string) error {
	stack := bs.stack
	field, err := p.bodyField(ctx, stack)
	if err != nil {
		return err
	}
	out, _, err := p.nightField(ctx, stack, chosen.Night)
	if err != nil {
		return fmt.Errorf("night %s: %w", chosen.Night.Format(goals.NightLayout), err)
	}
	times := []time.Time{field.refTime}
	for _, r := range rows {
		times = append(times, *r.RefTime)
	}
	eph, err := cometEphemeris(ctx, p.horizonsClient(), bs.body, times, field.lon, field.lat, field.elev)
	if err != nil {
		return err
	}
	raC0, decC0, ok := eph.at(*chosen.RefTime)
	if !ok {
		return errors.New("no body position on the field's night")
	}
	dir, err := os.MkdirTemp(p.workDir, "comet-nights-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	starless := newCombiner(out.sky, stack.Width, stack.Height, raC0, decC0)
	comets := newCombiner(out.sky, stack.Width, stack.Height, raC0, decC0)
	subs, alone := 0, 0
	for i, r := range rows {
		nf, _, err := p.nightField(ctx, stack, r.Night)
		if err != nil {
			return fmt.Errorf("night %s: %w", r.Night.Format(goals.NightLayout), err)
		}
		ra, dec, ok := eph.at(*r.RefTime)
		if !ok {
			return fmt.Errorf("no body position on the night of %s", r.Night.Format(goals.NightLayout))
		}
		p.progress(stack.Object, stack.Filter, StageComet, i, len(rows))
		d, w, h, err := p.nightLayer(ctx, dir, *r.StarlessKey)
		if err != nil {
			return err
		}
		weight := math.Max(r.EffectiveHours, 1e-6)
		starless.add(nightImage{data: d, w: w, h: h, sky: nf.sky, raC: ra, decC: dec, weight: weight, bg: nonzeroMedian(d)})
		c, w, h, err := p.nightLayer(ctx, dir, path.Join(path.Dir(*r.StarlessKey), bodyCometName))
		if err != nil {
			return err
		}
		if c != nil {
			comets.add(nightImage{data: c, w: w, h: h, sky: nf.sky, raC: ra, decC: dec, weight: weight})
			alone++
		}
		subs += r.Subs
	}
	data, cover := starless.result()
	var master []float32
	if alone == len(rows) {
		f, _, _, err := p.nightLayer(ctx, dir, path.Join(path.Dir(*chosen.StarlessKey), bodyFieldName))
		if err != nil {
			return err
		}
		if f != nil {
			c, _ := comets.result()
			master = cometOnField(f, c)
		}
	}
	return p.publishCombinedComet(ctx, stack, combined{starless: data, cover: cover, master: master, field: chosen.Night},
		len(rows), subs, *chosen.RefTime, sig, alignedOn(bs.body))
}

// nightLayer reads a night's layer stored at key; nil when there is none.
func (p *Pipeline) nightLayer(ctx context.Context, dir, key string) ([]float32, int, int, error) {
	if _, err := p.s3.StatObject(ctx, p.dest, key, minio.StatObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, err
	}
	file := filepath.Join(dir, path.Base(key))
	if err := p.download(ctx, p.dest, key, file); err != nil {
		return nil, 0, 0, err
	}
	defer os.Remove(file)
	return readSub(file)
}

// cometOnField adds the comet, sky-free, to a field of stars and sky.
func cometOnField(field, comet []float32) []float32 {
	out := slices.Clone(field)
	for i, v := range field {
		if v != 0 && v < 1 {
			out[i] = min(1, max(1e-7, v+comet[i]))
		}
	}
	return out
}

// fieldNight is the night a target's combined comet master shows the
// stars of: of rows, the night with the most effective hours over all the
// target's filters, so that every filter picks the same one, and the later
// one of equals.
func (p *Pipeline) fieldNight(ctx context.Context, object string, rows []app.BodyNight) app.BodyNight {
	var all []app.BodyNight
	if err := p.db.WithContext(ctx).Where("object = ?", object).Find(&all).Error; err != nil {
		slog.Warn("Could not read the target's nights; choosing from this filter's", "object", object, "error", err)
		all = rows
	}
	return pickFieldNight(rows, all)
}

func pickFieldNight(rows, all []app.BodyNight) app.BodyNight {
	hours := map[string]float64{}
	for _, r := range all {
		hours[r.Night.UTC().Format(goals.NightLayout)] += r.EffectiveHours
	}
	best := rows[0]
	for _, r := range rows[1:] {
		if h, b := hours[r.Night.UTC().Format(goals.NightLayout)], hours[best.Night.UTC().Format(goals.NightLayout)]; h > b || (h == b && r.Night.After(best.Night)) {
			best = r
		}
	}
	return best
}

// dataRect bounds the pixels of data that aren't empty.
func dataRect(data []float32, w, h int) Rect {
	cover := make([]uint8, len(data))
	for i, v := range data {
		if v != 0 {
			cover[i] = 1
		}
	}
	return coverRect(cover, w, h, 1)
}

// combined is a moving body's nights combined: starless, with how many
// nights cover each pixel, and the master on the field of night field, nil
// without one.
type combined struct {
	starless []float32
	cover    []uint8
	master   []float32
	field    time.Time
}

func (p *Pipeline) publishCombinedComet(ctx context.Context, stack *app.Stack, nc combined, nights, subs int, refTime time.Time, sig, aligned string) error {
	dir, err := os.MkdirTemp(p.workDir, "comet-nights-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	w, h := stack.Width, stack.Height
	own := []imagedata.Card{
		imagedata.StringCard("OBJECT", stack.Object, "target"),
		imagedata.StringCard("FILTER", stack.Filter, "filter"),
		imagedata.StringCard("IMAGETYP", "Master Light", aligned+", nights combined"),
		imagedata.IntCard("NCOMBINE", subs, "subs stacked"),
		imagedata.IntCard("NIGHTS", nights, "nights combined, each stacked on its own"),
		imagedata.FloatCard("EXPTIME", stack.ScaleExposure, "[s] scaled to one sub of this length"),
		imagedata.StringCard("COMETREF", refTime.UTC().Format("2006-01-02T15:04:05"), "body held where it was at this time (UTC)"),
		imagedata.StringCard("FIELDNIT", nc.field.Format(goals.NightLayout), "night whose stars and frame the master shows"),
		imagedata.StringCard("DATE", time.Now().UTC().Format("2006-01-02T15:04:05"), "file written"),
		imagedata.StringCard("SWCREATE", "astro-stacker (Siril 1.4 calibration)", ""),
	}
	cards := p.masterHeader(ctx, stack, own, dir, "")
	starlessCards := withWCS(cards, nil)
	wcs, _ := p.nightWCS(ctx, stack.Object, nc.field)
	cards = withWCS(cards, wcs)
	data := nc.starless
	need := uint8(0)
	for _, c := range nc.cover {
		need = max(need, c)
	}
	r := coverRect(nc.cover, w, h, need)
	if nc.master != nil {
		data = nc.master
		r = dataRect(data, w, h)
	}
	files := map[string]string{}
	for name, write := range map[string]func(string) error{
		"comet.fit":           func(f string) error { return writeFITSFile(f, w, h, 1, data, cards) },
		"comet.xisf":          func(f string) error { return writeXISFFile(f, w, h, data, cards) },
		"comet-starless.fit":  func(f string) error { return writeFITSFile(f, w, h, 1, nc.starless, starlessCards) },
		"comet-starless.xisf": func(f string) error { return writeXISFFile(f, w, h, nc.starless, starlessCards) },
	} {
		f := filepath.Join(dir, name)
		if err := write(f); err != nil {
			return err
		}
		files[name] = f
	}
	jpg, err := preview.Render(&imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(data, w, r)}, preview.DefaultOptions())
	if err != nil {
		return err
	}
	linear, err := linearPreview(&imagedata.Image{W: w, H: h, C: 1, Data: data})
	if err != nil {
		return err
	}
	prefix := stackPrefix(stack)
	keys := map[string]string{}
	for name, f := range files {
		k := path.Join(prefix, name)
		ct := contentTypeFITS
		if path.Ext(name) == ".xisf" {
			ct = contentTypeOctetStream
		}
		if err := p.upload(ctx, f, k, ct); err != nil {
			return err
		}
		keys[name] = k
	}
	keys["comet.jpg"] = path.Join(prefix, "comet.jpg")
	if err := p.putBytes(ctx, keys["comet.jpg"], jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG}); err != nil {
		return err
	}
	keys["comet-linear.bin"] = path.Join(prefix, "comet-linear.bin")
	if err := p.putBytes(ctx, keys["comet-linear.bin"], linear, minio.PutObjectOptions{ContentType: contentTypeOctetStream, ContentEncoding: contentEncodingGzip}); err != nil {
		return err
	}
	moved := stack.CometKey
	if err := p.db.WithContext(ctx).Model(stack).UpdateColumns(map[string]any{
		"comet_key": keys["comet.fit"], "comet_xisf_key": keys["comet.xisf"], "comet_preview_key": keys["comet.jpg"],
		"comet_linear_key": keys["comet-linear.bin"], "comet_subs": subs,
		"comet_crop_x": r.X, "comet_crop_y": r.Y, "comet_crop_w": r.W, "comet_crop_h": r.H,
		colCometSignature: sig, colCometError: nil, "comet_method": cometMethod,
	}).Error; err != nil {
		return err
	}
	p.removeMoved(ctx, moved, prefix, "comet.fit", "comet.xisf", "comet-starless.fit", "comet-starless.xisf",
		"comet-aligned.fit", "comet-aligned.xisf", "comet.jpg", "comet-linear.bin")
	for _, n := range []string{"comet-aligned.fit", "comet-aligned.xisf"} {
		if err := p.s3.RemoveObject(ctx, p.dest, path.Join(prefix, n), minio.RemoveObjectOptions{}); err != nil {
			slog.Warn("Could not remove a single night's comet master", "key", path.Join(prefix, n), "error", err)
		}
	}
	stack.CometSignature = sig
	stack.CometLinearKey = ptrTo(keys["comet-linear.bin"])
	stack.CometCropX, stack.CometCropY, stack.CometCropW, stack.CometCropH = r.X, r.Y, r.W, r.H
	p.refreshCover(ctx, stack.Object)
	p.Events.Publish(events.Event{Type: events.TypeMaster, Object: stack.Object, Filter: stack.Filter})
	slog.Info("Combined a moving body's nights into its master", "object", stack.Object, "filter", stack.Filter, "nights", nights, "subs", subs,
		"field_night", nc.field.Format(goals.NightLayout), "on_field", nc.master != nil)
	return nil
}
