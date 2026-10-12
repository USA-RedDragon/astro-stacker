package bodies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CometElementsURL = "https://www.minorplanetcenter.net/iau/MPCORB/CometEls.txt"
	SBDBQueryURL     = "https://ssd-api.jpl.nasa.gov/sbdb_query.api?fields=pdes,full_name,M1,K1&sb-kind=c"
	ElementsMaxAge   = 24 * time.Hour
	DefaultMaxMag    = 15.0
	DefaultMinAlt    = 30.0
	CometBasis       = "Positions are two-body from the MPC's daily comet elements, seen from the Earth's centre (tens of arcsec from JPL at most; the position tables come from Horizons). Both magnitudes are model estimates, not measurements, and often disagree by several magnitudes: magnitude_mpc is H + 5 log10(delta) + 2.5 G log10(r) from the MPC's H and G; magnitude_jpl is M1 + 5 log10(delta) + K1 log10(r) from JPL's SBDB, the T-mag Horizons gives. A comet is listed when either is no fainter than max_magnitude."
)

var ErrNoElements = errors.New("the MPC comet elements could not be fetched and none are cached")

type CometWindow struct {
	Hours   float64    `json:"hours"`
	Start   *time.Time `json:"start"`
	End     *time.Time `json:"end"`
	PeakAlt float64    `json:"peak_alt"`
	PeakAt  *time.Time `json:"peak_at"`
}

type Comet struct {
	Name        string      `json:"name"`
	Designation string      `json:"designation"`
	Command     string      `json:"horizons_command"`
	OrbitType   string      `json:"orbit_type"`
	Perihelion  time.Time   `json:"perihelion"`
	Q           float64     `json:"q_au"`
	E           float64     `json:"e"`
	MagMPC      *float64    `json:"magnitude_mpc"`
	MagJPL      *float64    `json:"magnitude_jpl"`
	R           float64     `json:"r_au"`
	Delta       float64     `json:"delta_au"`
	RA          float64     `json:"ra"`
	Dec         float64     `json:"dec"`
	MotionRate  float64     `json:"rate_arcsec_per_min"`
	Window      CometWindow `json:"window"`
}

type ElementsInfo struct {
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetched_at"`
	Comets    int        `json:"comets"`
	Error     string     `json:"error,omitempty"`
}

type CometView struct {
	ComputedAt  time.Time    `json:"computed_at"`
	Night       sky.Night    `json:"night"`
	MinAltitude float64      `json:"min_altitude"`
	MaxMag      float64      `json:"max_magnitude"`
	Elements    ElementsInfo `json:"elements"`
	JPLMags     ElementsInfo `json:"jpl_magnitudes"`
	Basis       string       `json:"basis"`
	Comets      []Comet      `json:"comets"`
}

// Brightest is the brighter of the two magnitude estimates.
func (c Comet) Brightest() (float64, bool) {
	switch {
	case c.MagMPC != nil && c.MagJPL != nil:
		return min(*c.MagMPC, *c.MagJPL), true
	case c.MagMPC != nil:
		return *c.MagMPC, true
	case c.MagJPL != nil:
		return *c.MagJPL, true
	}
	return 0, false
}

type CometList struct {
	DB        *gorm.DB
	URL       string
	SBDBURL   string
	UserAgent string
	HTTP      *http.Client
	Site      SiteFunc
	MinAlt    float64
	Now       func() time.Time

	mu      sync.Mutex
	parsed  []Elements
	parseAt time.Time
	jpl     map[string][2]float64
	jplAt   time.Time
}

func (c *CometList) url() string {
	if c.URL == "" {
		return CometElementsURL
	}
	return c.URL
}

func (c *CometList) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *CometList) sbdbURL() string {
	if c.SBDBURL == "" {
		return SBDBQueryURL
	}
	return c.SBDBURL
}

func (c *CometList) fetch(ctx context.Context, source string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", err
	}
	ua := c.UserAgent
	if ua == "" {
		ua = horizons.UserAgent("")
	}
	req.Header.Set("User-Agent", ua)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return string(b), err
}

