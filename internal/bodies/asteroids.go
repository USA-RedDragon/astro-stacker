package bodies

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	// AsteroidQueryURL asks JPL's SBDB for the osculating elements of every
	// asteroid that can get to V 12: H 12.5 or brighter anywhere, and H 16
	// or brighter for those with perihelia inside 1.7 au, which pass close
	// enough to the Earth to.
	AsteroidQueryURL = "https://ssd-api.jpl.nasa.gov/sbdb_query.api?fields=pdes,full_name,name,epoch,e,q,i,om,w,tp,H,G,neo&sb-kind=a&full-prec=1" +
		"&sb-cdata=%7B%22OR%22%3A%5B%22H%7CLE%7C12.5%22%2C%7B%22AND%22%3A%5B%22q%7CLT%7C1.7%22%2C%22H%7CLE%7C16%22%5D%7D%5D%7D"
	// CloseApproachURL is JPL's list of close approaches to the Earth.
	CloseApproachURL = "https://ssd-api.jpl.nasa.gov/cad.api"

	DefaultAsteroidMaxMag = 12.0
	// DefaultG is the IAU slope parameter for asteroids SBDB gives none.
	DefaultG = 0.15
	// NEOPerihelion is the perihelion distance, in au, under which an
	// asteroid's position and magnitude are taken from Horizons.
	NEOPerihelion = 1.3
	// CloseApproachDays is how far either side of tonight close approaches
	// are looked for, and CloseApproachDist how near, in au.
	CloseApproachDays = 10
	CloseApproachDist = 0.2
	// candidateMargin is how much fainter than the limit a two-body
	// estimate of a near-Earth asteroid may be and still be checked.
	candidateMargin = 1.5
	// MaxHorizonsFetches is the most Horizons tables one list fetches;
	// tables already cached don't count.
	MaxHorizonsFetches = 40
	// UnknownTTL is how long a body Horizons does not know is not asked
	// for again.
	UnknownTTL = 24 * time.Hour
	// MaxElementAge is how far from their epoch elements are propagated;
	// older ones are left out of the list.
	MaxElementAge = 2 * 365 * 24 * time.Hour
	// DefaultHorizonsBudget is how long a list request waits on Horizons
	// before answering with the two-body values of those not yet checked.
	DefaultHorizonsBudget = 30 * time.Second
	// AsteroidWarmInterval is how often the list is built in the
	// background, so requests find Horizons' tables cached.
	AsteroidWarmInterval = time.Hour

	SourceTwoBody  = "two-body"
	SourceHorizons = "horizons"

	AsteroidBasis = "Main-belt positions and magnitudes are two-body from JPL SBDB's osculating elements, seen from the Earth's centre: within about 20 arcsec of Horizons for Ceres and Vesta on 2026-10-13, good for the altitude window. Near-Earth asteroids (SBDB's NEO flag or perihelion inside 1.3 au) and bodies on JPL's close-approach list are taken from JPL Horizons for tonight from the site: topocentric position, V is Horizons' APmag, the rate is the fastest while above min_altitude in darkness. V = H + 5 log10(r delta) - 2.5 log10((1-G) phi1 + G phi2) with the IAU H,G phase function (G = 0.15 where SBDB has none) is a model prediction, not a measurement. The position tables the plugin centres on always come from Horizons."
)

// ErrNoAsteroidElements means the asteroid list has no elements to work from.
var ErrNoAsteroidElements = errors.New("the JPL SBDB asteroid elements could not be fetched and none are cached")

// AsteroidElements are an asteroid's heliocentric two-body elements from
// SBDB, with its absolute magnitude H and slope G.
type AsteroidElements struct {
	Elements
	Number   *int
	FullName string
	NEO      bool
	HV       float64
	GV       *float64
	Epoch    time.Time
}

func (a AsteroidElements) slope() float64 {
	if a.GV != nil {
		return *a.GV
	}
	return DefaultG
}

// V is the asteroid's H,G magnitude and phase angle in degrees as seen in
// s at t.
func (a AsteroidElements) V(s Sighting, t time.Time) (mag, phase float64, ok bool) {
	phase = PhaseAngle(s.R, s.Delta, earthSun(t))
	mag, ok = HGMagnitude(a.HV, a.slope(), s.R, s.Delta, phase)
	return mag, phase, ok
}

