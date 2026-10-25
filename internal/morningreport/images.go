package morningreport

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"golang.org/x/image/draw"
)

// Photo limits: the long side Telegram keeps, and a file size that keeps a
// full album of MediaGroupMax under the Bot API's 50 MB upload limit.
const (
	MaxPhotoSide  = 2560
	MaxPhotoBytes = 4<<20 + 512<<10
)

// images picks each target's current preview: its color cover if it has
// one, else the master of the filter with the most exposure.
func (c Collector) images(ctx context.Context, s *Subs, night time.Time) []Image {
	var out []Image
	for _, t := range s.Targets {
		news := 0
		for _, f := range t.Filters {
			news += f.Kept + f.Pending
		}
		if news == 0 {
			continue
		}
		im, ok, err := c.image(ctx, t, night)
		if err != nil {
			slog.Warn("Morning report: no preview", "object", t.Object, "error", err)
			continue
		}
		if !ok || im.NewSubs == 0 {
			continue
		}
		if c.Previews != nil {
			raw, err := c.Previews.Get(ctx, im.Key)
			if err == nil {
				raw, err = FitPhoto(raw)
			}
			if err != nil {
				slog.Warn("Morning report: preview unreadable", "object", t.Object, "key", im.Key, "error", err)
				continue
			}
			im.JPEG = raw
		}
		out = append(out, im)
	}
	return out
}

func (c Collector) image(ctx context.Context, t TargetSubs, night time.Time) (Image, bool, error) {
	db := c.App.WithContext(ctx)
	var stacks []app.Stack
	if err := db.Where("object = ?", t.Object).Find(&stacks).Error; err != nil {
		return Image{}, false, err
	}
	var covers []app.Cover
	if err := db.Where("subject = ?", t.Object).Limit(1).Find(&covers).Error; err != nil {
		return Image{}, false, err
	}
	if len(covers) == 1 && covers[0].PreviewKey != "" {
		cv := covers[0]
		filters, label := stacking.PaletteFilters(cv.Palette), cv.Palette+" color"
		if cv.Filter != "" {
			filters, label = []string{cv.Filter}, cv.Filter
		}
		im := preview(t, stacks, filters, cv.PreviewKey, label, cv.UpdatedAt)
		if im.NewSubs > 0 {
			if stacking.CometCover(withLinear(stacks)) {
				if err := c.comet(ctx, &im, stacks, filters, night); err != nil {
					return Image{}, false, err
				}
			}
			return im, true, nil
		}
	}
	fresh := map[string]int{}
	for _, f := range t.Filters {
		fresh[f.Filter] = f.Kept + f.Pending
	}
	var best *app.Stack
	better := func(s, b *app.Stack) bool {
		if (fresh[s.Filter] > 0) != (fresh[b.Filter] > 0) {
			return fresh[s.Filter] > 0
		}
		if s.ExposureSeconds != b.ExposureSeconds {
			return s.ExposureSeconds > b.ExposureSeconds
		}
		return filterRank(s.Filter) < filterRank(b.Filter)
	}
	for i := range stacks {
		s := &stacks[i]
		if s.PreviewKey == nil || *s.PreviewKey == "" {
			continue
		}
		if best == nil || better(s, best) {
			best = s
		}
	}
	if best == nil {
		return Image{}, false, nil
	}
	return preview(t, stacks, []string{best.Filter}, *best.PreviewKey, best.Filter, best.UpdatedAt), true, nil
}

func withLinear(stacks []app.Stack) []app.Stack {
	var out []app.Stack
	for _, s := range stacks {
		if s.LinearKey != nil {
			out = append(out, s)
		}
	}
	return out
}