// cached is the text at source, fetched again once a day and kept in the
// app database; a failed fetch falls back to the stored copy.
func (c *CometList) cached(ctx context.Context, source string, valid func(string) bool) (app.CometElements, ElementsInfo, error) {
	info := ElementsInfo{Source: source}
	var stored app.CometElements
	if c.DB != nil {
		if err := c.DB.WithContext(ctx).Where("source = ?", source).Limit(1).Find(&stored).Error; err != nil {
			return stored, info, err
		}
	}
	if stored.Source == "" || c.now().Sub(stored.FetchedAt) >= ElementsMaxAge {
		body, err := c.fetch(ctx, source)
		if err == nil && !valid(body) {
			err = errors.New("nothing readable in the response")
		}
		switch {
		case err == nil:
			stored = app.CometElements{Source: source, Body: body, FetchedAt: c.now().UTC()}
			if c.DB != nil {
				if err := c.DB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&stored).Error; err != nil {
					slog.Warn("Storing comet data failed", "source", source, "error", err)
				}
			}
		case stored.Source == "":
			return stored, info, err
		default:
			info.Error = err.Error()
			slog.Warn("Fetching comet data failed; using the stored copy", "source", source, "fetched_at", stored.FetchedAt, "error", err)
		}
	}
	at := stored.FetchedAt
	info.FetchedAt = &at
	return stored, info, nil
}

func (c *CometList) elements(ctx context.Context) ([]Elements, ElementsInfo, error) {
	stored, info, err := c.cached(ctx, c.url(), func(b string) bool { els, _ := ParseComets(b); return len(els) > 0 })
	if err != nil {
		return nil, info, fmt.Errorf("%w: %w", ErrNoElements, err)
	}
	if c.parsed == nil || !c.parseAt.Equal(stored.FetchedAt) {
		els, bad := ParseComets(stored.Body)
		if bad > 0 {
			slog.Debug("Unreadable comet element lines", "lines", bad)
		}
		c.parsed, c.parseAt = els, stored.FetchedAt
	}
	info.Comets = len(c.parsed)
	return c.parsed, info, nil
}

