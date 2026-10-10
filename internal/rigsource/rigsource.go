package rigsource

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	SourceHeaders = "fits-headers"
	SourceNone    = "none"

	Window          = 60 * 24 * time.Hour
	fallbackLights  = 500
	minFilterFrames = 3
	maxTSRows       = 3000
	defaultTTL      = 10 * time.Minute

	ticksThreshold  = 600000000000000000
	ticksUnixOffset = 621355968000000000
	ticksPerSecond  = 10000000
)

type Basis struct {
	Source      string      `json:"source"`
	Frames      int         `json:"frames"`
	From        *time.Time  `json:"from"`
	To          *time.Time  `json:"to"`
	Camera      *string     `json:"camera"`
	Telescope   *string     `json:"telescope"`
	GuideFrames int         `json:"guideFrames"`
	HFRFrames   int         `json:"hfrFrames"`
	HFRSource   *string     `json:"hfrSource"`
	Filters     []FilterUse `json:"filters"`
}

type FilterUse struct {
	Filter string     `json:"filter"`
	Frames int        `json:"frames"`
	Last   *time.Time `json:"last"`
}

type Rig struct {
	FocalLength     *float64           `json:"focalLength"`
	PixelSize       *float64           `json:"pixelSize"`
	WidthPx         *int               `json:"widthPx"`
	HeightPx        *int               `json:"heightPx"`
	Scale           *float64           `json:"scale"`
	WidthDeg        *float64           `json:"widthDeg"`
	HeightDeg       *float64           `json:"heightDeg"`
	Colour          *bool              `json:"colour"`
	Filters         []string           `json:"filters"`
	TypicalHFR      *float64           `json:"typicalHfr"`
	TypicalGuideRMS *float64           `json:"typicalGuideRms"`
	Exposures       map[string]float64 `json:"exposures"`
	Basis           Basis              `json:"basis"`
}

func (r Rig) Known() bool {
	return r.FocalLength != nil && r.PixelSize != nil && r.WidthPx != nil && r.HeightPx != nil
}

func (r Rig) Mosaic() (mosaics.Rig, bool) {
	if r.WidthDeg == nil || r.HeightDeg == nil || r.Scale == nil {
		return mosaics.Rig{}, false
	}
	return mosaics.Rig{WidthDeg: *r.WidthDeg, HeightDeg: *r.HeightDeg, ScaleArcsec: *r.Scale}, true
}

var ErrUnknown = errors.New("the rig is not known yet: no lights with FOCALLEN, XPIXSZ and image size have been indexed")

func MosaicRig(ctx context.Context, appDB *gorm.DB) (mosaics.Rig, error) {
	r, err := Measure(ctx, appDB, nil, time.Now())
	if err != nil {
		return mosaics.Rig{}, err
	}
	m, ok := r.Mosaic()
	if !ok {
		return m, ErrUnknown
	}
	return m, nil
}

func Empty() Rig {
	return Rig{Filters: []string{}, Exposures: map[string]float64{}, Basis: Basis{Source: SourceNone, Filters: []FilterUse{}}}
}

func CanonicalFilter(name string) string {
	n := strings.ToUpper(strings.TrimSpace(name))
	n = strings.NewReplacer("-", "", " ", "", "_", "").Replace(n)
	switch {
	case strings.HasPrefix(n, "HA") || strings.HasPrefix(n, "HALPHA") || n == "H":
		return "H"
	case strings.HasPrefix(n, "OIII") || n == "O3" || n == "O":
		return "O"
	case strings.HasPrefix(n, "SII") || n == "S2" || n == "S":
		return "S"
	case n == "L" || n == "LUM" || n == "LUMINANCE":
		return "L"
	case strings.HasPrefix(n, "R"):
		return "R"
	case strings.HasPrefix(n, "G"):
		return "G"
	case strings.HasPrefix(n, "B"):
		return "B"
	}
	return n
}

func FilterOrder() []string { return []string{"L", "R", "G", "B", "H", "O", "S"} }

type lightRow struct {
	DateObs      *time.Time
	Filter       string
	Exposure     *float64
	Camera       string
	Width        *int
	Height       *int
	FocalLength  *float64
	PixelSize    *float64
	Telescope    *string
	BayerPattern *string
	GeometryRev  *int
	StarHFR      *float64
}

