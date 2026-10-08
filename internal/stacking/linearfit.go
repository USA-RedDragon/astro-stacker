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
func fitGroups() [][]string {
	return [][]string{
		{filterRed, filterGreen, filterBlue},
		{filterHa, filterOIII, filterSII},
	}
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
			if s.FittedKey != nil || s.FitError != nil {
				p.dropFit(ctx, &s)
			}
			continue
		}
		groups[key{s.Object, g}] = append(groups[key{s.Object, g}], s)
	}
	for k, masters := range groups {
		if p.stopping(ctx) {
			return nil
		}
		if len(masters) < 2 {
			for i := range masters {
				if masters[i].FittedKey != nil || masters[i].FitError != nil {
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
	for _, g := range fitGroups() {
		if slices.Contains(g, filter) {
			return strings.Join(g, "/")
		}
	}
	return ""
}

// dropFit removes a fitted master that no longer has a group to fit in.
func (p *Pipeline) dropFit(ctx context.Context, s *app.Stack) {
	if s.FittedKey != nil {
		if err := p.s3.RemoveObject(ctx, p.dest, *s.FittedKey, minio.RemoveObjectOptions{}); err != nil {
			slog.Warn("Could not remove fitted master", "object", s.Object, "filter", s.Filter, "error", err)
			return
		}
	}
	if err := p.db.WithContext(ctx).Model(s).UpdateColumns(map[string]any{
		columnFittedKey: nil, columnFitReference: "", columnFitOffset: 0, columnFitScale: 0, columnFitSignature: "", columnFitError: nil,
	}).Error; err != nil {
		slog.Warn("Could not clear fitted master", "object", s.Object, "filter", s.Filter, "error", err)
	}
}

// fitMethod is part of every fit's signature, so a change to how fits are
// made refits every target.
const fitMethod = 2 // symmetric fit without clipping; reference by filter

// fitSignature identifies the masters a fit was made from, and how.
func fitSignature(masters []app.Stack) (string, time.Time) {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", fitMethod)
	var newest time.Time
	for _, m := range masters {
		fmt.Fprintf(h, "%s\x00%d\x00", m.Filter, m.UpdatedAt.UnixNano())
		if m.UpdatedAt.After(newest) {
			newest = m.UpdatedAt
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32], newest
}

// fitReference is the filter the others are fitted to: the first of its
// group, in fitGroups' order, that the target has. The reference only sets
// the level the group is brought to, and a fixed choice brings every panel
// of a mosaic to the same filter. Effective exposure, used before, isn't
// comparable between filters: it picked S-II for one panel of a mosaic and
// H-a for its neighbours.
func fitReference(masters []app.Stack) int {
	rank := func(filter string) int {
		for _, g := range fitGroups() {
			if i := slices.Index(g, filter); i >= 0 {
				return i
			}
		}
		return math.MaxInt
	}
	best := 0
	for i, m := range masters {
		if rank(m.Filter) < rank(masters[best].Filter) {
			best = i
		}
	}
	return best
}

// fitError is a fit that can't succeed until the masters change, unlike
// a download or upload that might work next time.
type fitError struct{ msg string }

func (e *fitError) Error() string { return e.msg }

func unfittable(format string, args ...any) error {
	return &fitError{fmt.Sprintf(format, args...)}
}

// fitMasters fits a master to the reference over the part of the frame
// both cover.
func fitMasters(im, ref *imagedata.Image, crop, refCrop Rect) (offset, scale float64, err error) {
	if im.W != ref.W || im.H != ref.H {
		return 0, 1, unfittable("master is %dx%d, reference %dx%d", im.W, im.H, ref.W, ref.H)
	}
	return fitLinear(im.Data, ref.Data, im.W, intersect(crop, refCrop))
}

// failFit records why a master couldn't be fitted, against the signature of
// the masters it was tried with so it isn't tried again until one changes,
// and takes down its fit of older masters.
func (p *Pipeline) failFit(ctx context.Context, m *app.Stack, sig, msg string) error {
	if m.FittedKey != nil {
		if err := p.s3.RemoveObject(ctx, p.dest, *m.FittedKey, minio.RemoveObjectOptions{}); err != nil {
			return fmt.Errorf("remove old fit of %s: %w", m.Filter, err)
		}
	}
	return p.db.WithContext(ctx).Model(m).UpdateColumns(map[string]any{
		columnFittedKey: nil, columnFitReference: "", columnFitOffset: 0, columnFitScale: 0,
		columnFitSignature: sig, columnFitError: msg,
	}).Error
}

// fitDue gives the signature of a group's masters and whether they are to
// be fitted: they've been left alone for quiet and some master's last fit,
// or failure to fit, was of other masters. A fit that failed to download or
// upload records nothing and is due again next pass.
func fitDue(masters []app.Stack, quiet time.Duration) (string, bool) {
	sig, newest := fitSignature(masters)
	current := true
	for _, m := range masters {
		current = current && m.FitSignature == sig
	}
	return sig, !current && time.Since(newest) >= quiet
}

func (p *Pipeline) linearFitIfDue(ctx context.Context, object string, masters []app.Stack) error {
	sig, due := fitDue(masters, p.opts.MosaicQuiet)
	if !due {
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
			offset, scale, err = fitMasters(im, refIm, cropRect(m), cropRect(ref))
			if err != nil {
				// Left unfitted, and not tried again until a master
				// changes; the rest of the group is still published.
				msg := fmt.Sprintf("fitting %s to %s: %v", m.Filter, ref.Filter, err)
				slog.Warn("Could not linear fit master", "object", object, "filter", m.Filter,
					"reference", ref.Filter, "error", err)
				if err := p.failFit(ctx, m, sig, msg); err != nil {
					return err
				}
				continue
			}
			applyLinear(im.Data, offset, scale)
		}
		out := slices.Concat(cards, []imagedata.Card{
			imagedata.StringCard("LFREF", ref.Filter, "LinearFit reference filter"),
			imagedata.FloatCard("LFOFFSET", offset, "LinearFit: this = offset + scale * master"),
			imagedata.FloatCard("LFSCALE", scale, "LinearFit scale"),
		})
		file := filepath.Join(dir, "linearfit.xisf")
		if err := writeXISFFile(file, im.W, im.H, im.Data, out); err != nil {
			return err
		}
		key := path.Join(stackPrefix(m), "linearfit.xisf")
		if err := p.upload(ctx, file, key, contentTypeOctetStream); err != nil {
			return err
		}
		if m.FittedKey != nil && *m.FittedKey != key {
			// Fits were FITS before; the XISF replaces it.
			_ = p.s3.RemoveObject(ctx, p.dest, *m.FittedKey, minio.RemoveObjectOptions{})
		}
		// UpdateColumns leaves updated_at, which the signature is made of.
		if err := p.db.WithContext(ctx).Model(m).UpdateColumns(map[string]any{
			columnFittedKey: key, columnFitReference: ref.Filter, columnFitOffset: offset, columnFitScale: scale,
			columnFitSignature: sig, columnFitError: nil,
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
		case c.Name == keywordSIMPLE, c.Name == keywordBITPIX, c.Name == keywordEXTEND, c.Name == keywordBZERO, c.Name == keywordBSCALE,
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
// deviations.
//
// Most of a master is sky, where pixel noise is far larger than any signal
// the two filters share. Regressing one on the other there pulls the slope
// towards 0 (regression dilution): ref on target underestimates the scale
// and target on ref overestimates it. So both are fitted and the scale is
// their geometric mean, which is exact when the two masters are equally
// noisy for their signal, halves the error otherwise, and comes out the
// same whichever way round the pair is fitted. Nothing is sigma clipped:
// clipping about the sky's residuals threw away the stars and nebula that
// carry the relation and left sky noise to fit, which gave one faint O-III
// master a negative scale and fitted others at a fifth of what chaining
// them through a third filter gave. Without it, structure only the target
// sees tilts the fit by a few percent.
func fitLinear(target, ref []float32, w int, r Rect) (offset, scale float64, err error) {
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
	if len(xs) < 100 {
		return 0, 1, unfittable("only %d pixels with data in both", len(xs))
	}
	up, ok := fitLAD(xs, ys)
	down, ok2 := fitLAD(ys, xs)
	if !ok || !ok2 {
		return 0, 1, unfittable("no spread in the pixels to fit")
	}
	if up <= 0 || down <= 0 {
		return 0, 1, unfittable("no positive relation between them (slopes %.3g and %.3g)", up, 1/down)
	}
	scale = math.Sqrt(up / down)
	res := make([]float64, len(xs))
	for i, x := range xs {
		res[i] = ys[i] - scale*x
	}
	return median(res), scale, nil
}

// fitLAD fits y = a + b×x minimising absolute deviations, by iteratively
// reweighted least squares, and returns b.
func fitLAD(xs, ys []float64) (b float64, ok bool) {
	wts := make([]float64, len(xs))
	for i := range wts {
		wts[i] = 1
	}
	var a float64
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
			return 1, false
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
	return b, true
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
