package stacking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"log/slog"
	"math"
	"path"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm/clause"
)

func dustSubKey(night time.Time, frameID int) string {
	return path.Join("dust", "lights", night.Format(time.DateOnly), fmt.Sprintf("%d.sub.zst", frameID))
}

// keepDustSubs keeps a 4×4 binned copy of each light just calibrated with a
// flat, from a recent night, for its night's lights check. Failures are
// only logged: the check is a side line of stacking.
func (p *Pipeline) keepDustSubs(ctx context.Context, cals []calibrated, now time.Time) {
	if !p.opts.DustCheck {
		return
	}
	for _, c := range cals {
		f := c.c.frame
		if c.flat == "" || f.Night == nil || now.Sub(*f.Night) > DustLightsRecent {
			continue
		}
		if err := p.keepDustSub(ctx, c, *f.Night); err != nil && ctx.Err() == nil {
			slog.Warn("Could not keep a light for the dust check", "frame", f.Key, "error", err)
		}
	}
}

func (p *Pipeline) keepDustSub(ctx context.Context, c calibrated, night time.Time) error {
	data, w, h, err := readSub(c.path)
	if err != nil {
		return err
	}
	m, err := dustcheck.SubMap(data, w, h)
	if err != nil {
		return err
	}
	b, err := dustcheck.EncodeSub(m)
	if err != nil {
		return err
	}
	f := c.c.frame
	key := dustSubKey(night, f.ID)
	if err := p.putBytes(ctx, key, b, minio.PutObjectOptions{ContentType: contentTypeOctetStream}); err != nil {
		return err
	}
	row := app.DustLightSub{FrameID: f.ID, Night: night, Object: f.Object, Filter: f.Filter, FlatMaster: c.flat, Key: key, CreatedAt: time.Now()}
	return p.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "frame_id"}},
		UpdateAll: true,
	}).Create(&row).Error
}

type dustNight struct {
	Night  time.Time
	Subs   int
	Fields int
	Last   time.Time
}

// keptNights sums the kept lights by night.
func (p *Pipeline) keptNights(ctx context.Context) ([]dustNight, error) {
	var subs []app.DustLightSub
	if err := p.db.WithContext(ctx).Select("night, object, created_at").Find(&subs).Error; err != nil {
		return nil, fmt.Errorf("load kept lights: %w", err)
	}
	by := map[time.Time]*dustNight{}
	objects := map[time.Time]map[string]bool{}
	var out []*dustNight
	for _, s := range subs {
		n := by[s.Night]
		if n == nil {
			n = &dustNight{Night: s.Night}
			by[s.Night] = n
			objects[s.Night] = map[string]bool{}
			out = append(out, n)
		}
		n.Subs++
		objects[s.Night][s.Object] = true
		n.Fields = len(objects[s.Night])
		if s.CreatedAt.After(n.Last) {
			n.Last = s.CreatedAt
		}
	}
	res := make([]dustNight, len(out))
	for i, n := range out {
		res[i] = *n
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Night.Before(res[j].Night) })
	return res, nil
}

