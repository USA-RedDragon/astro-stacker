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
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	skysite "github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm/clause"
)

const (
	StageBodyNight   = "body-night"
	bodyNightMinSubs = 3
	tailProbeArcsec  = 60.0
	bodyMeasureEvery = 10 * time.Minute
	bodyRetryAfter   = 2 * time.Hour
	bodyNightPoll    = 2 * time.Minute
	bodyCropMargin   = 256
)

var errNoTail = errors.New("an asteroid has no tail")

type nightSub struct {
	RegisteredKey string
	Exposure      float64
	Weight        float64
	Score         float64
	DateObs       time.Time
	Gain          *float64
	Night         time.Time
}

func (p *Pipeline) runBodyNights(ctx context.Context) {
	for !p.stopping(ctx) {
		if err := p.bodyNightsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Body night stacks failed", "error", err)
		}
		if !p.pause(ctx, bodyNightPoll) {
			return
		}
	}
}

type bodyStack struct {
	stack  *app.Stack
	body   bodies.Target
	nights map[string][]nightSub
	have   map[string]app.BodyNight
}

func (p *Pipeline) bodyStacks(ctx context.Context) ([]bodyStack, error) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("subs >= ? AND master_key IS NOT NULL", bodyNightMinSubs).Order("id").Find(&stacks).Error; err != nil {
		return nil, err
	}
	stored := p.storedDesignations(ctx)
	var out []bodyStack
	for i := range stacks {
		s := &stacks[i]
		body, ok := bodies.OwnBody(stored, s.Object)
		if !ok {
			continue
		}
		nights, err := p.nightSubs(ctx, s)
		if err != nil {
			return nil, err
		}
		have, err := p.pruneBodyNights(ctx, s, nights)
		if err != nil {
			return nil, err
		}
		out = append(out, bodyStack{stack: s, body: body, nights: nights, have: have})
	}
	return out, nil
}

func (p *Pipeline) pruneBodyNights(ctx context.Context, stack *app.Stack, nights map[string][]nightSub) (map[string]app.BodyNight, error) {
	var have []app.BodyNight
	if err := p.db.WithContext(ctx).Where("object = ? AND filter = ?", stack.Object, stack.Filter).Find(&have).Error; err != nil {
		return nil, err
	}
	out := map[string]app.BodyNight{}
	for _, n := range have {
		k := n.Night.UTC().Format(goals.NightLayout)
		if _, ok := nights[k]; !ok {
			if err := p.db.WithContext(ctx).Delete(&app.BodyNight{}, n.ID).Error; err != nil {
				return nil, err
			}
			continue
		}
		out[k] = n
	}
	return out, nil
}

