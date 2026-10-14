package stacking

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// DustHistory is a lights check over archived lights.
type DustHistory struct {
	From, To time.Time
	// Days is each window's length; Per the most lights of one target and
	// filter in a window.
	Days int
	Per  int
	// Probes are sensor positions (x, y, rows top first) whose residual is
	// printed for each window.
	Probes [][2]int
}

type cachedPlane struct {
	data   []float32
	w, h   int
	median float32
	key    string
}

// DustLightsHistory runs the lights check over archived lights window by
// window, calibrating each light here with its matched bias and flat
// (building them if needed), and writes what it finds to out. Residual
// maps go to the bucket under dust/history. Nothing else is written.
func (p *Pipeline) DustLightsHistory(ctx context.Context, h DustHistory, out io.Writer) error {
	if h.Days < 1 || h.Per < 1 {
		return errors.New("days and per must be at least 1")
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return err
	}
	cache := map[string]cachedPlane{}
	for from := h.From; from.Before(h.To); from = from.AddDate(0, 0, h.Days) {
		to := from.AddDate(0, 0, h.Days)
		var frames []app.Frame
		if err := p.db.WithContext(ctx).Where("type = ? AND index_error IS NULL AND date_obs >= ? AND date_obs < ?",
			frameTypeLight, from, to).Order("object, filter, date_obs").Find(&frames).Error; err != nil {
			return err
		}
		picked := pickPer(frames, h.Per)
		var maps []dustcheck.Map
		fields := map[string]bool{}
		flats := map[string]bool{}
		for _, f := range picked {
			if p.stopping(ctx) {
				return ctx.Err()
			}
			m, flatKey, err := p.historySub(ctx, f, sets, cache)
			if err != nil {
				fmt.Fprintf(out, "  skip %s: %v\n", f.Key, err)
				continue
			}
			maps = append(maps, m)
			fields[f.Object] = true
			flats[flatKey] = true
		}
		status := dustcheck.LightsStatus(len(maps), len(fields))
		fmt.Fprintf(out, "%s..%s: %d lights, %d fields, %d flats: %s\n", from.Format(time.DateOnly), to.AddDate(0, 0, -1).Format(time.DateOnly),
			len(maps), len(fields), len(flats), status)
		if len(maps) < 3 {
			continue
		}
		var check app.DustLightCheck
		r, discs, err := p.residualCheck(ctx, maps, path.Join("dust", "history", from.Format(time.DateOnly)), &check)
		if err != nil {
			return err
		}
		noisy := ""
		if check.Sigma > dustcheck.LightsMaxNoise {
			noisy = " (too noisy to tell)"
		}
		fmt.Fprintf(out, "  residual noise %.3f%%%s, preview %s\n", check.Sigma*100, noisy, *check.PreviewKey)
		for _, d := range discs {
			fmt.Fprintf(out, "  disc at %d,%d d%d %+.2f%% edge %.2f sharp %.2f\n", d.X, d.Y, d.Diameter, d.Peak, d.Edge, d.Sharp)
		}
		for _, pr := range h.Probes {
			fmt.Fprintf(out, "  probe %d,%d: %+.2f%%\n", pr[0], pr[1], probe(r, pr[0], pr[1])*100)
		}
	}
	return nil
}

// probe is a map's mean in a box 44 map pixels across around a sensor
// position, after a light blur.
func probe(r dustcheck.Map, x, y int) float64 {
	g := dustcheck.Gaussian(r, 2)
	cx, cy := x/dustcheck.BinFactor, y/dustcheck.BinFactor
	sum, n := 0.0, 0
	for yy := max(0, cy-22); yy < min(g.H, cy+22); yy++ {
		for xx := max(0, cx-22); xx < min(g.W, cx+22); xx++ {
			sum += float64(g.At(xx, yy))
			n++
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

func pickPer(frames []app.Frame, per int) []app.Frame {
	var out []app.Frame
	count := map[[2]string]int{}
	for _, f := range frames {
		if precalibrated(f) || f.Night == nil {
			continue
		}
		k := [2]string{f.Object, f.Filter}
		if count[k] >= per {
			continue
		}
		count[k]++
		out = append(out, f)
	}
	return out
}

// historySub calibrates one archived light with its matched bias and flat
// and returns its sub map and the flat's key.
func (p *Pipeline) historySub(ctx context.Context, f app.Frame, sets []calmatch.Set, cache map[string]cachedPlane) (dustcheck.Map, string, error) {
	g := calmatch.Group{Night: *f.Night, Filter: f.Filter, Exposure: val(f.Exposure), Gain: val(f.Gain), Offset: val(f.Offset),
		SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator)}
	m := calmatch.Choose(g, sets)
	if m.Flat.Set == nil || m.Bias.Set == nil {
		return dustcheck.Map{}, "", errors.New("no flat or bias")
	}
	load := func(set calmatch.Set) (cachedPlane, string, error) {
		if c, ok := cache[dustGroupKey(set)+set.Type+set.From.String()]; ok {
			return c, c.key, nil
		}
		data, w, h, key, err := p.masterPixels(ctx, set, sets)
		if err != nil {
			return cachedPlane{}, "", err
		}
		if len(cache) >= 6 {
			clear(cache)
		}
		c := cachedPlane{data: data, w: w, h: h, median: medianOf(data), key: key}
		cache[dustGroupKey(set)+set.Type+set.From.String()] = c
		return c, key, nil
	}
	bias, _, err := load(*m.Bias.Set)
	if err != nil {
		return dustcheck.Map{}, "", fmt.Errorf("bias: %w", err)
	}
	flat, flatKey, err := load(*m.Flat.Set)
	if err != nil {
		return dustcheck.Map{}, "", fmt.Errorf("flat: %w", err)
	}
	dir, err := os.MkdirTemp(p.workDir, "dust-")
	if err != nil {
		return dustcheck.Map{}, "", err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "light"+path.Ext(f.Key))
	if err := p.download(ctx, p.source, f.Key, local); err != nil {
		return dustcheck.Map{}, "", err
	}
	data, w, h, err := readSub(local)
	if err != nil {
		return dustcheck.Map{}, "", err
	}
	if w != flat.w || h != flat.h || w != bias.w || h != bias.h {
		return dustcheck.Map{}, "", fmt.Errorf("light is %dx%d, flat %dx%d, bias %dx%d", w, h, flat.w, flat.h, bias.w, bias.h)
	}
	for i := range data {
		data[i] = (data[i] - bias.data[i]) / (flat.data[i] / flat.median)
	}
	sm, err := dustcheck.SubMap(data, w, h)
	return sm, flatKey, err
}

func medianOf(v []float32) float32 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}
