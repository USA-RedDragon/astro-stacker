package morningreport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// DustChange is one dust change the flats showed, across the filters that
// show it.
type DustChange struct {
	Class    string
	Scope    string
	X, Y     int
	Diameter int
	// Depth is in percent, negative for a new shadow.
	Depth   float64
	Filters []string
	Since   time.Time
	// Crop is the previous flat, the new one and their difference, as JPEG.
	Crop []byte
}

// DustLights is the night's lights check when it found dust the flats
// don't hold.
type DustLights struct {
	Discs   []dustcheck.Blob
	Preview []byte
}

// RimTarget is one target's lights of the night that carried a dust rim
// streak (dustcheck.RimStreak) deep enough to cost weight.
type RimTarget struct {
	Object  string
	Filters []string
	Lowered int
	LeftOut int
	// Shallowest and Deepest are the streaks' depths, in percent of the
	// sky.
	Shallowest, Deepest float64
}

// rims collects the night's lights whose dust rim streak cost them weight,
// by target.
func (c Collector) rims(ctx context.Context, r *Report) {
	var rows []app.RimStreak
	if err := c.App.WithContext(ctx).Where("night = ? AND factor < 1", r.Night.Format(time.DateOnly)).Order("object, filter").Find(&rows).Error; err != nil {
		slog.Warn("Morning report: dust rim streaks unreadable", "error", err)
		return
	}
	by := map[string]*RimTarget{}
	for _, s := range rows {
		t := by[s.Object]
		if t == nil {
			t = &RimTarget{Object: s.Object, Shallowest: math.Inf(1)}
			by[s.Object] = t
		}
		if len(t.Filters) == 0 || t.Filters[len(t.Filters)-1] != s.Filter {
			t.Filters = append(t.Filters, s.Filter)
		}
		if s.Factor <= 0 {
			t.LeftOut++
		} else {
			t.Lowered++
		}
		d := math.Abs(s.Depth)
		t.Shallowest, t.Deepest = math.Min(t.Shallowest, d), math.Max(t.Deepest, d)
	}
	for _, t := range by {
		r.Rims = append(r.Rims, *t)
	}
	sort.Slice(r.Rims, func(i, j int) bool { return r.Rims[i].Deepest > r.Rims[j].Deepest })
}