func newestFirst(nights map[string][]nightSub) []string {
	keys := make([]string, 0, len(nights))
	for k := range nights {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	slices.Reverse(keys)
	return keys
}

func (p *Pipeline) bodyNightsOnce(ctx context.Context) error {
	stacks, err := p.bodyStacks(ctx)
	if err != nil {
		return err
	}
	for _, bs := range stacks {
		for _, k := range newestFirst(bs.nights) {
			if p.stopping(ctx) {
				return nil
			}
			row, err := p.measureNightIfDue(ctx, bs, k)
			if err != nil {
				return err
			}
			bs.have[k] = row
		}
	}
	for _, bs := range stacks {
		if time.Since(bs.stack.UpdatedAt) < p.opts.MosaicQuiet {
			continue
		}
		for _, k := range newestFirst(bs.nights) {
			if p.stopping(ctx) {
				return nil
			}
			done, err := p.imageNightIfDue(ctx, bs, k)
			if err != nil || done {
				return err
			}
		}
	}
	return nil
}

func retryDue(err *string, at time.Time) bool {
	return err != nil && *err != goals.ErrInsufficientData.Error() && time.Since(at) >= bodyRetryAfter
}

func (p *Pipeline) measureNightIfDue(ctx context.Context, bs bodyStack, k string) (app.BodyNight, error) {
	rows := bs.nights[k]
	sig := bodyNightSignature(rows, bs.body)
	row, had := bs.have[k]
	if !had {
		night, _ := time.Parse(goals.NightLayout, k)
		row = app.BodyNight{Object: bs.stack.Object, Filter: bs.stack.Filter, Night: night}
	}
	row.StackID, row.Kind = bs.stack.ID, goals.BodyKind(bs.body.Kind)
	switch {
	case row.Signature == sig && !retryDue(row.Error, row.MeasuredAt):
		return row, nil
	case had && row.Signature != sig && time.Since(row.MeasuredAt) < bodyMeasureEvery:
		return row, nil
	}
	if !p.hold(bs.stack.Object) {
		return row, nil
	}
	err := p.measureBodyNight(ctx, bs.stack, bs.body, rows, &row, sig)
	p.release(bs.stack.Object)
	if err != nil {
		return row, err
	}
	return row, p.saveBodyNight(ctx, row)
}

func (p *Pipeline) imageNightIfDue(ctx context.Context, bs bodyStack, k string) (bool, error) {
	rows := bs.nights[k]
	row, had := bs.have[k]
	if !had || len(rows) < bodyNightMinSubs {
		return false, nil
	}
	sig := bodyNightSignature(rows, bs.body)
	if row.Signature != sig || retryDue(row.Error, time.Time{}) || (row.ImageSignature == sig && !retryDue(row.ImageError, row.UpdatedAt)) {
		return false, nil
	}
	if !p.hold(bs.stack.Object) {
		return false, nil
	}
	err := p.imageBodyNight(ctx, bs.stack, bs.body, rows, &row)
	p.release(bs.stack.Object)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		slog.Warn("Body night image failed", "object", bs.stack.Object, "filter", bs.stack.Filter, "night", k, "error", err)
		msg := err.Error()
		row.ImageError = &msg
	} else {
		row.ImageError = nil
	}
	row.ImageSignature = sig
	return true, p.saveBodyNight(ctx, row)
}

func (p *Pipeline) nightSubs(ctx context.Context, stack *app.Stack) (map[string][]nightSub, error) {
	var rows []nightSub
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.registered_key, sf.exposure, sf.weight, sf.score, f.date_obs, f.gain, f.night").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id = ? AND sf.status = ? AND sf.registered_key IS NOT NULL AND f.date_obs IS NOT NULL AND f.night IS NOT NULL AND sf.weight > 0 AND sf.exposure > 0",
			stack.ID, app.StackStatusAdded).
		Order("f.date_obs").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]nightSub{}
	for _, r := range rows {
		k := r.Night.UTC().Format(goals.NightLayout)
		out[k] = append(out[k], r)
	}
	return out, nil
}

func bodyNightSignature(rows []nightSub, body bodies.Target) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%d", goals.BodyMethod, body.Designation, goals.BodyKind(body.Kind), len(rows))
	for _, r := range rows {
		fmt.Fprintf(h, "\x00%s\x00%.4f\x00%.4f", r.RegisteredKey, r.Weight, r.Score)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func resetMeasure(row *app.BodyNight) {
	keep := app.BodyNight{ID: row.ID, Object: row.Object, Filter: row.Filter, Night: row.Night, StackID: row.StackID, Kind: row.Kind,
		ImageSignature: row.ImageSignature, ImageError: row.ImageError, MasterKey: row.MasterKey, StarlessKey: row.StarlessKey, PreviewKey: row.PreviewKey}
	*row = keep
}

func (p *Pipeline) measureBodyNight(ctx context.Context, stack *app.Stack, body bodies.Target, rows []nightSub, row *app.BodyNight, sig string) error {
	resetMeasure(row)
	row.Signature, row.Method, row.Subs, row.EffectiveHours = sig, goals.BodyMethod, len(rows), effectiveHours(rows)
	row.MeasuredAt = time.Now().UTC()
	if len(rows) < goals.MinSubs {
		msg := goals.ErrInsufficientData.Error()
		row.Error = &msg
		return nil
	}
	defer p.finished(stack.Object)
	if _, err := p.computeBodyNight(ctx, stack, body, rows, row, true); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("Body night measurement failed", "object", stack.Object, "filter", stack.Filter, "night", row.Night.Format(goals.NightLayout), "error", err)
		msg := err.Error()
		row.Error = &msg
		return nil
	}
	slog.Info("Body night measured", "object", stack.Object, "filter", stack.Filter, "night", row.Night.Format(goals.NightLayout),
		"kind", row.Kind, "subs", row.Subs, "snr", math.Round(row.SNR*100)/100, "trail_px", math.Round(deref(row.TrailPx)), "reason", row.LowReason)
	return nil
}