// checkLightNights checks each recent night whose kept lights stopped
// arriving DustLightsQuiet ago and that changed since its last check, and
// drops kept lights older than DustLightsKeep.
func (p *Pipeline) checkLightNights(ctx context.Context, now time.Time) error {
	nights, err := p.keptNights(ctx)
	if err != nil {
		return err
	}
	type due struct {
		n    dustNight
		prev app.DustLightCheck
	}
	var todo []due
	for _, n := range nights {
		if p.stopping(ctx) {
			return ctx.Err()
		}
		if now.Sub(n.Night) > DustLightsKeep {
			p.dropDustSubs(ctx, n.Night)
			continue
		}
		if now.Sub(n.Last) < DustLightsQuiet {
			continue
		}
		var prev app.DustLightCheck
		err := p.db.WithContext(ctx).Where("night = ?", n.Night).Limit(1).Find(&prev).Error
		if err != nil {
			return err
		}
		if prev.ID != 0 && prev.Subs == n.Subs {
			continue
		}
		todo = append(todo, due{n, prev})
	}
	if len(todo) > 0 {
		p.DustTask.Start("", int64(len(todo)), "nights")
	}
	for i, d := range todo {
		n, prev := d.n, d.prev
		if p.stopping(ctx) {
			return ctx.Err()
		}
		p.DustTask.SetProgress(int64(i), int64(len(todo)))
		p.DustTask.SetCurrent("checking the lights of " + n.Night.Format("2 Jan 2006") + " for dust")
		if err := p.checkLightNight(ctx, n, prev.ID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("Could not check a night's lights for dust", "night", n.Night.Format(time.DateOnly), "error", err)
			failed := app.DustLightCheck{ID: prev.ID, Night: n.Night, Subs: n.Subs, Fields: n.Fields,
				Status: dustcheck.LightsFailed, CheckedAt: time.Now()}
			if err := p.db.WithContext(ctx).Save(&failed).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Pipeline) dropDustSubs(ctx context.Context, night time.Time) {
	var subs []app.DustLightSub
	if err := p.db.WithContext(ctx).Where("night = ?", night).Find(&subs).Error; err != nil {
		return
	}
	for _, s := range subs {
		if err := p.s3.RemoveObject(ctx, p.dest, s.Key, minio.RemoveObjectOptions{}); err != nil {
			slog.Warn("Could not delete a kept light", "key", s.Key, "error", err)
			return
		}
		p.db.WithContext(ctx).Delete(&s)
	}
}

func (p *Pipeline) checkLightNight(ctx context.Context, n dustNight, id int) error {
	check := app.DustLightCheck{ID: id, Night: n.Night, Subs: n.Subs, Fields: n.Fields,
		Status: dustcheck.LightsStatus(n.Subs, n.Fields), CheckedAt: time.Now()}
	if check.Status == dustcheck.LightsChecked {
		var subs []app.DustLightSub
		if err := p.db.WithContext(ctx).Where("night = ?", n.Night).Order("frame_id").Find(&subs).Error; err != nil {
			return err
		}
		maps := make([]dustcheck.Map, 0, min(len(subs), DustLightsMax))
		for _, s := range spread(subs, DustLightsMax) {
			b, err := p.getBytes(ctx, s.Key)
			if err != nil {
				return err
			}
			m, err := dustcheck.DecodeSub(b)
			if err != nil {
				return fmt.Errorf("%s: %w", s.Key, err)
			}
			maps = append(maps, m)
		}
		_, res, err := p.residualCheck(ctx, maps, path.Join("dust", "nights", n.Night.Format(time.DateOnly)), &check)
		if err != nil {
			return err
		}
		if check.Sigma > dustcheck.LightsMaxNoise {
			check.Status = dustcheck.LightsNoisy
			res = nil
		}
		for _, d := range res {
			slog.Warn("Lights hold dust their flats don't", "night", n.Night.Format(time.DateOnly), "x", d.X, "y", d.Y,
				"diameter", d.Diameter, "depth", fmt.Sprintf("%+.2f%%", d.Peak))
		}
	}
	return p.db.WithContext(ctx).Save(&check).Error
}

// residualCheck runs the lights check on sub maps, stores the residual and
// its preview under prefix, and fills in check.
func (p *Pipeline) residualCheck(ctx context.Context, maps []dustcheck.Map, prefix string, check *app.DustLightCheck) (dustcheck.Map, []dustcheck.Blob, error) {
	r, err := dustcheck.Residual(maps)
	if err != nil {
		return r, nil, err
	}
	s := dustcheck.Prepare(r)
	discs := dustcheck.ResidualDiscs(s)
	enc, err := dustcheck.Encode(r)
	if err != nil {
		return r, nil, err
	}
	mapKey := prefix + "/residual.map.zst"
	if err := p.putBytes(ctx, mapKey, enc, minio.PutObjectOptions{ContentType: contentTypeOctetStream}); err != nil {
		return r, nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dustcheck.Stretch(dustcheck.Gaussian(r, 1.5), -0.02, 0.01)); err != nil {
		return r, nil, err
	}
	pngKey := prefix + "/residual.png"
	if err := p.putBytes(ctx, pngKey, buf.Bytes(), minio.PutObjectOptions{ContentType: contentTypePNG}); err != nil {
		return r, nil, err
	}
	js, err := json.Marshal(discs)
	if err != nil {
		return r, nil, err
	}
	check.ResidualKey, check.PreviewKey = &mapKey, &pngKey
	check.Discs = string(js)
	check.Sigma = dustcheck.Sigma(s)
	check.Deepest = 0
	for _, d := range discs {
		check.Deepest = math.Max(check.Deepest, math.Abs(d.Peak))
	}
	return r, discs, nil
}

// spread picks at most n items evenly from all.
func spread[T any](all []T, n int) []T {
	if len(all) <= n {
		return all
	}
	out := make([]T, n)
	for i := range n {
		out[i] = all[i*len(all)/n]
	}
	return out
}
