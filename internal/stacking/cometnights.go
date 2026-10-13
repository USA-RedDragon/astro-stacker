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

func combinedSignature(rows []app.BodyNight) string {
	h := sha256.New()
	fmt.Fprintf(h, "nights\x00%d\x00%d", cometNightsMethod, cometMethod)
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
		switch {
		case !ok:
			return nil, false
		case r.ImageError != nil && r.ImageSignature == sig:
			continue
		case retryDue(r.Error, time.Time{}) && r.Signature == sig:
			continue
		case r.ImageSignature != sig || r.Signature != sig || r.StarlessKey == nil || r.RefTime == nil:
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
	sig := combinedSignature(rows)
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
	err := p.combineNights(ctx, bs, rows, sig)
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

func (p *Pipeline) combineNights(ctx context.Context, bs bodyStack, rows []app.BodyNight, sig string) error {
	stack := bs.stack
	field, err := p.bodyField(ctx, stack)
	if err != nil {
		return err
	}
	times := []time.Time{field.refTime}
	for _, r := range rows {
		times = append(times, *r.RefTime)
	}
	eph, err := cometEphemeris(ctx, p.horizonsClient(), bs.body, times, field.lon, field.lat, field.elev)
	if err != nil {
		return err
	}
	raC0, decC0, ok := eph.at(field.refTime)
	if !ok {
		return errors.New("no body position at the reference sub's time")
	}
	dir, err := os.MkdirTemp(p.workDir, "comet-nights-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	comb := newCombiner(field.sky, stack.Width, stack.Height, raC0, decC0)
	subs := 0
	for i, r := range rows {
		nf, _, err := p.nightField(ctx, stack, r.Night)
		if err != nil {
			return fmt.Errorf("night %s: %w", r.Night.Format(goals.NightLayout), err)
		}
		ra, dec, ok := eph.at(*r.RefTime)
		if !ok {
			return fmt.Errorf("no body position on the night of %s", r.Night.Format(goals.NightLayout))
		}
		file := filepath.Join(dir, fmt.Sprintf("n%03d.fit", i))
		p.progress(stack.Object, stack.Filter, StageComet, i, len(rows))
		if err := p.download(ctx, p.dest, *r.StarlessKey, file); err != nil {
			return err
		}
		d, w, h, err := readSub(file)
		if err != nil {
			return err
		}
		_ = os.Remove(file)
		comb.add(nightImage{data: d, w: w, h: h, sky: nf.sky, raC: ra, decC: dec, weight: math.Max(r.EffectiveHours, 1e-6), bg: nonzeroMedian(d)})
		subs += r.Subs
	}
	data, cover := comb.result()
	return p.publishCombinedComet(ctx, stack, data, cover, len(rows), subs, field.refTime, sig, alignedOn(bs.body))
}

func (p *Pipeline) publishCombinedComet(ctx context.Context, stack *app.Stack, data []float32, cover []uint8, nights, subs int, refTime time.Time, sig, aligned string) error {
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
		imagedata.StringCard("DATE", time.Now().UTC().Format("2006-01-02T15:04:05"), "file written"),
		imagedata.StringCard("SWCREATE", "astro-stacker (Siril 1.4 calibration)", ""),
	}
	cards := p.masterHeader(ctx, stack, own, dir, "")
	starlessCards := cards[:0:0]
	for _, c := range cards {
		if !wcsKey.MatchString(c.Key) {
			starlessCards = append(starlessCards, c)
		}
	}
	files := map[string]string{}
	for name, write := range map[string]func(string) error{
		"comet.fit":           func(f string) error { return writeFITSFile(f, w, h, 1, data, cards) },
		"comet.xisf":          func(f string) error { return writeXISFFile(f, w, h, data, cards) },
		"comet-starless.fit":  func(f string) error { return writeFITSFile(f, w, h, 1, data, starlessCards) },
		"comet-starless.xisf": func(f string) error { return writeXISFFile(f, w, h, data, starlessCards) },
	} {
		f := filepath.Join(dir, name)
		if err := write(f); err != nil {
			return err
		}
		files[name] = f
	}
	need := uint8(0)
	for _, c := range cover {
		need = max(need, c)
	}
	r := coverRect(cover, w, h, need)
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
	slog.Info("Combined a moving body's nights into its master", "object", stack.Object, "filter", stack.Filter, "nights", nights, "subs", subs)
	return nil
}