// nearEarth reports whether the asteroid's position should come from
// Horizons.
func (a AsteroidElements) nearEarth() bool {
	return a.NEO || a.Q < NEOPerihelion
}

func fromJD(jd float64) time.Time {
	return time.Unix(0, int64((jd-2440587.5)*86400e9)).UTC()
}

// asteroidName is how an asteroid is listed: "1 Ceres", "4015 (1979 VA)"
// or "2026 TC6".
func asteroidName(full, name string, number *int, des string) string {
	full = strings.Join(strings.Fields(full), " ")
	switch {
	case number != nil && strings.TrimSpace(name) != "":
		return fmt.Sprintf("%d %s", *number, strings.TrimSpace(name))
	case number != nil && full != "":
		return full
	case number != nil:
		return strconv.Itoa(*number)
	}
	if des != "" {
		return des
	}
	return strings.Trim(full, "()")
}

func numberOf(des string) *int {
	if des == "" || strings.Trim(des, "0123456789") != "" {
		return nil
	}
	n, err := strconv.Atoi(des)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// ParseAsteroids reads an SBDB query of asteroids' pdes, full_name, name,
// epoch, e, q, i, om, w, tp, H, G and neo, skipping rows it can't read.
func ParseAsteroids(body string) ([]AsteroidElements, int, error) {
	var r struct {
		Fields []string    `json:"fields"`
		Data   [][]*string `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, 0, err
	}
	col := map[string]int{}
	for i, f := range r.Fields {
		col[f] = i
	}
	for _, f := range []string{"pdes", "full_name", "name", "epoch", "e", "q", "i", "om", "w", "tp", "H", "G", "neo"} {
		if _, ok := col[f]; !ok {
			return nil, 0, fmt.Errorf("sbdb: no %s field", f)
		}
	}
	out := make([]AsteroidElements, 0, len(r.Data))
	bad := 0
	for _, row := range r.Data {
		get := func(f string) string {
			if i := col[f]; i < len(row) && row[i] != nil {
				return strings.TrimSpace(*row[i])
			}
			return ""
		}
		nums := map[string]float64{}
		var errs []error
		for _, f := range []string{"epoch", "e", "q", "i", "om", "w", "tp", "H"} {
			v, err := strconv.ParseFloat(get(f), 64)
			nums[f] = v
			errs = append(errs, err)
		}
		des := get("pdes")
		if errors.Join(errs...) != nil || des == "" || nums["q"] <= 0 || nums["e"] < 0 || nums["e"] >= 1 || !finite(nums["H"]) {
			bad++
			continue
		}
		a := AsteroidElements{Number: numberOf(des), FullName: strings.Join(strings.Fields(get("full_name")), " "), NEO: get("neo") == "Y",
			HV: nums["H"], Epoch: fromJD(nums["epoch"])}
		a.Designation = des
		a.Name = asteroidName(a.FullName, get("name"), a.Number, des)
		a.OrbitType = "A"
		a.Perihelion = fromJD(nums["tp"])
		a.Q, a.E, a.Incl, a.Node, a.ArgPeri = nums["q"], nums["e"], nums["i"], nums["om"], nums["w"]
		if g, err := strconv.ParseFloat(get("G"), 64); err == nil && finite(g) {
			a.GV = &g
		}
		out = append(out, a)
	}
	return out, bad, nil
}

// CloseApproach is one entry of JPL's close-approach list.
type CloseApproach struct {
	Designation string
	FullName    string
	At          time.Time
	DistMin     float64
	H           float64
}

// ParseCloseApproaches reads a cad.api answer with fullname=true.
func ParseCloseApproaches(body string) ([]CloseApproach, error) {
	var r struct {
		Fields []string        `json:"fields"`
		Data   [][]*string     `json:"data"`
		Count  json.RawMessage `json:"count"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, err
	}
	if len(r.Count) == 0 {
		return nil, errors.New("cad: no count in the answer")
	}
	col := map[string]int{}
	for i, f := range r.Fields {
		col[f] = i
	}
	if len(r.Data) > 0 {
		for _, f := range []string{"des", "jd", "dist_min", "h", "fullname"} {
			if _, ok := col[f]; !ok {
				return nil, fmt.Errorf("cad: no %s field", f)
			}
		}
	}
	out := make([]CloseApproach, 0, len(r.Data))
	for _, row := range r.Data {
		get := func(f string) string {
			if i := col[f]; i < len(row) && row[i] != nil {
				return strings.TrimSpace(*row[i])
			}
			return ""
		}
		jd, err1 := strconv.ParseFloat(get("jd"), 64)
		dist, err2 := strconv.ParseFloat(get("dist_min"), 64)
		h, err3 := strconv.ParseFloat(get("h"), 64)
		if errors.Join(err1, err2, err3) != nil || get("des") == "" || dist <= 0 {
			continue
		}
		out = append(out, CloseApproach{Designation: get("des"), FullName: strings.Join(strings.Fields(get("fullname")), " "), At: fromJD(jd), DistMin: dist, H: h})
	}
	return out, nil
}

// earthSun is the Earth's distance from the Sun, in au.
func earthSun(t time.Time) float64 { return earth(t).norm() }

// PhaseAngle is the Sun-body-Earth angle in degrees, from the body's
// distances to the Sun (r) and the Earth (delta) and the Earth's to the Sun.
func PhaseAngle(r, delta, sunEarth float64) float64 {
	c := (r*r + delta*delta - sunEarth*sunEarth) / (2 * r * delta)
	return math.Acos(math.Max(-1, math.Min(1, c))) / degree
}

// HGMagnitude is the IAU H,G apparent magnitude.
func HGMagnitude(h, g, r, delta, phase float64) (float64, bool) {
	if r <= 0 || delta <= 0 || !finite(phase) {
		return 0, false
	}
	t := math.Tan(phase * degree / 2)
	phi1 := math.Exp(-3.33 * math.Pow(t, 0.63))
	phi2 := math.Exp(-1.87 * math.Pow(t, 1.22))
	f := (1-g)*phi1 + g*phi2
	if f <= 0 {
		return 0, false
	}
	return h + 5*math.Log10(r*delta) - 2.5*math.Log10(f), true
}

// helioFromTopo is the body's distance from the Sun and its phase angle
// from a position seen from the Earth: RA and Dec (ICRF, degrees) and range
// delta (au).
func helioFromTopo(t time.Time, ra, dec, delta float64) (r, phase float64) {
	e := earth(t)
	ce, se := math.Cos(obliquity), math.Sin(obliquity)
	eq := Vec{e[0], ce*e[1] - se*e[2], se*e[1] + ce*e[2]}
	a, d := ra*degree, dec*degree
	g := Vec{delta * math.Cos(d) * math.Cos(a), delta * math.Cos(d) * math.Sin(a), delta * math.Sin(d)}
	b := Vec{eq[0] + g[0], eq[1] + g[1], eq[2] + g[2]}
	r = b.norm()
	return r, PhaseAngle(r, delta, eq.norm())
}

type Asteroid struct {
	Name           string      `json:"name"`
	Designation    string      `json:"designation"`
	Number         *int        `json:"number"`
	FullName       string      `json:"full_name"`
	Kind           string      `json:"kind"`
	Command        string      `json:"horizons_command"`
	H              float64     `json:"h"`
	G              float64     `json:"g"`
	Magnitude      *float64    `json:"magnitude"`
	MagSource      string      `json:"magnitude_source"`
	PositionSource string      `json:"position_source"`
	NEO            bool        `json:"neo"`
	R              float64     `json:"r_au"`
	Delta          float64     `json:"delta_au"`
	Phase          float64     `json:"phase_angle"`
	RA             float64     `json:"ra"`
	Dec            float64     `json:"dec"`
	MotionRate     float64     `json:"rate_arcsec_per_min"`
	Window         CometWindow `json:"window"`
}

type AsteroidSource struct {
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetched_at"`
	Asteroids int        `json:"asteroids"`
	// Stale counts the elements left out for an epoch more than
	// MaxElementAge from tonight.
	Stale int    `json:"stale,omitempty"`
	Error string `json:"error,omitempty"`
}

type HorizonsChecks struct {
	Checked int      `json:"checked"`
	Failed  int      `json:"failed"`
	Pending int      `json:"pending"`
	Errors  []string `json:"errors"`
}

type AsteroidView struct {
	ComputedAt      time.Time      `json:"computed_at"`
	Night           sky.Night      `json:"night"`
	MinAltitude     float64        `json:"min_altitude"`
	MaxMag          float64        `json:"max_magnitude"`
	Elements        AsteroidSource `json:"elements"`
	CloseApproaches AsteroidSource `json:"close_approaches"`
	Horizons        HorizonsChecks `json:"horizons"`
	Basis           string         `json:"basis"`
	Asteroids       []Asteroid     `json:"asteroids"`
}

// AsteroidList lists the asteroids brighter than a limit that are up in
// tonight's darkness. It fetches through Comets' daily cache.
type AsteroidList struct {
	Comets   *CometList
	Horizons *horizons.Client
	URL      string
	CADURL   string
	// Budget is how long a request waits on Horizons; 0 is
	// DefaultHorizonsBudget.
	Budget time.Duration

	mu      sync.Mutex
	parsed  []AsteroidElements
	parseAt time.Time
	unknown map[string]time.Time
}

func (l *AsteroidList) url() string {
	if l.URL == "" {
		return AsteroidQueryURL
	}
	return l.URL
}

func (l *AsteroidList) cadBase() string {
	if l.CADURL == "" {
		return CloseApproachURL
	}
	return l.CADURL
}

func (l *AsteroidList) budget() time.Duration {
	if l.Budget <= 0 {
		return DefaultHorizonsBudget
	}
	return l.Budget
}

func (l *AsteroidList) elements(ctx context.Context) ([]AsteroidElements, AsteroidSource, error) {
	stored, info, err := l.Comets.cached(ctx, l.url(), func(b string) bool { els, _, err := ParseAsteroids(b); return err == nil && len(els) > 0 })
	src := AsteroidSource{Source: info.Source, FetchedAt: info.FetchedAt, Error: info.Error}
	if err != nil {
		return nil, src, fmt.Errorf("%w: %w", ErrNoAsteroidElements, err)
	}
	l.mu.Lock()
	els, at := l.parsed, l.parseAt
	l.mu.Unlock()
	if els == nil || !at.Equal(stored.FetchedAt) {
		var bad int
		els, bad, err = ParseAsteroids(stored.Body)
		if err != nil {
			return nil, src, fmt.Errorf("%w: %w", ErrNoAsteroidElements, err)
		}
		if bad > 0 {
			slog.Debug("Unreadable asteroid element rows", "rows", bad)
		}
		l.mu.Lock()
		l.parsed, l.parseAt = els, stored.FetchedAt
		l.mu.Unlock()
	}
	src.Asteroids = len(els)
	return els, src, nil
}

// cadURL asks for approaches nearer than CloseApproachDist within
// CloseApproachDays of the night's start, on whole days so the source, and
// with it the cached copy, changes once a day.
func (l *AsteroidList) cadURL(night time.Time) string {
	day := night.UTC().Truncate(24 * time.Hour)
	q := url.Values{}
	q.Set("date-min", day.AddDate(0, 0, -CloseApproachDays).Format(time.DateOnly))
	q.Set("date-max", day.AddDate(0, 0, CloseApproachDays).Format(time.DateOnly))
	q.Set("dist-max", strconv.FormatFloat(CloseApproachDist, 'f', -1, 64))
	q.Set("fullname", "true")
	return l.cadBase() + "?" + q.Encode()
}

func (l *AsteroidList) closeApproaches(ctx context.Context, night time.Time) ([]CloseApproach, AsteroidSource) {
	source := l.cadURL(night)
	stored, info, err := l.Comets.cached(ctx, source, func(b string) bool { _, err := ParseCloseApproaches(b); return err == nil })
	src := AsteroidSource{Source: info.Source, FetchedAt: info.FetchedAt, Error: info.Error}
	if err != nil {
		src.Error = err.Error()
		return nil, src
	}
	if l.Comets.DB != nil {
		if err := l.Comets.DB.WithContext(ctx).Where("source LIKE ? AND source <> ?", l.cadBase()+"?%", source).Delete(&app.CometElements{}).Error; err != nil {
			slog.Warn("Dropping older close-approach lists failed", "error", err)
		}
	}
	cas, err := ParseCloseApproaches(stored.Body)
	if err != nil {
		src.Error = err.Error()
		return nil, src
	}
	src.Asteroids = len(cas)
	return cas, src
}

// interpolate is the table's row at t, linear between the rows around it.
func interpolate(rows []horizons.Row, t time.Time) (horizons.Row, bool) {
	i := sort.Search(len(rows), func(i int) bool { return !rows[i].At.Before(t) })
	if i < len(rows) && rows[i].At.Equal(t) {
		return rows[i], true
	}
	if i == 0 || i == len(rows) {
		return horizons.Row{}, false
	}
	a, b := rows[i-1], rows[i]
	span := b.At.Sub(a.At)
	if span <= 0 || span > time.Hour {
		return horizons.Row{}, false
	}
	f := float64(t.Sub(a.At)) / float64(span)
	out := horizons.Row{At: t, RA: math.Mod(a.RA+f*math.Remainder(b.RA-a.RA, 360)+360, 360), Dec: a.Dec + f*(b.Dec-a.Dec),
		RARate: a.RARate + f*(b.RARate-a.RARate), DecRate: a.DecRate + f*(b.DecRate-a.DecRate)}
	lerp := func(x, y *float64) *float64 {
		if x == nil || y == nil {
			return nil
		}
		v := *x + f*(*y-*x)
		return &v
	}
	out.Mag, out.Delta = lerp(a.Mag, b.Mag), lerp(a.Delta, b.Delta)
	return out, true
}

// fromTable fills an asteroid's window, position, magnitude and rate from
// Horizons' table of the night; false when the table does not cover the
// night's darkness.
func fromTable(out *Asteroid, site sky.Site, n sky.Night, rows []horizons.Row, minAlt float64) bool {
	var w CometWindow
	w.PeakAlt = math.Inf(-1)
	var peak, brightest *horizons.Row
	rate := 0.0
	for _, at := range n.Dark {
		r, ok := interpolate(rows, at)
		if !ok {
			continue
		}
		alt := site.Altitude(r.RA, r.Dec, at)
		if alt > w.PeakAlt {
			t, p := at, r
			w.PeakAlt, w.PeakAt, peak = alt, &t, &p
		}
		if alt < minAlt {
			continue
		}
		t := at
		if w.Start == nil {
			w.Start = &t
		}
		end := at.Add(n.Step)
		if n.Dawn != nil && end.After(*n.Dawn) {
			end = *n.Dawn
		}
		w.End = &end
		w.Hours += n.Step.Hours()
		rate = max(rate, math.Hypot(r.RARate, r.DecRate)/60)
		if r.Mag != nil && (brightest == nil || *r.Mag < *brightest.Mag) {
			b := r
			brightest = &b
		}
	}
	if peak == nil {
		return false
	}
	w.PeakAlt = math.Round(w.PeakAlt*10) / 10
	w.Hours = math.Round(w.Hours*100) / 100
	out.Window = w
	at := *peak
	if w.Hours == 0 {
		rate = math.Hypot(peak.RARate, peak.DecRate) / 60
	}
	if brightest == nil {
		brightest = peak
	}
	out.RA, out.Dec = round(at.RA, 4), round(at.Dec, 4)
	out.MotionRate = round(rate, 2)
	out.Magnitude = nil
	if brightest.Mag != nil {
		m := round(*brightest.Mag, 1)
		out.Magnitude = &m
	}
	if at.Delta != nil {
		r, phase := helioFromTopo(at.At, at.RA, at.Dec, *at.Delta)
		out.Delta, out.R, out.Phase = round(*at.Delta, 4), round(r, 4), round(phase, 1)
	}
	out.MagSource, out.PositionSource = SourceHorizons, SourceHorizons
	return true
}

func twoBody(el AsteroidElements, s Sighting, at time.Time) Asteroid {
	a := Asteroid{Name: el.Name, Designation: el.Designation, Number: el.Number, FullName: el.FullName, Kind: KindAsteroid,
		Command: horizons.CommandFor(el.Designation, KindAsteroid), H: el.HV, G: el.slope(), NEO: el.nearEarth(),
		R: round(s.R, 4), Delta: round(s.Delta, 4), RA: round(s.RA, 4), Dec: round(s.Dec, 4),
		MagSource: SourceTwoBody, PositionSource: SourceTwoBody}
	m, phase, ok := el.V(s, at)
	a.Phase = round(phase, 1)
	if ok {
		m = round(m, 1)
		a.Magnitude = &m
	}
	return a
}

type candidate struct {
	asteroid Asteroid
	estimate float64
}

func magnitudeOf(a Asteroid) float64 {
	if a.Magnitude == nil {
		return math.Inf(1)
	}
	return *a.Magnitude
}

// List gives the asteroids no brighter than maxMag that clear minAlt in
// tonight's astronomical darkness, brightest first; all keeps those that
// don't clear it. Near-Earth asteroids are checked with Horizons for as long
// as Budget allows; those not checked in time keep their two-body values.
func (l *AsteroidList) List(ctx context.Context, maxMag float64, minAltitude *float64, all bool) (AsteroidView, error) {
	return l.list(ctx, maxMag, minAltitude, all, l.budget())
}

func (l *AsteroidList) list(ctx context.Context, maxMag float64, minAltitude *float64, all bool, budget time.Duration) (AsteroidView, error) {
	c := l.Comets
	view := AsteroidView{MaxMag: maxMag, Basis: AsteroidBasis, Asteroids: []Asteroid{}, Horizons: HorizonsChecks{Errors: []string{}}}
	if c == nil {
		return view, ErrNoSite
	}
	now := c.now().UTC()
	view.ComputedAt = now
	minAlt := c.MinAlt
	if minAlt <= 0 {
		minAlt = DefaultMinAlt
	}
	if minAltitude != nil {
		minAlt = *minAltitude
	}
	view.MinAltitude = minAlt
	if !finite(maxMag) || !finite(minAlt) || minAlt < -90 || minAlt > 90 {
		return view, ErrBadQuery
	}
	if c.Site == nil {
		return view, ErrNoSite
	}
	site, err := c.Site(ctx)
	if err != nil {
		return view, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	els, info, err := l.elements(ctx)
	view.Elements = info
	if err != nil {
		return view, err
	}
	night := site.Tonight(now, sky.DefaultStep)
	view.Night = night
	mid := now
	if len(night.Dark) > 0 {
		mid = night.Dark[len(night.Dark)/2]
	}
	cands, listed, stale, err := twoBodyCandidates(ctx, els, site, night, mid, minAlt, maxMag)
	if err != nil {
		return view, err
	}
	view.Elements.Stale = stale
	cas, cadInfo := l.closeApproaches(ctx, night.Start)
	view.CloseApproaches = cadInfo
	cands = append(cands, closeCandidates(cas, els, listed, maxMag)...)
	l.check(ctx, site, night, minAlt, cands, &view, budget)
	for _, cd := range cands {
		a := cd.asteroid
		if a.PositionSource == "" || magnitudeOf(a) > maxMag || a.Window.Hours == 0 && !all {
			continue
		}
		view.Asteroids = append(view.Asteroids, a)
	}
	slices.SortStableFunc(view.Asteroids, func(a, b Asteroid) int {
		if c := cmp.Compare(magnitudeOf(a), magnitudeOf(b)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return view, nil
}

// twoBodyCandidates are the asteroids whose two-body magnitude in the
// night's middle is near enough maxMag: within 0.5 for main-belt ones, whose
// magnitude hardly changes in a night, and within candidateMargin for
// near-Earth ones, which Horizons checks. listed holds their designations;
// stale counts the elements too far from their epoch to propagate.
func twoBodyCandidates(ctx context.Context, els []AsteroidElements, site sky.Site, night sky.Night, mid time.Time, minAlt, maxMag float64) ([]candidate, map[string]bool, int, error) {
	var cands []candidate
	listed := map[string]bool{}
	stale := 0
	for _, el := range els {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		if age := mid.Sub(el.Epoch); age > MaxElementAge || age < -MaxElementAge {
			stale++
			continue
		}
		s := el.Seen(mid)
		if !s.Finite() {
			continue
		}
		m, _, ok := el.V(s, mid)
		limit := maxMag + 0.5
		if el.nearEarth() {
			limit = maxMag + candidateMargin
		}
		if !ok || m > limit {
			continue
		}
		w, at := window(site, night, el.Elements, minAlt)
		seen := el.Seen(at)
		rate := motion(el.Elements, at)
		if !seen.Finite() || !finite(rate) {
			continue
		}
		a := twoBody(el, seen, at)
		a.MotionRate = round(rate, 2)
		a.Window = w
		listed[strings.ToUpper(el.Designation)] = true
		cands = append(cands, candidate{asteroid: a, estimate: m})
	}
	return cands, listed, stale, nil
}

// closeCandidates are the close approaches not already listed that could be
// near maxMag: H + 5 log10(distance) at the closest approach, as bright as
// they get, within candidateMargin. They have no position until Horizons
// gives one.
func closeCandidates(cas []CloseApproach, els []AsteroidElements, listed map[string]bool, maxMag float64) []candidate {
	byDes := map[string]AsteroidElements{}
	for _, el := range els {
		byDes[strings.ToUpper(el.Designation)] = el
	}
	var out []candidate
	for _, ca := range cas {
		key := strings.ToUpper(ca.Designation)
		est := ca.H + 5*math.Log10(ca.DistMin)
		if listed[key] || est > maxMag+candidateMargin {
			continue
		}
		listed[key] = true
		a := Asteroid{Designation: ca.Designation, Number: numberOf(ca.Designation), FullName: ca.FullName, Kind: KindAsteroid,
			Command: horizons.CommandFor(ca.Designation, KindAsteroid), H: ca.H, G: DefaultG, NEO: true}
		a.Name = asteroidName(ca.FullName, "", a.Number, ca.Designation)
		if el, ok := byDes[key]; ok {
			a.Name, a.FullName, a.H, a.G = el.Name, el.FullName, el.HV, el.slope()
		}
		out = append(out, candidate{asteroid: a, estimate: est})
	}
	return out
}

// check takes the near-Earth candidates, brightest estimate first, from
// Horizons, until budget runs out or MaxHorizonsChecks are done.
func (l *AsteroidList) check(ctx context.Context, site sky.Site, night sky.Night, minAlt float64, cands []candidate, view *AsteroidView, budget time.Duration) {
	var order []int
	for i, cd := range cands {
		if cd.asteroid.NEO {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return cands[order[a]].estimate < cands[order[b]].estimate })
	if len(order) == 0 {
		return
	}
	if l.Horizons == nil {
		view.Horizons.Pending = len(order)
		view.Horizons.Errors = append(view.Horizons.Errors, "no Horizons client")
		return
	}
	hctx, cancel := ctx, context.CancelFunc(func() {})
	if budget > 0 {
		hctx, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()
	span := Span(site, night.Start)
	fetched := 0
	for _, i := range order {
		cd := &cands[i]
		if fetched >= MaxHorizonsFetches || hctx.Err() != nil {
			view.Horizons.Pending++
			continue
		}
		err := l.knownUnknown(cd.asteroid.Command)
		remembered := err != nil
		if !remembered {
			req := horizons.Request{Command: cd.asteroid.Command, Site: site, Start: span.Start, Stop: span.Stop, Step: CoarseStep}
			var tab horizons.Table
			tab, err = l.Horizons.Ephemeris(hctx, req)
			if err != nil || !tab.Cached {
				fetched++
			}
			if errors.Is(err, horizons.ErrUnknownBody) || errors.Is(err, horizons.ErrAmbiguous) {
				l.forget(cd.asteroid.Command)
			}
			if err == nil && !fromTable(&cd.asteroid, site, night, tab.Rows, minAlt) {
				err = errors.New("the table does not cover tonight's darkness")
			}
		}
		switch {
		case err == nil:
			view.Horizons.Checked++
		case hctx.Err() != nil && ctx.Err() == nil:
			view.Horizons.Pending++
		default:
			view.Horizons.Failed++
			if len(view.Horizons.Errors) < 5 {
				view.Horizons.Errors = append(view.Horizons.Errors, fmt.Sprintf("%s: %v", cd.asteroid.Name, err))
			}
			if !remembered {
				slog.Warn("Horizons check of an asteroid failed", "designation", cd.asteroid.Designation, "error", err)
			}
		}
	}
}

// knownUnknown is the error for a command Horizons did not know within
// UnknownTTL.
func (l *AsteroidList) knownUnknown(command string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.unknown[command]
	if !ok {
		return nil
	}
	if l.Comets.now().After(until) {
		delete(l.unknown, command)
		return nil
	}
	return fmt.Errorf("%w (asked within the last %s)", horizons.ErrUnknownBody, UnknownTTL)
}

func (l *AsteroidList) forget(command string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unknown == nil {
		l.unknown = map[string]time.Time{}
	}
	l.unknown[command] = l.Comets.now().Add(UnknownTTL)
}

// Run builds the list every interval so Horizons' tables of tonight's
// near-Earth asteroids are cached before anyone asks.
func (l *AsteroidList) Run(ctx context.Context, every time.Duration) {
	for {
		if _, err := l.list(ctx, DefaultAsteroidMaxMag, nil, false, 0); err != nil && ctx.Err() == nil {
			slog.Warn("Building the asteroid list failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