// preview is the image at key, made from the masters of filters (nil for
// every filter) as last updated at updated.
func preview(t TargetSubs, stacks []app.Stack, filters []string, key, label string, updated time.Time) Image {
	im := Image{Object: t.Object, Key: key, Label: label, Current: true}
	var seconds float64
	for _, s := range stacks {
		if filters == nil || slices.Contains(filters, s.Filter) {
			seconds += s.ExposureSeconds
		}
	}
	im.Exposure = time.Duration(seconds * float64(time.Second))
	for _, f := range t.Filters {
		if filters != nil && !slices.Contains(filters, f.Filter) {
			continue
		}
		im.NewSubs += f.Kept + f.Pending
		if f.ByNight > 0 {
			im.ByNight = true
		}
		if f.Pending > 0 || f.Kept > f.ByNight && f.LastProcessed.After(updated) {
			im.Current = false
		}
	}
	return im
}

// comet fills in a comet cover, which stacks each filter's comet on itself
// from the target's imaged nights. The nights are known when their subs
// add up to the comet masters' subs; the exposure when the subs found
// match that count too.
func (c Collector) comet(ctx context.Context, im *Image, stacks []app.Stack, filters []string, night time.Time) error {
	db := c.App.WithContext(ctx)
	im.ByNight, im.Exposure, im.ExposureMissing = false, 0, true
	ids := []int{}
	want := map[string]int{}
	for _, s := range stacks {
		if filters == nil || slices.Contains(filters, s.Filter) {
			ids = append(ids, s.ID)
			want[s.Filter] = s.CometSubs
			im.CometSubs += s.CometSubs
		}
	}
	var rows []app.BodyNight
	if err := db.Where("object = ? AND starless_key IS NOT NULL", im.Object).Find(&rows).Error; err != nil {
		return err
	}
	got := map[string]int{}
	nights := map[string]bool{}
	for _, r := range rows {
		if _, ok := want[r.Filter]; ok {
			got[r.Filter] += r.Subs
			nights[r.Night.UTC().Format(time.DateOnly)] = true
		}
	}
	for f, n := range want {
		if got[f] != n {
			im.Current = false
			return nil
		}
	}
	keys := slices.Sorted(maps.Keys(nights))
	for _, k := range keys {
		d, err := time.Parse(time.DateOnly, k)
		if err != nil {
			return err
		}
		im.CometNights = append(im.CometNights, d)
	}
	im.Current = nights[NightKey(night)]

	var refs []app.NightReference
	if err := db.Where("object = ?", im.Object).Find(&refs).Error; err != nil {
		return err
	}
	own := map[string]bool{}
	for _, r := range refs {
		own[r.Night.UTC().Format(time.DateOnly)] = true
	}
	var subs []struct {
		Status   string
		Exposure float64
		Night    *time.Time
	}
	if err := db.Table("stack_frames sf").Select("sf.status, sf.exposure, f.night").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id IN ? AND sf.status IN ? AND sf.registered_key IS NOT NULL AND f.date_obs IS NOT NULL AND f.night IS NOT NULL AND sf.weight > 0 AND sf.exposure > 0",
			ids, []string{app.StackStatusAdded, app.StackStatusNight}).Scan(&subs).Error; err != nil {
		return err
	}
	n, seconds := 0, 0.0
	for _, s := range subs {
		k := s.Night.UTC().Format(time.DateOnly)
		if !nights[k] || (s.Status == app.StackStatusNight) != own[k] {
			continue
		}
		n++
		seconds += s.Exposure
	}
	if n == im.CometSubs {
		im.Exposure, im.ExposureMissing = time.Duration(seconds*float64(time.Second)), false
	}
	return nil
}

// FitPhoto shrinks a JPEG to Telegram's photo limits, or returns it as is
// when it fits.
func FitPhoto(raw []byte) ([]byte, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("not a JPEG: %w", err)
	}
	if max(cfg.Width, cfg.Height) <= MaxPhotoSide && len(raw) <= MaxPhotoBytes {
		return raw, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	w, h := cfg.Width, cfg.Height
	if long := max(w, h); long > MaxPhotoSide {
		w, h = w*MaxPhotoSide/long, h*MaxPhotoSide/long
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(1, w), max(1, h)))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	for _, q := range []int{90, 80, 70, 60} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if buf.Len() <= MaxPhotoBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("still over %d bytes at quality 60", MaxPhotoBytes)
}