func (p *Pipeline) imageBodyNight(ctx context.Context, stack *app.Stack, body bodies.Target, rows []nightSub, row *app.BodyNight) error {
	defer p.finished(stack.Object)
	scratch := app.BodyNight{Object: row.Object, Filter: row.Filter, Night: row.Night, Kind: row.Kind, Subs: len(rows), EffectiveHours: effectiveHours(rows)}
	layers, err := p.computeBodyNight(ctx, stack, body, rows, &scratch, false)
	if err != nil {
		return err
	}
	scratch.Subs = len(rows)
	if err := p.publishBodyNight(ctx, stack, layers, *scratch.RefTime, &scratch, alignedOn(body)); err != nil {
		return err
	}
	row.MasterKey, row.StarlessKey, row.PreviewKey = scratch.MasterKey, scratch.StarlessKey, scratch.PreviewKey
	slog.Info("Body night image published", "object", stack.Object, "filter", stack.Filter, "night", row.Night.Format(goals.NightLayout), "subs", len(rows))
	return nil
}

func effectiveHours(rows []nightSub) float64 {
	s := 0.0
	for _, r := range rows {
		s += r.Score * r.Exposure
	}
	return s / 3600
}

func (p *Pipeline) saveBodyNight(ctx context.Context, row app.BodyNight) error {
	row.UpdatedAt = time.Now().UTC()
	return p.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "object"}, {Name: "filter"}, {Name: "night"}},
		UpdateAll: true,
	}).Create(&row).Error
}

func middleTime(rows []nightSub) time.Time {
	r := rows[len(rows)/2]
	return r.DateObs.Add(time.Duration(r.Exposure / 2 * float64(time.Second)))
}

func offsetSky(ra, dec, paDeg, arcsec float64) (float64, float64) {
	d := arcsec / 3600
	pa := paDeg * math.Pi / 180
	return ra + d*math.Sin(pa)/math.Cos(dec*math.Pi/180), dec + d*math.Cos(pa)
}