func lights(ctx context.Context, db *gorm.DB, now time.Time) ([]lightRow, error) {
	cols := "date_obs, filter, exposure, camera, width, height, focal_length, pixel_size, telescope, bayer_pattern, geometry_rev, star_hfr"
	var rows []lightRow
	if err := db.WithContext(ctx).Model(&app.Frame{}).Select(cols).
		Where("type = ? AND index_error IS NULL AND date_obs >= ?", "LIGHT", now.Add(-Window)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		return rows, nil
	}
	err := db.WithContext(ctx).Model(&app.Frame{}).Select(cols).
		Where("type = ? AND index_error IS NULL AND date_obs IS NOT NULL", "LIGHT").Order("date_obs DESC").Limit(fallbackLights).Scan(&rows).Error
	return rows, err
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func mode[K comparable](counts map[K]int) (K, int) {
	var best K
	n := 0
	for k, c := range counts {
		if c > n {
			best, n = k, c
		}
	}
	return best, n
}

type tally struct {
	sizes        map[[2]int]int
	scopes       map[string]int
	focal, pixel []float64
	geometryRead int
	bayer        int
	byFilter     map[string][]float64
	filterCount  map[string]int
}

func (t *tally) add(r lightRow, b *Basis) {
	if r.DateObs != nil {
		d := *r.DateObs
		if b.From == nil || d.Before(*b.From) {
			b.From = &d
		}
		if b.To == nil || d.After(*b.To) {
			b.To = &d
		}
	}
	if r.Width != nil && r.Height != nil {
		t.sizes[[2]int{*r.Width, *r.Height}]++
	}
	if r.FocalLength != nil && *r.FocalLength > 0 {
		t.focal = append(t.focal, *r.FocalLength)
	}
	if r.PixelSize != nil && *r.PixelSize > 0 {
		t.pixel = append(t.pixel, *r.PixelSize)
	}
	if r.Telescope != nil && *r.Telescope != "" {
		t.scopes[*r.Telescope]++
	}
	if r.GeometryRev != nil {
		t.geometryRead++
		if r.BayerPattern != nil && *r.BayerPattern != "" {
			t.bayer++
		}
	}
	if f := CanonicalFilter(r.Filter); f != "" {
		t.filterCount[f]++
		if r.Exposure != nil && *r.Exposure > 0 {
			t.byFilter[f] = append(t.byFilter[f], *r.Exposure)
		}
	}
}

func (t *tally) apply(out *Rig) {
	if sz, n := mode(t.sizes); n > 0 {
		w, h := sz[0], sz[1]
		out.WidthPx, out.HeightPx = &w, &h
	}
	if len(t.focal) > 0 {
		v := median(t.focal)
		out.FocalLength = &v
	}
	if len(t.pixel) > 0 {
		v := median(t.pixel)
		out.PixelSize = &v
	}
	if sc, n := mode(t.scopes); n > 0 {
		out.Basis.Telescope = &sc
	}
	if t.geometryRead > 0 {
		c := t.bayer*2 > t.geometryRead
		out.Colour = &c
	}
	for f, exps := range t.byFilter {
		if t.filterCount[f] >= minFilterFrames {
			out.Exposures[f] = median(exps)
		}
	}
	if out.FocalLength != nil && out.PixelSize != nil {
		scale := 206.264806 * *out.PixelSize / *out.FocalLength
		out.Scale = &scale
		if out.WidthPx != nil {
			w, h := scale*float64(*out.WidthPx)/3600, scale*float64(*out.HeightPx)/3600
			out.WidthDeg, out.HeightDeg = &w, &h
		}
	}
}

func Measure(ctx context.Context, appDB, sched *gorm.DB, now time.Time) (Rig, error) {
	out := Empty()
	if appDB == nil || !appDB.Migrator().HasTable(&app.Frame{}) {
		return out, nil
	}
	rows, err := lights(ctx, appDB, now)
	if err != nil || len(rows) == 0 {
		return out, err
	}
	cams := map[string]int{}
	for _, r := range rows {
		cams[r.Camera]++
	}
	camera, _ := mode(cams)
	t := tally{sizes: map[[2]int]int{}, scopes: map[string]int{}, byFilter: map[string][]float64{}, filterCount: map[string]int{}}
	var used []lightRow
	for _, r := range rows {
		if r.Camera == camera {
			used = append(used, r)
			t.add(r, &out.Basis)
		}
	}
	out.Basis.Source, out.Basis.Frames = SourceHeaders, len(used)
	if camera != "" {
		c := camera
		out.Basis.Camera = &c
	}
	t.apply(&out)
	if err := filterUse(ctx, appDB, camera, &out); err != nil {
		return out, err
	}
	measureGuiding(ctx, sched, now, &out, used)
	return out, nil
}

type filterRow struct {
	Filter   string
	Exposure *float64
	Frames   int
	Last     *string
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func filterUse(ctx context.Context, db *gorm.DB, camera string, out *Rig) error {
	var rows []filterRow
	if err := db.WithContext(ctx).Model(&app.Frame{}).
		Select("filter, exposure, COUNT(*) AS frames, MAX(date_obs) AS last").
		Where("type = ? AND index_error IS NULL AND camera = ?", "LIGHT", camera).
		Group("filter, exposure").Scan(&rows).Error; err != nil {
		return err
	}
	frames := map[string]int{}
	last := map[string]time.Time{}
	exps := map[string]map[float64]int{}
	for _, r := range rows {
		f := CanonicalFilter(r.Filter)
		if f == "" {
			continue
		}
		frames[f] += r.Frames
		if r.Last != nil {
			if t, ok := parseTime(*r.Last); ok && t.After(last[f]) {
				last[f] = t
			}
		}
		if r.Exposure != nil && *r.Exposure > 0 {
			if exps[f] == nil {
				exps[f] = map[float64]int{}
			}
			exps[f][*r.Exposure] += r.Frames
		}
	}
	order := FilterOrder()
	for f := range frames {
		if !slices.Contains(order, f) {
			order = append(order, f)
		}
	}
	for _, f := range order {
		if frames[f] < minFilterFrames {
			continue
		}
		out.Filters = append(out.Filters, f)
		use := FilterUse{Filter: f, Frames: frames[f]}
		if l, ok := last[f]; ok && !l.IsZero() {
			use.Last = &l
		}
		out.Basis.Filters = append(out.Basis.Filters, use)
		if _, ok := out.Exposures[f]; !ok && len(exps[f]) > 0 {
			out.Exposures[f] = weightedMedian(exps[f])
		}
	}
	return nil
}

func weightedMedian(counts map[float64]int) float64 {
	keys := make([]float64, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var v []float64
	for _, k := range keys {
		for range counts[k] {
			v = append(v, k)
		}
	}
	return median(v)
}

func measureGuiding(ctx context.Context, sched *gorm.DB, now time.Time, out *Rig, used []lightRow) {
	var hfr, rms []float64
	if sched != nil && sched.Migrator().HasTable("acquiredimage") {
		since := now.Add(-Window)
		if out.Basis.From != nil && out.Basis.From.Before(since) {
			since = *out.Basis.From
		}
		var metas []string
		if err := sched.WithContext(ctx).Table("acquiredimage").Where("metadata IS NOT NULL").
			Where(`CAST(acquireddate AS BIGINT) >= ? AND (CAST(acquireddate AS BIGINT) < ? OR CAST(acquireddate AS BIGINT) >= ?)`,
				since.Unix(), int64(ticksThreshold), since.Unix()*ticksPerSecond+ticksUnixOffset).
			Order("acquireddate DESC").Limit(maxTSRows).Pluck("metadata", &metas).Error; err == nil {
			for _, raw := range metas {
				var m quality.Metadata
				if json.Unmarshal([]byte(raw), &m) != nil {
					continue
				}
				if v := float64(m.HFR); v > 0 && !math.IsInf(v, 0) {
					hfr = append(hfr, v)
				}
				if v := float64(m.GuidingRMSArcSec); v > 0 && !math.IsInf(v, 0) {
					rms = append(rms, v)
				}
			}
		}
	}
	src := "target-scheduler"
	if len(hfr) == 0 {
		src = "stacker"
		for _, r := range used {
			if r.StarHFR != nil && *r.StarHFR > 0 {
				hfr = append(hfr, *r.StarHFR)
			}
		}
	}
	if len(rms) > 0 {
		v := median(rms)
		out.TypicalGuideRMS, out.Basis.GuideFrames = &v, len(rms)
	}
	if len(hfr) > 0 && out.Scale != nil {
		v := median(hfr) * *out.Scale
		out.TypicalHFR, out.Basis.HFRFrames, out.Basis.HFRSource = &v, len(hfr), &src
	}
}

type Source struct {
	App   *gorm.DB
	Sched *gorm.DB
	TTL   time.Duration
	Now   func() time.Time

	mu  sync.Mutex
	at  time.Time
	rig *Rig
}

func (s *Source) Get(ctx context.Context) Rig {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rig != nil && now.Sub(s.at) < ttl {
		return *s.rig
	}
	r, err := Measure(ctx, s.App, s.Sched, now)
	if err != nil {
		if s.rig != nil {
			return *s.rig
		}
		return Empty()
	}
	s.rig, s.at = &r, now
	return r
}