// ParseSBDB reads an SBDB query of comets' pdes, full_name, M1 and K1 into
// M1 and K1 by designation.
func ParseSBDB(body string) (map[string][2]float64, error) {
	var r struct {
		Fields []string    `json:"fields"`
		Data   [][]*string `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, f := range r.Fields {
		col[f] = i
	}
	for _, f := range []string{"pdes", "full_name", "M1", "K1"} {
		if _, ok := col[f]; !ok {
			return nil, fmt.Errorf("sbdb: no %s field", f)
		}
	}
	out := map[string][2]float64{}
	for _, row := range r.Data {
		get := func(f string) string {
			if i := col[f]; i < len(row) && row[i] != nil {
				return strings.TrimSpace(*row[i])
			}
			return ""
		}
		m1, err1 := strconv.ParseFloat(get("M1"), 64)
		k1, err2 := strconv.ParseFloat(get("K1"), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		des := get("pdes")
		if des == "" || des[0] < '0' || des[0] > '9' || !strings.ContainsAny(des[1:], "PDI") || strings.Contains(des, " ") {
			des = designationOf(get("full_name"))
		}
		out[des] = [2]float64{m1, k1}
	}
	return out, nil
}

// JPLMagnitude is the total magnitude from JPL's M1 and K1:
// M1 + 5 log10(Delta) + K1 log10(r), Horizons' T-mag.
func JPLMagnitude(m1k1 [2]float64, s Sighting) (float64, bool) {
	if s.R <= 0 || s.Delta <= 0 {
		return 0, false
	}
	return m1k1[0] + 5*math.Log10(s.Delta) + m1k1[1]*math.Log10(s.R), true
}

func (c *CometList) jplMagnitudes(ctx context.Context) (map[string][2]float64, ElementsInfo) {
	stored, info, err := c.cached(ctx, c.sbdbURL(), func(b string) bool { m, err := ParseSBDB(b); return err == nil && len(m) > 0 })
	if err != nil {
		info.Error = err.Error()
		return nil, info
	}
	if c.jpl == nil || !c.jplAt.Equal(stored.FetchedAt) {
		m, err := ParseSBDB(stored.Body)
		if err != nil {
			info.Error = err.Error()
			return nil, info
		}
		c.jpl, c.jplAt = m, stored.FetchedAt
	}
	info.Comets = len(c.jpl)
	return c.jpl, info
}

// window is when the comet clears minAlt in the night's darkness, and the
// time it is highest (the night's middle when it has no darkness).
func window(site sky.Site, n sky.Night, el Elements, minAlt float64) (CometWindow, time.Time) {
	var w CometWindow
	w.PeakAlt = math.Inf(-1)
	peak := n.Start.Add(12 * time.Hour)
	for _, at := range n.Dark {
		s := el.Seen(at)
		alt := site.Altitude(s.RA, s.Dec, at)
		if alt > w.PeakAlt {
			a := at
			w.PeakAlt, w.PeakAt, peak = alt, &a, at
		}
		if alt >= minAlt {
			a := at
			if w.Start == nil {
				w.Start = &a
			}
			end := at.Add(n.Step)
			if n.Dawn != nil && end.After(*n.Dawn) {
				end = *n.Dawn
			}
			w.End = &end
			w.Hours += n.Step.Hours()
		}
	}
	if math.IsInf(w.PeakAlt, -1) {
		w.PeakAlt = 0
	}
	w.PeakAlt = math.Round(w.PeakAlt*10) / 10
	w.Hours = math.Round(w.Hours*100) / 100
	return w, peak
}

// motion is the comet's rate across the sky at t, in arcsec per minute.
func motion(el Elements, t time.Time) float64 {
	a, b := el.Seen(t.Add(-30*time.Minute)), el.Seen(t.Add(30*time.Minute))
	dra := math.Mod(b.RA-a.RA+540, 360) - 180
	return math.Hypot(dra*math.Cos((a.Dec+b.Dec)/2*degree), b.Dec-a.Dec) * 60
}

func (c *CometList) comet(el Elements, jpl map[string][2]float64, s Sighting) Comet {
	cm := Comet{Name: el.Name, Designation: el.Designation, Command: horizons.Command(el.Designation), OrbitType: el.OrbitType,
		Perihelion: el.Perihelion, Q: el.Q, E: el.E, R: round(s.R, 4), Delta: round(s.Delta, 4), RA: round(s.RA, 4), Dec: round(s.Dec, 4)}
	if m, ok := el.Magnitude(s); ok {
		m = round(m, 1)
		cm.MagMPC = &m
	}
	if p, ok := jpl[el.Designation]; ok {
		if m, ok := JPLMagnitude(p, s); ok {
			m = round(m, 1)
			cm.MagJPL = &m
		}
	}
	return cm
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// List gives the comets no fainter than maxMag (by the MPC magnitude
// estimate) that clear minAlt in tonight's astronomical darkness, brightest
// first; all includes those that don't clear it and those with no
// magnitude parameters.
func (c *CometList) List(ctx context.Context, maxMag, minAlt float64, all bool) (CometView, error) {
	now := c.now().UTC()
	if minAlt <= 0 {
		minAlt = c.MinAlt
	}
	if minAlt <= 0 {
		minAlt = DefaultMinAlt
	}
	view := CometView{ComputedAt: now, MinAltitude: minAlt, MaxMag: maxMag, Basis: CometBasis, Comets: []Comet{}}
	if c.Site == nil {
		return view, ErrNoSite
	}
	site, err := c.Site(ctx)
	if err != nil {
		return view, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	els, info, err := c.elements(ctx)
	view.Elements = info
	if err != nil {
		return view, err
	}
	jpl, jplInfo := c.jplMagnitudes(ctx)
	view.JPLMags = jplInfo
	night := site.Tonight(now, sky.DefaultStep)
	view.Night = night
	mid := now
	if len(night.Dark) > 0 {
		mid = night.Dark[len(night.Dark)/2]
	}
	for _, el := range els {
		cm := c.comet(el, jpl, el.Seen(mid))
		if m, ok := cm.Brightest(); !ok && !all || ok && m > maxMag {
			continue
		}
		w, at := window(site, night, el, minAlt)
		if w.Hours == 0 && !all {
			continue
		}
		cm = c.comet(el, jpl, el.Seen(at))
		cm.MotionRate = round(motion(el, at), 2)
		cm.Window = w
		view.Comets = append(view.Comets, cm)
	}
	slices.SortStableFunc(view.Comets, func(a, b Comet) int {
		ma, oka := a.Brightest()
		mb, okb := b.Brightest()
		switch {
		case oka != okb:
			if oka {
				return -1
			}
			return 1
		case ma < mb:
			return -1
		case ma > mb:
			return 1
		}
		return 0
	})
	return view, nil
}