func (p *Pipeline) tailPA(ctx context.Context, body bodies.Target, field bodyField, at time.Time) (*float64, error) {
	if goals.BodyKind(body.Kind) != goals.BodyComet {
		return nil, errNoTail
	}
	elev := field.elev
	start := at.UTC().Truncate(10 * time.Minute)
	tab, err := p.horizonsClient().EphemerisWithAngles(ctx, horizons.Request{
		Command: horizons.CommandFor(body.Designation, body.Kind),
		Site:    skysite.Site{Latitude: field.lat, Longitude: field.lon, Elevation: &elev},
		Start:   start, Stop: start.Add(20 * time.Minute), Step: 10 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	var best *float64
	bestGap := time.Duration(math.MaxInt64)
	for _, r := range tab.Rows {
		gap := r.At.Sub(at)
		if gap < 0 {
			gap = -gap
		}
		if r.PsAng != nil && gap < bestGap {
			v := *r.PsAng
			best, bestGap = &v, gap
		}
	}
	return best, nil
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func bodyCrop(track [][2]float64, x0, y0, arcsecPerPx float64, w, h int) Rect {
	half := float64(bodyCropMargin)
	if arcsecPerPx > 0 {
		half = math.Max(half, goals.TailLengthArcsec/arcsecPerPx+64)
	}
	minX, maxX, minY, maxY := x0, x0, y0, y0
	for _, t := range track {
		minX, maxX, minY, maxY = math.Min(minX, t[0]), math.Max(maxX, t[0]), math.Min(minY, t[1]), math.Max(maxY, t[1])
	}
	x1, y1 := max(0, int(math.Floor(minX-half))), max(0, int(math.Floor(minY-half)))
	x2, y2 := min(w, int(math.Ceil(maxX+half))+1), min(h, int(math.Ceil(maxY+half))+1)
	return Rect{X: x1, Y: y1, W: max(0, x2-x1), H: max(0, y2-y1)}
}

func (p *Pipeline) computeBodyNight(ctx context.Context, stack *app.Stack, body bodies.Target, rows []nightSub, row *app.BodyNight, cropped bool) (cometLayers, error) {
	field, err := p.bodyField(ctx, stack)
	if err != nil {
		return cometLayers{}, err
	}
	refTime := middleTime(rows)
	row.RefTime = &refTime
	lon := field.lon
	row.Longitude = &lon
	scales := decodeGainTable(stack.GainScales)
	subs := make([]storedSub, len(rows))
	mids := make([]time.Time, len(rows))
	times := []time.Time{refTime}
	for i, r := range rows {
		mids[i] = r.DateObs.Add(time.Duration(r.Exposure / 2 * float64(time.Second)))
		subs[i] = storedSub{key: r.RegisteredKey, exposure: r.Exposure * scales.scale(r.Gain), weight: r.Weight}
		times = append(times, mids[i])
	}
	eph, err := cometEphemeris(ctx, p.horizonsClient(), body, times, field.lon, field.lat, field.elev)
	if err != nil {
		return cometLayers{}, err
	}
	x0, y0, shifts, track, err := bodyTrack(eph, field, stack, refTime, mids)
	if err != nil {
		return cometLayers{}, err
	}
	row.HeadX, row.HeadY = &x0, &y0
	trail := math.Hypot(track[len(track)-1][0]-track[0][0], track[len(track)-1][1]-track[0][1])
	row.TrailPx = &trail

	tail, arcsecPerPx, err := p.bodyGeometry(ctx, field, eph, body, stack, refTime, x0, y0, row)
	if err != nil {
		return cometLayers{}, err
	}
	box := Rect{W: stack.Width, H: stack.Height}
	if cropped {
		box = bodyCrop(track, x0, y0, arcsecPerPx, stack.Width, stack.Height)
	}
	if box.W < 8*goals.NoiseBin || box.H < 8*goals.NoiseBin {
		return cometLayers{}, errors.New("too little of the frame around the body")
	}
	for i := range track {
		track[i] = [2]float64{track[i][0] - float64(box.X), track[i][1] - float64(box.Y)}
	}
	lx, ly := x0-float64(box.X), y0-float64(box.Y)
	sat := p.opts.Stack.SaturationLevel
	if sat <= 0 {
		sat = goals.DefaultSaturation
	}
	var bm *bodyMeasure
	var taps []cometTap
	if cropped {
		bm = newBodyMeasure(goals.BodyKind(body.Kind), box.W, box.H, stack.Object, stack.Filter, rows, subs, row, lx, ly, tail, arcsecPerPx, sat)
		taps = append(taps, bm.tap)
	}
	var files []string
	read := func(i int) ([]float32, int, int, error) {
		sub, w, h, err := readSub(files[i])
		if err != nil || !cropped {
			return sub, w, h, err
		}
		if w != stack.Width || h != stack.Height {
			return nil, 0, 0, fmt.Errorf("sub %s is %dx%d, master %dx%d", subs[i].key, w, h, stack.Width, stack.Height)
		}
		return crop(sub, w, box), box.W, box.H, nil
	}

	dir, err := os.MkdirTemp(p.workDir, "body-night-")
	if err != nil {
		return cometLayers{}, err
	}
	defer os.RemoveAll(dir)
	files = make([]string, len(subs))
	for i, s := range subs {
		p.progress(stack.Object, stack.Filter, StageBodyNight, i, len(subs))
		files[i] = filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, files[i]); err != nil {
			return cometLayers{}, err
		}
	}
	master, _, err := p.loadMaster(ctx, dir, stack)
	if err != nil {
		return cometLayers{}, fmt.Errorf("star-aligned master: %w", err)
	}
	mdata := master.Data
	if cropped && master.W == stack.Width && master.H == stack.Height {
		mdata = crop(master.Data, master.W, box)
	}
	stars := starMask(mdata, box.W, box.H)
	layers, err := separateComet(subs, shifts, track, stars, p.opts.Stack, read, func(done, total int) {
		p.progress(stack.Object, stack.Filter, StageBodyNight, done, total)
	}, taps...)
	if err != nil {
		return cometLayers{}, err
	}
	if bm != nil {
		if bm.tapErr != nil {
			return cometLayers{}, bm.tapErr
		}
		bm.apply(row)
	}
	row.MeasuredAt = time.Now().UTC()
	return layers, nil
}

func bodyTrack(eph ephemeris, field bodyField, stack *app.Stack, refTime time.Time, mids []time.Time) (float64, float64, [][2]float64, [][2]float64, error) {
	at := func(t time.Time) (float64, float64, error) {
		ra, dec, ok := eph.at(t)
		if !ok {
			return 0, 0, fmt.Errorf("no body position at %s", t)
		}
		x, y, ok := field.pixel(ra, dec, stack.Height)
		if !ok {
			return 0, 0, errors.New("the body is off the plate solution")
		}
		return x, y, nil
	}
	x0, y0, err := at(refTime)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	if x0 < 0 || y0 < 0 || x0 >= float64(stack.Width) || y0 >= float64(stack.Height) {
		return 0, 0, nil, nil, errors.New("the body was outside the target's reference frame that night")
	}
	shifts := make([][2]float64, len(mids))
	track := make([][2]float64, len(mids))
	for i, m := range mids {
		x, y, err := at(m)
		if err != nil {
			return 0, 0, nil, nil, err
		}
		shifts[i], track[i] = [2]float64{x0 - x, y0 - y}, [2]float64{x, y}
	}
	return x0, y0, shifts, track, nil
}

func (p *Pipeline) bodyGeometry(ctx context.Context, field bodyField, eph ephemeris, body bodies.Target, stack *app.Stack, refTime time.Time, x0, y0 float64, row *app.BodyNight) (*[2]float64, float64, error) {
	ra0, dec0, _ := eph.at(refTime)
	pa, err := p.tailPA(ctx, body, field, refTime)
	if err != nil && ctx.Err() != nil {
		return nil, 0, err
	}
	probePA := 0.0
	if pa != nil {
		probePA = *pa
		row.TailPA = pa
	}
	rp, dp := offsetSky(ra0, dec0, probePA, tailProbeArcsec)
	px, py, ok := field.pixel(rp, dp, stack.Height)
	if !ok {
		return nil, 0, errors.New("the body's surroundings are off the plate solution")
	}
	probeLen := math.Hypot(px-x0, py-y0)
	if probeLen <= 0 {
		return nil, 0, errors.New("the plate solution has no scale at the body")
	}
	row.ArcsecPerPx = tailProbeArcsec / probeLen
	if pa == nil {
		return nil, row.ArcsecPerPx, nil
	}
	dx, dy := (px-x0)/probeLen, (py-y0)/probeLen
	row.TailDX, row.TailDY = &dx, &dy
	return &[2]float64{dx, dy}, row.ArcsecPerPx, nil
}

type bodyMeasure struct {
	kind      string
	subs      []storedSub
	x0, y0    float64
	sat       float32
	noTail    bool
	coma      goals.ComaMasks
	meas      *goals.Measurer
	measErr   error
	tapErr    error
	fluxes    []float64
	saturated int
}

func newBodyMeasure(kind string, w, h int, object, filter string, rows []nightSub, subs []storedSub, row *app.BodyNight, x0, y0 float64, tail *[2]float64, arcsecPerPx float64, sat float32) *bodyMeasure {
	bm := &bodyMeasure{kind: kind, subs: subs, x0: x0, y0: y0, sat: sat, noTail: tail == nil}
	if kind != goals.BodyComet {
		return bm
	}
	bm.coma = goals.ComaRegions(w/goals.NoiseBin, h/goals.NoiseBin, x0, y0, tail, arcsecPerPx)
	row.RegionBins = bm.coma.HeadBins + bm.coma.TailBins
	gsubs := make([]goals.Sub, len(rows))
	for i, r := range rows {
		gsubs[i] = goals.Sub{Exposure: subs[i].exposure, Weight: r.Weight, Effective: r.Score * r.Exposure}
	}
	bm.meas, bm.measErr = goals.NewMeasurer(w, h, gsubs, goals.Options{
		Seed: goals.Seed(object+"\x00"+row.Night.Format(goals.NightLayout), filter), RegionMask: bm.coma.Union()})
	return bm
}

func (bm *bodyMeasure) tap(i int, sub []float32, w, h int, scale float64) {
	if bm.kind != goals.BodyComet {
		f := goals.ApertureFlux(sub, w, h, bm.x0, bm.y0, bm.sat)
		switch {
		case f.Saturated:
			bm.saturated++
		case f.OK && scale > 0:
			bm.fluxes = append(bm.fluxes, f.Flux/scale)
		}
		return
	}
	if bm.measErr != nil || bm.tapErr != nil {
		return
	}
	b, err := goals.Bin(sub, w, h, bm.subs[i].exposure, bm.sat)
	if err == nil {
		err = bm.meas.Add(i, b)
	}
	bm.tapErr = err
}

func (bm *bodyMeasure) apply(row *app.BodyNight) {
	if bm.kind == goals.BodyComet {
		applyComaMeasure(row, bm.meas, bm.measErr, bm.coma, bm.noTail)
		return
	}
	applyPointMeasure(row, bm.fluxes, bm.saturated)
}

func applyComaMeasure(row *app.BodyNight, meas *goals.Measurer, measErr error, coma goals.ComaMasks, noTail bool) {
	if measErr != nil {
		msg := measErr.Error()
		row.Error = &msg
		return
	}
	res, err := meas.Finish(row.EffectiveHours)
	if err != nil {
		msg := err.Error()
		row.Error = &msg
		return
	}
	row.Levels = res.Levels
	row.NoiseA, row.NoiseB, row.NoiseNow = res.NoiseA, res.NoiseB, res.NoiseNow
	row.GainPerHourPct = res.GainPerHourPct
	if !goals.FloorMeasurable(res.Levels) {
		row.LowReason = goals.FloorReason(res.Levels)
	}
	row.LowConfidence = row.LowReason != ""
	sig, ok := goals.MeasureComa(res, coma)
	if !ok || !(res.NoiseNow > 0) {
		msg := "the comet's head is not covered by the night's subs"
		row.Error = &msg
		return
	}
	row.Signal, row.SNR = sig.Signal, sig.Signal/res.NoiseNow
	head := sig.Head / res.NoiseNow
	row.HeadSNR = &head
	if sig.HasTail {
		tail := sig.Tail / res.NoiseNow
		row.TailSNR = &tail
	}
	switch {
	case noTail:
		row.LowReason = joinReason(row.LowReason, "no tail direction from JPL Horizons, so the head alone")
	case !sig.HasTail:
		row.LowReason = joinReason(row.LowReason, "the tail runs off the frame, so the head alone")
	}
	if sig.Signal <= 0 {
		row.LowConfidence = true
		row.LowReason = joinReason(row.LowReason, "no signal from the comet")
	}
}

func applyPointMeasure(row *app.BodyNight, fluxes []float64, saturated int) {
	r, err := goals.PointSNR(fluxes, saturated)
	if err != nil {
		msg := err.Error()
		row.Error = &msg
		return
	}
	row.Levels = 0
	row.Signal, row.NoiseNow, row.SNR = r.Flux, r.Sigma/math.Sqrt(float64(max(1, r.Measured))), r.SNR
	row.Saturated = goals.PointSaturated(r)
	if row.Saturated {
		row.LowReason = goals.ReasonSaturated
	}
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func (p *Pipeline) publishBodyNight(ctx context.Context, stack *app.Stack, layers cometLayers, refTime time.Time, row *app.BodyNight, aligned string) error {
	dir, err := os.MkdirTemp(p.workDir, "body-night-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	acc := layers.comet
	starless := acc.Master(stack.ScaleExposure)
	master := addStars(starless, layers.stars, layers.add, stack.ScaleExposure)
	night := row.Night.Format(goals.NightLayout)
	own := []imagedata.Card{
		imagedata.StringCard("OBJECT", stack.Object, "target"),
		imagedata.StringCard("FILTER", stack.Filter, "filter"),
		imagedata.StringCard("IMAGETYP", "Master Light", aligned+", one night"),
		imagedata.StringCard("NIGHT", night, "observing night (local date it began)"),
		imagedata.IntCard("NCOMBINE", row.Subs, "subs stacked"),
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
	fitsFile, starlessFile := filepath.Join(dir, "body.fit"), filepath.Join(dir, "body-starless.fit")
	if err := writeFITSFile(fitsFile, acc.W, acc.H, 1, master, cards); err != nil {
		return err
	}
	if err := writeFITSFile(starlessFile, acc.W, acc.H, 1, starless, starlessCards); err != nil {
		return err
	}
	r := coverageCrop(acc)
	jpg, err := preview.Render(&imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(master, acc.W, r)}, preview.DefaultOptions())
	if err != nil {
		return err
	}
	prefix := path.Join(stackPrefix(stack), "nights", night)
	masterKey, starlessKey, previewKey := path.Join(prefix, "body.fit"), path.Join(prefix, "body-starless.fit"), path.Join(prefix, "body.jpg")
	if err := p.upload(ctx, fitsFile, masterKey, contentTypeFITS); err != nil {
		return err
	}
	if err := p.upload(ctx, starlessFile, starlessKey, contentTypeFITS); err != nil {
		return err
	}
	if err := p.putBytes(ctx, previewKey, jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG}); err != nil {
		return err
	}
	row.MasterKey, row.StarlessKey, row.PreviewKey = &masterKey, &starlessKey, &previewKey
	return nil
}

const bodyKeep = goals.PointSkyOuter + 2

type savedDisc struct {
	idx  []int
	vals []float32
}

func saveDisc(sub []float32, w, h int, c [2]float64, r int) savedDisc {
	var d savedDisc
	for y := max(0, int(c[1])-r); y <= min(h-1, int(c[1])+r); y++ {
		for x := max(0, int(c[0])-r); x <= min(w-1, int(c[0])+r); x++ {
			if math.Hypot(float64(x)-c[0], float64(y)-c[1]) <= float64(r) {
				i := y*w + x
				d.idx = append(d.idx, i)
				d.vals = append(d.vals, sub[i])
			}
		}
	}
	return d
}

func (d savedDisc) restore(sub []float32) {
	for j, i := range d.idx {
		sub[i] = d.vals[j]
	}
}
