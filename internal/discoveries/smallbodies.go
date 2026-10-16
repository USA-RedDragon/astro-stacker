package discoveries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	sbIdentURL        = "https://ssd-api.jpl.nasa.gov/sb_ident.api"
	CrossCheckSkyBoT  = "skybot"
	CrossCheckSBIdent = "sbident"
	crossMatchArcsec  = 60.0
	crossCheckTimeout = 10 * time.Minute
)

// SmallBodies is the local astro-small-bodies service, which computes the
// known asteroids and comets in a field from mirrored MPC and JPL orbits.
// With no URL the known bodies come from IMCCE's SkyBoT.
type SmallBodies struct {
	URL string
	// Topocentric sends the observatory's site so positions include the
	// parallax; Site gives it (latitude, east longitude in degrees,
	// height in metres). The site is never sent to a public service.
	Topocentric bool
	Site        func(ctx context.Context) (lat, lon, height float64, ok bool)
	// CrossCheck lists public services (skybot, sbident) asked as well,
	// geocentrically, whose differences from the local list are logged.
	CrossCheck []string
	SBIdentURL string
	// crossChecked, when set, is called when a background cross-check ends.
	crossChecked func()
}

type smallBodiesResponse struct {
	Observer string `json:"observer"`
	Orbits   string `json:"orbits"`
	Bodies   []struct {
		Number string   `json:"number"`
		Name   string   `json:"name"`
		Class  string   `json:"class"`
		Kind   string   `json:"kind"`
		RA     float64  `json:"ra"`
		Dec    float64  `json:"dec"`
		V      *float64 `json:"v"`
		DRA    *float64 `json:"dra"`
		DDec   *float64 `json:"ddec"`
	} `json:"bodies"`
}

// KnownBodies returns the known asteroids and comets within radius degrees
// at epoch: from the local service when it is set and holds the night,
// else from SkyBoT.
func (c *Catalogs) KnownBodies(ctx context.Context, ra, dec, radius float64, epoch time.Time) ([]Body, error) {
	sb := c.Local
	if sb.URL == "" {
		return c.SkyBoT(ctx, ra, dec, radius, epoch)
	}
	topo := sb.Topocentric && sb.Site != nil
	bodies, err := c.smallBodies(ctx, ra, dec, radius, epoch, topo)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest && strings.Contains(se.Body, "outside the precomputed range") {
		slog.Info("Known bodies from SkyBoT: the night is outside the local service's range", "epoch", epoch.Format(time.DateOnly))
		return c.SkyBoT(ctx, ra, dec, radius, epoch)
	}
	if err != nil {
		return nil, err
	}
	if len(sb.CrossCheck) > 0 {
		geo := bodies
		if topo {
			if geo, err = c.smallBodies(ctx, ra, dec, radius, epoch, false); err != nil {
				slog.Warn("Known bodies cross-check skipped: no geocentric list", "error", err)
				return bodies, nil
			}
		}
		go c.crossCheck(context.WithoutCancel(ctx), geo, ra, dec, radius, epoch)
	}
	return bodies, nil
}

func (c *Catalogs) smallBodies(ctx context.Context, ra, dec, radius float64, epoch time.Time, topo bool) ([]Body, error) {
	q := url.Values{}
	q.Set("ra", strconv.FormatFloat(ra, 'f', 6, 64))
	q.Set("dec", strconv.FormatFloat(dec, 'f', 6, 64))
	q.Set("radius", strconv.FormatFloat(radius, 'f', 4, 64))
	q.Set("epoch", strconv.FormatFloat(julian(epoch), 'f', 6, 64))
	if topo {
		lat, lon, h, ok := c.Local.Site(ctx)
		if ok {
			q.Set("site", fmt.Sprintf("%.6f,%.6f,%.1f", lat, lon, h))
		}
	}
	b, err := c.get(ctx, strings.TrimRight(c.Local.URL, "/")+"/v1/cone?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("small bodies: %w", withoutQuery(err))
	}
	return ParseSmallBodies(b)
}

// withoutQuery drops the query from a transport error's URL, which holds
// the site when positions are topocentric, so it reaches no log or run.
func withoutQuery(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			u.RawQuery = ""
			return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
		}
		return &url.Error{Op: ue.Op, URL: "(small bodies)", Err: ue.Err}
	}
	return err
}

