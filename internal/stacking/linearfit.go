package stacking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// StageLinearFit is reported while a target's masters are being fitted.
const StageLinearFit = "linear_fit"

// fitRejectHigh is the brightest value, in either master, a pixel may have to
// be fitted, as in PixInsight's LinearFit: stars near saturation respond
// non-linearly.
const fitRejectHigh = 0.92

// fitSamples caps how many pixels a fit uses.
const fitSamples = 2_000_000

// fitGroups are the filters fitted to each other. Colour channels are
// matched to colour channels and narrowband to narrowband; luminance, and
// any filter not listed, is left alone.
var fitGroups = [][]string{
	{"Red", "Green", "Blue"},
	{"H-a", "O-III", "S-II"},
}

// linearFitsOnce refits each target's filter groups whose masters changed
// and have been left alone for MosaicQuiet.
func (p *Pipeline) linearFitsOnce(ctx context.Context) error {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("subs > 0 AND master_key IS NOT NULL").
		Order("object, filter").Find(&stacks).Error; err != nil {
		return err
	}
	type key struct{ object, group string }
	groups := map[key][]app.Stack{}
	for _, s := range stacks {
		g := fitGroup(s.Filter)
		if g == "" {
			if s.FittedKey != nil {
				p.dropFit(ctx, &s)
			}
			continue
		}
		groups[key{s.Object, g}] = append(groups[key{s.Object, g}], s)
	}
	for k, masters := range groups {
		if len(masters) < 2 {
			for i := range masters {
				if masters[i].FittedKey != nil {
					p.dropFit(ctx, &masters[i])
				}
			}
			continue
		}
		if err := p.linearFitIfDue(ctx, k.object, masters); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("LinearFit failed", "object", k.object, "filters", k.group, "error", err)
		}
	}
	return nil
}

// fitGroup names the group a filter is fitted within, or "" for none.
func fitGroup(filter string) string {
	for _, g := range fitGroups {
		if slices.Contains(g, filter) {
			return strings.Join(g, "/")
		}
	}
	return ""
}

// dropFit removes a fitted master that no longer has a group to fit in.
func (p *Pipeline) dropFit(ctx context.Context, s *app.Stack) {
	if err := p.s3.RemoveObject(ctx, p.dest, *s.FittedKey, minio.RemoveObjectOptions{}); err != nil {
		slog.Warn("Could not remove fitted master", "object", s.Object, "filter", s.Filter, "error", err)
		return
	}
	if err := p.db.WithContext(ctx).Model(s).UpdateColumns(map[string]any{
		"fitted_key": nil, "fit_reference": "", "fit_offset": 0, "fit_scale": 0, "fit_signature": "",
	}).Error; err != nil {
		slog.Warn("Could not clear fitted master", "object", s.Object, "filter", s.Filter, "error", err)
	}
}

// fitSignature identifies the masters a fit was made from.
func fitSignature(masters []app.Stack) (string, time.Time) {
	h := sha256.New()
	var newest time.Time
	for _, m := range masters {
		fmt.Fprintf(h, "%s\x00%d\x00", m.Filter, m.UpdatedAt.UnixNano())
		if m.UpdatedAt.After(newest) {
			newest = m.UpdatedAt
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32], newest
}

// fitReference is the filter the others are fitted to: the one with the
// most effective exposure, whose master is the least noisy.
func fitReference(masters []app.Stack) int {
	best := 0
	for i, m := range masters {
		if m.EffectiveSeconds > masters[best].EffectiveSeconds {
			best = i
		}
	}
	return best
}

func (p *Pipeline) linearFitIfDue(ctx context.Context, object string, masters []app.Stack) error {
	sig, newest := fitSignature(masters)
	current := true
	for _, m := range masters {
		current = current && m.FitSignature == sig
	}
	if current || time.Since(newest) < p.opts.MosaicQuiet {
		return nil
	}
	if !p.hold(object) {
		return nil // a worker is stacking it; its masters are about to change
	}
	defer p.release(object)
	p.progress(object, "", StageLinearFit, 0, len(masters))
	defer p.finished(object)

	dir, err := os.MkdirTemp(p.workDir, "linearfit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ri := fitReference(masters)
	ref := &masters[ri]
	refIm, _, err := p.loadMaster(ctx, dir, ref)
	if err != nil {
		return fmt.Errorf("load %s: %w", ref.Filter, err)
	}
	for i := range masters {
		m := &masters[i]
		p.progress(object, m.Filter, StageLinearFit, i, len(masters))
		im, cards, err := p.loadMaster(ctx, dir, m)
		if err != nil {
			return fmt.Errorf("load %s: %w", m.Filter, err)
		}
		offset, scale := 0.0, 1.0
		if i != ri {
			if im.W != refIm.W || im.H != refIm.H {
				return fmt.Errorf("%s is %dx%d, %s %dx%d", m.Filter, im.W, im.H, ref.Filter, refIm.W, refIm.H)
			}
			r := intersect(cropRect(m), cropRect(ref))
			var ok bool
			if offset, scale, ok = fitLinear(im.Data, refIm.Data, im.W, r); !ok {
				return fmt.Errorf("too few pixels to fit %s to %s", m.Filter, ref.Filter)
			}
			applyLinear(im.Data, offset, scale)
		}
		out := append(cards,
			imagedata.StringCard("LFREF", ref.Filter, "LinearFit reference filter"),
			imagedata.FloatCard("LFOFFSET", offset, "LinearFit: this = offset + scale * master"),
			imagedata.FloatCard("LFSCALE", scale, "LinearFit scale"),
		)
		file := filepath.Join(dir, "linearfit.fit")
		if err := writeFITSFile(file, im.W, im.H, 1, im.Data, out); err != nil {
			return err
		}
		key := path.Join(stackPrefix(m), "linearfit.fit")
		if err := p.upload(ctx, file, key, "application/fits"); err != nil {
			return err
		}
		// UpdateColumns leaves updated_at, which the signature is made of.
		if err := p.db.WithContext(ctx).Model(m).UpdateColumns(map[string]any{
			"fitted_key": key, "fit_reference": ref.Filter, "fit_offset": offset, "fit_scale": scale, "fit_signature": sig,
		}).Error; err != nil {
			return err
		}
		os.Remove(file)
		slog.Info("Linear fitted master", "object", object, "filter", m.Filter, "reference", ref.Filter,
			"offset", offset, "scale", scale)
	}
	p.Events.Publish(events.Event{Type: events.TypeMaster, Object: object})
	return nil
}

// loadMaster downloads a master and returns its image and header, without
// the cards describing the file's layout.
func (p *Pipeline) loadMaster(ctx context.Context, dir string, s *app.Stack) (*imagedata.Image, []imagedata.Card, error) {
	local := filepath.Join(dir, strings.ReplaceAll(s.Filter, "/", "_")+".fit")
	defer os.Remove(local)
	if err := p.download(ctx, p.dest, *s.MasterKey, local); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return nil, nil, err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return nil, nil, err
	}
	if im.C != 1 {
		return nil, nil, fmt.Errorf("master has %d channels", im.C)
	}
	all, err := frameheader.ParseCards(b)
	if err != nil {
		return nil, nil, err
	}
	var cards []imagedata.Card
	for _, c := range all {
		switch {
		case c.Name == "SIMPLE", c.Name == "BITPIX", c.Name == "EXTEND", c.Name == "BZERO", c.Name == "BSCALE",
			strings.HasPrefix(c.Name, "NAXIS"), strings.HasPrefix(c.Name, "LF"):
		default:
			cards = append(cards, imageCard(c))
		}
	}
	return im, cards, nil
}

func cropRect(s *app.Stack) Rect {
	if s.CropW <= 0 || s.CropH <= 0 {
		return Rect{W: s.Width, H: s.Height}
	}
	return Rect{X: s.CropX, Y: s.CropY, W: s.CropW, H: s.CropH}
}

func intersect(a, b Rect) Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	return Rect{X: x0, Y: y0, W: max(0, x1-x0), H: max(0, y1-y0)}
}