// dust collects the flat dust changes detected in the report's window from
// flats of the last day or so, and the night's lights check.
func (c Collector) dust(ctx context.Context, r *Report) {
	db := c.App.WithContext(ctx)
	var evs []app.DustEvent
	if err := db.Where("detected_at >= ? AND detected_at < ? AND taken_at >= ? AND class IN ?", r.Start.UTC(), r.End.UTC(), r.Start.Add(-24*time.Hour).UTC(),
		[]string{dustcheck.ClassNew, dustcheck.ClassGone, dustcheck.ClassMoved}).Order("group_key, id").Find(&evs).Error; err != nil {
		slog.Warn("Morning report: dust changes unreadable", "error", err)
		return
	}
	prevAt := map[int]time.Time{}
	seen := map[string]bool{}
	for _, e := range evs {
		if e.Class == dustcheck.ClassMoved && e.Depth > 0 {
			continue
		}
		key := e.GroupKey
		if key == "" {
			key = fmt.Sprint(e.ID)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := prevAt[e.PrevID]; !ok {
			var prev app.DustFlat
			if err := db.Select("taken_at").Where("id = ?", e.PrevID).Limit(1).Find(&prev).Error; err == nil {
				prevAt[e.PrevID] = prev.TakenAt
			}
		}
		d := DustChange{Class: e.Class, Scope: e.Scope, X: e.X, Y: e.Y, Diameter: e.Diameter, Depth: e.Depth,
			Filters: strings.Split(e.Filters, ","), Since: prevAt[e.PrevID]}
		if e.Filters == "" {
			d.Filters = []string{e.Filter}
		}
		if e.CropKey != nil && c.Previews != nil {
			if raw, err := c.Previews.Get(ctx, *e.CropKey); err == nil {
				d.Crop, err = pngToJPEG(raw)
				if err != nil {
					slog.Warn("Morning report: dust crop unreadable", "key", *e.CropKey, "error", err)
				}
			}
		}
		r.Dust = append(r.Dust, d)
	}
	sort.SliceStable(r.Dust, func(i, j int) bool { return math.Abs(r.Dust[i].Depth) > math.Abs(r.Dust[j].Depth) })

	var check app.DustLightCheck
	if err := db.Where("checked_at >= ? AND checked_at < ? AND night >= ? AND status = ?", r.Start.UTC(), r.End.UTC(),
		r.Night.AddDate(0, 0, -2), dustcheck.LightsChecked).Order("night DESC").Limit(1).Find(&check).Error; err != nil || check.ID == 0 {
		return
	}
	var discs []dustcheck.Blob
	if json.Unmarshal([]byte(check.Discs), &discs) != nil || len(discs) == 0 {
		return
	}
	r.DustLights = &DustLights{Discs: discs}
	if check.PreviewKey != nil && c.Previews != nil {
		if raw, err := c.Previews.Get(ctx, *check.PreviewKey); err == nil {
			r.DustLights.Preview, _ = pngToJPEG(raw)
		}
	}
}

func pngToJPEG(raw []byte) ([]byte, error) {
	im, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return FitPhoto(buf.Bytes())
}

func dustWhat(d DustChange) string {
	switch d.Class {
	case dustcheck.ClassGone:
		return "shadow gone"
	case dustcheck.ClassMoved:
		return "shadow moved"
	}
	return "new shadow"
}

func dustWhere(d DustChange) string {
	switch d.Scope {
	case dustcheck.ScopeWindow:
		return "camera window"
	case dustcheck.ScopeFilter:
		return "filter"
	}
	return "window or filter"
}

// dust is the report's dust lines: one per flat change, and the lights
// check's leftover shadows.
func (f Formatter) dust(r Report) string {
	if len(r.Dust) == 0 && r.DustLights == nil && len(r.Rims) == 0 {
		return ""
	}
	lines := []string{"<b>Dust</b>"}
	for _, d := range r.Dust {
		since := ""
		if !d.Since.IsZero() {
			since = " since " + d.Since.In(f.Loc).Format("Jan 2")
		}
		lines = append(lines, fmt.Sprintf("%s on the %s at x %d, y %d · %s%% deep · %d px · %s%s",
			dustWhat(d), dustWhere(d), d.X, d.Y, num(math.Abs(d.Depth), 2), d.Diameter, esc(strings.Join(d.Filters, ", ")), since))
	}
	if r.DustLights != nil {
		for _, b := range r.DustLights.Discs {
			what := "a shadow the flats don't have"
			if b.Peak > 0 {
				what = "a shadow the flats have that the lights don't"
			}
			lines = append(lines, fmt.Sprintf("lights: %s at x %d, y %d · %s%% · %d px", what, b.X, b.Y, num(math.Abs(b.Peak), 2), b.Diameter))
		}
	}
	for _, t := range r.Rims {
		lines = append(lines, rimLine(t))
	}
	return strings.Join(lines, "\n")
}

// rimLine tells one target's moonlit dust rim streak.
func rimLine(t RimTarget) string {
	depth := num(t.Deepest, 2) + "%"
	if t.Shallowest < t.Deepest {
		depth = num(t.Shallowest, 2) + "–" + depth
	}
	var did []string
	if t.Lowered > 0 {
		did = append(did, plural(t.Lowered, "sub", "subs")+" weighted down")
	}
	if t.LeftOut > 0 {
		did = append(did, fmt.Sprintf("%d left out", t.LeftOut))
	}
	return fmt.Sprintf("moonlight streaked the dust rims in %s (%s) · %s of the sky · %s",
		esc(t.Object), esc(strings.Join(t.Filters, ", ")), depth, strings.Join(did, ", "))
}

// dustPhotos are the crops of the night's dust changes.
func (f Formatter) dustPhotos(r Report) []Photo {
	var out []Photo
	for _, d := range r.Dust {
		if len(d.Crop) == 0 {
			continue
		}
		out = append(out, Photo{JPEG: d.Crop, Caption: fmt.Sprintf("<b>Dust</b> · %s on the %s at x %d, y %d · before | after | difference",
			dustWhat(d), dustWhere(d), d.X, d.Y)})
	}
	if r.DustLights != nil && len(r.DustLights.Preview) > 0 {
		out = append(out, Photo{JPEG: r.DustLights.Preview, Caption: "<b>Dust</b> · last night's lights over their flats; dark rings are dust the flats miss"})
	}
	return out
}