// ParseSmallBodies reads the local service's JSON; planets are left out.
// A body with no magnitude parameters gets V 0, as ParseSkyBoT gives one
// without Mv.
func ParseSmallBodies(b []byte) ([]Body, error) {
	var resp smallBodiesResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("small bodies: %w", err)
	}
	if resp.Bodies == nil && resp.Orbits == "" {
		return nil, errors.New("small bodies: unexpected response")
	}
	out := make([]Body, 0, len(resp.Bodies))
	for _, r := range resp.Bodies {
		if r.Kind == "planet" {
			continue
		}
		bd := Body{Number: r.Number, Name: r.Name, Class: r.Class, RA: r.RA, Dec: r.Dec}
		if r.V != nil {
			bd.V = *r.V
		}
		if r.DRA != nil {
			bd.DRA = *r.DRA
		}
		if r.DDec != nil {
			bd.DDec = *r.DDec
		}
		out = append(out, bd)
	}
	return out, nil
}

// SBIdent returns the asteroids and comets JPL's sb_ident finds within
// radius degrees at epoch, as seen from the geocentre.
func (c *Catalogs) SBIdent(ctx context.Context, ra, dec, radius float64, epoch time.Time) ([]Body, error) {
	q := url.Values{}
	q.Set("mpc-code", "500")
	q.Set("obs-time", strconv.FormatFloat(julian(epoch), 'f', 6, 64))
	q.Set("fov-ra-center", sexaHMS(ra/15))
	q.Set("fov-dec-center", sexaDMS(dec))
	q.Set("fov-ra-hwidth", strconv.FormatFloat(radius/math.Max(math.Cos(dec*deg), 0.05), 'f', 4, 64))
	q.Set("fov-dec-hwidth", strconv.FormatFloat(radius, 'f', 4, 64))
	q.Set("two-pass", "true")
	q.Set("suppress-first-pass", "true")
	q.Set("mag-required", "false")
	b, err := c.get(ctx, or(c.Local.SBIdentURL, sbIdentURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("sb_ident: %w", err)
	}
	bodies, err := ParseSBIdent(b)
	if err != nil {
		return nil, err
	}
	out := bodies[:0]
	for _, bd := range bodies {
		if Separation(ra, dec, bd.RA, bd.Dec) <= radius {
			out = append(out, bd)
		}
	}
	return out, nil
}

// ParseSBIdent reads sb_ident's second-pass (n-body) table.
func ParseSBIdent(b []byte) ([]Body, error) {
	var resp struct {
		Fields []string   `json:"fields_second"`
		Data   [][]string `json:"data_second_pass"`
		Count  *int       `json:"n_second_pass"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("sb_ident: %w", err)
	}
	if resp.Count == nil {
		return nil, errors.New("sb_ident: unexpected response")
	}
	col := func(prefix string) int {
		return slices.IndexFunc(resp.Fields, func(f string) bool { return strings.HasPrefix(f, prefix) })
	}
	iName, iRA, iDec, iV := col("Object name"), col("Astrometric RA"), col("Astrometric Dec"), col("Visual magnitude")
	iDRA, iDDec := col("RA rate"), col("Dec rate")
	if iName < 0 || iRA < 0 || iDec < 0 {
		return nil, errors.New("sb_ident: missing columns")
	}
	out := make([]Body, 0, len(resp.Data))
	for _, row := range resp.Data {
		ra, ok1 := sexagesimal(strings.NewReplacer(":", " ").Replace(row[iRA]), 15)
		dec, ok2 := sexagesimal(strings.NewReplacer("'", " ", "\"", "").Replace(row[iDec]), 1)
		if !ok1 || !ok2 {
			continue
		}
		bd := Body{RA: ra, Dec: dec, V: math.NaN()}
		bd.Number, bd.Name = splitSBIdentName(row[iName])
		pick := func(i int) float64 {
			if i < 0 || i >= len(row) {
				return math.NaN()
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)
			if err != nil {
				return math.NaN()
			}
			return v
		}
		bd.V, bd.DRA, bd.DDec = pick(iV), pick(iDRA), pick(iDDec)
		out = append(out, bd)
	}
	return out, nil
}

// splitSBIdentName splits "433 Eros (A898 PA)" or "64676 (2001 XE68)" into
// number and name, as SkyBoT and the local service give them.
func splitSBIdentName(s string) (string, string) {
	s = strings.TrimSpace(s)
	head, rest, _ := strings.Cut(s, " ")
	if _, err := strconv.Atoi(head); err != nil || rest == "" {
		return "", s
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "(") {
		return head, strings.Trim(rest, "()")
	}
	name, _, _ := strings.Cut(rest, " (")
	return head, name
}

func sexaHMS(h float64) string {
	return sexa(math.Mod(h+24, 24))
}

// sexaDMS writes a declination as sb_ident wants it: dd-mm-ss.ss, M for minus.
func sexaDMS(v float64) string {
	if v < 0 {
		return "M" + sexa(-v)
	}
	return sexa(v)
}

func sexa(v float64) string {
	cs := int64(math.Round(v * 360000))
	d, m, s := cs/360000, cs/6000%60, float64(cs%6000)/100
	return fmt.Sprintf("%02d-%02d-%05.2f", d, m, s)
}

// bodyKey identifies a body across services: its number, else its name.
func bodyKey(b Body) string {
	if b.Number != "" {
		return "#" + b.Number
	}
	return strings.ToUpper(strings.Join(strings.Fields(strings.Split(b.Name, " (")[0]), " "))
}

// CrossCheckResult is how another service's list differs from the local one.
type CrossCheckResult struct {
	Service   string
	Local     int
	Other     int
	Matched   int
	MissingV  []string
	Missing   []string
	Extra     []string
	MedianArc float64
	MaxArc    float64
}

// CompareBodies matches two lists by number or designation, then by
// position for the rest.
func CompareBodies(service string, local, other []Body) CrossCheckResult {
	res := CrossCheckResult{Service: service, Local: len(local), Other: len(other)}
	byKey := make(map[string]int, len(local))
	for i, b := range local {
		byKey[bodyKey(b)] = i
	}
	used := make([]bool, len(local))
	var seps []float64
	for _, o := range other {
		i, ok := byKey[bodyKey(o)]
		if !ok || used[i] {
			i = -1
			best := crossMatchArcsec
			for j, l := range local {
				if used[j] {
					continue
				}
				if s := Separation(o.RA, o.Dec, l.RA, l.Dec) * 3600; s < best {
					best, i = s, j
				}
			}
		}
		if i < 0 {
			res.Missing = append(res.Missing, bodyName(o))
			if !math.IsNaN(o.V) {
				res.MissingV = append(res.MissingV, fmt.Sprintf("%s V%.1f", bodyName(o), o.V))
			}
			continue
		}
		used[i] = true
		res.Matched++
		seps = append(seps, Separation(o.RA, o.Dec, local[i].RA, local[i].Dec)*3600)
	}
	for i, b := range local {
		if !used[i] {
			res.Extra = append(res.Extra, bodyName(b))
		}
	}
	if len(seps) > 0 {
		slices.Sort(seps)
		res.MedianArc = seps[len(seps)/2]
		res.MaxArc = seps[len(seps)-1]
	}
	return res
}

// crossCheck runs in the background: sb_ident can take minutes, and a
// cross-check must not hold up the run.
func (c *Catalogs) crossCheck(parent context.Context, local []Body, ra, dec, radius float64, epoch time.Time) {
	ctx, cancel := context.WithTimeout(parent, crossCheckTimeout)
	defer cancel()
	if c.Local.crossChecked != nil {
		defer c.Local.crossChecked()
	}
	for _, svc := range c.Local.CrossCheck {
		var other []Body
		var err error
		switch svc {
		case CrossCheckSkyBoT:
			other, err = c.SkyBoT(ctx, ra, dec, radius, epoch)
		case CrossCheckSBIdent:
			other, err = c.SBIdent(ctx, ra, dec, radius, epoch)
		default:
			continue
		}
		if err != nil {
			slog.Warn("Known bodies cross-check failed", "service", svc, "error", err)
			continue
		}
		r := CompareBodies(svc, local, other)
		slog.Info("Known bodies cross-check", "service", svc, "epoch", epoch.UTC().Format(time.RFC3339),
			"ra", ra, "dec", dec, "radius", radius, "local", r.Local, "other", r.Other, "matched", r.Matched,
			"median_arcsec", r.MedianArc, "max_arcsec", r.MaxArc, "missing", len(r.Missing), "extra", len(r.Extra),
			"missing_with_v", strings.Join(r.MissingV, "; "))
	}
}