// fitLinear finds offset and scale so offset + scale×target best matches
// ref inside r, over pixels with data below fitRejectHigh in both. Like
// PixInsight's LinearFit it minimises absolute rather than squared
// deviations. Structure one filter sees and the other doesn't (emission in
// H-a but not O-III) sits far off the line and can still tilt it, so pixels
// more than 5σ off are dropped and the fit repeated.
func fitLinear(target, ref []float32, w int, r Rect) (offset, scale float64, ok bool) {
	stride := max(1, int(math.Sqrt(float64(r.W*r.H)/fitSamples)))
	var xs, ys []float64
	for y := r.Y; y < r.Y+r.H; y += stride {
		for x := r.X; x < r.X+r.W; x += stride {
			i := y*w + x
			t, f := target[i], ref[i]
			if t > 0 && f > 0 && t < fitRejectHigh && f < fitRejectHigh {
				xs = append(xs, float64(t))
				ys = append(ys, float64(f))
			}
		}
	}
	for range 5 {
		if len(xs) < 100 {
			return 0, 1, false
		}
		if offset, scale, ok = fitLAD(xs, ys); !ok {
			return 0, 1, false
		}
		res := make([]float64, len(xs))
		for i, x := range xs {
			res[i] = math.Abs(ys[i] - offset - scale*x)
		}
		limit := 5 * 1.4826 * median(res)
		kx, ky := xs[:0], ys[:0]
		for i, x := range xs {
			if res[i] <= limit {
				kx, ky = append(kx, x), append(ky, ys[i])
			}
		}
		if len(kx) == len(xs) {
			break
		}
		xs, ys = kx, ky
	}
	return offset, scale, scale > 0
}

// fitLAD fits y = a + b×x minimising absolute deviations, by iteratively
// reweighted least squares.
func fitLAD(xs, ys []float64) (a, b float64, ok bool) {
	wts := make([]float64, len(xs))
	for i := range wts {
		wts[i] = 1
	}
	b = 1
	for range 50 {
		var sw, sx, sy, sxx, sxy float64
		for i, x := range xs {
			wt := wts[i]
			sw += wt
			sx += wt * x
			sy += wt * ys[i]
			sxx += wt * x * x
			sxy += wt * x * ys[i]
		}
		den := sw*sxx - sx*sx
		if den == 0 {
			return 0, 1, false
		}
		nb := (sw*sxy - sx*sy) / den
		na := (sy - nb*sx) / sw
		done := math.Abs(nb-b) < 1e-7*math.Abs(nb) && math.Abs(na-a) < 1e-9
		a, b = na, nb
		if done {
			break
		}
		for i, x := range xs {
			wts[i] = 1 / math.Max(math.Abs(ys[i]-a-b*x), 1e-7)
		}
	}
	return a, b, true
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

// applyLinear maps data to offset + scale×data, leaving empty pixels at 0
// and keeping the rest within (0, 1] as PixInsight would.
func applyLinear(data []float32, offset, scale float64) {
	const floor = 1e-7 // above 0, which marks pixels without data
	for i, v := range data {
		if v == 0 {
			continue
		}
		data[i] = float32(math.Min(1, math.Max(floor, offset+scale*float64(v))))
	}
}
