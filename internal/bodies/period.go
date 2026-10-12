package bodies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// SBDBURL is JPL's small-body database lookup of one body.
const SBDBURL = "https://ssd-api.jpl.nasa.gov/sbdb.api"

// ErrNoOrbit means no orbit is known for the body.
var ErrNoOrbit = errors.New("no orbit is known for the body")

// Orbit is the shape of a body's orbit, enough to say how often it comes
// back.
type Orbit struct {
	Designation string   `json:"designation"`
	E           float64  `json:"e"`
	PeriodYears *float64 `json:"period_years"`
	Source      string   `json:"source"`
}

// OrbitOf is the orbit the MPC elements give: the period from Kepler's third
// law for a closed orbit, none for a parabolic or hyperbolic one.
func OrbitOf(el Elements, source string) Orbit {
	o := Orbit{Designation: el.Designation, E: el.E, Source: source}
	if el.E < 1 && el.Q > 0 {
		a := el.Q / (1 - el.E)
		p := math.Pow(a, 1.5)
		o.PeriodYears = &p
	}
	return o
}

// SameDesignation compares designations ignoring case, spaces and leading
// zeros of a periodic number.
func SameDesignation(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToUpper(strings.Join(strings.Fields(s), ""))
		if i := strings.IndexByte(s, '/'); i > 1 && s[0] >= '0' && s[0] <= '9' {
			s = s[:i]
		}
		return strings.TrimLeft(s, "0")
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// ElementsOf finds a comet's elements in the stored MPC elements.
func (c *CometList) ElementsOf(ctx context.Context, designation string) (Elements, bool) {
	els, _, err := c.elements(ctx)
	if err != nil {
		return Elements{}, false
	}
	for _, el := range els {
		if SameDesignation(el.Designation, designation) {
			return el, true
		}
	}
	return Elements{}, false
}

// Orbit finds a comet in the stored MPC elements.
func (c *CometList) Orbit(ctx context.Context, designation string) (Orbit, error) {
	els, _, err := c.elements(ctx)
	if err != nil {
		return Orbit{}, err
	}
	for _, el := range els {
		if SameDesignation(el.Designation, designation) {
			return OrbitOf(el, "MPC comet elements"), nil
		}
	}
	return Orbit{}, fmt.Errorf("%w: %s is not in the MPC comet elements", ErrNoOrbit, designation)
}

type sbdbOrbit struct {
	Orbit struct {
		Elements []struct {
			Name  string  `json:"name"`
			Value *string `json:"value"`
		} `json:"elements"`
	} `json:"orbit"`
	Message string `json:"message"`
}

// ParseSBDBOrbit reads the eccentricity and period from an SBDB lookup.
func ParseSBDBOrbit(designation, body string) (Orbit, error) {
	var r sbdbOrbit
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return Orbit{}, err
	}
	o := Orbit{Designation: designation, E: math.NaN(), Source: "JPL SBDB"}
	for _, el := range r.Orbit.Elements {
		if el.Value == nil {
			continue
		}
		v, err := strconv.ParseFloat(*el.Value, 64)
		if err != nil {
			continue
		}
		switch el.Name {
		case "e":
			o.E = v
		case "per":
			p := v / 365.25
			o.PeriodYears = &p
		}
	}
	if math.IsNaN(o.E) {
		msg := r.Message
		if msg == "" {
			msg = "no eccentricity in the answer"
		}
		return Orbit{}, fmt.Errorf("%w: %s: %s", ErrNoOrbit, designation, msg)
	}
	if o.E >= 1 {
		o.PeriodYears = nil
	}
	return o, nil
}

// SBDBOrbit asks JPL's small-body database for a body's orbit.
func (c *CometList) SBDBOrbit(ctx context.Context, designation string) (Orbit, error) {
	body, err := c.fetch(ctx, SBDBURL+"?sstr="+url.QueryEscape(designation))
	if err != nil {
		return Orbit{}, fmt.Errorf("SBDB %s: %w", designation, err)
	}
	return ParseSBDBOrbit(designation, body)
}

// FindOrbit is a comet's orbit from the MPC elements, else from SBDB, and an
// asteroid's from SBDB.
func (c *CometList) FindOrbit(ctx context.Context, designation, kind string) (Orbit, error) {
	if kind != KindAsteroid {
		if o, err := c.Orbit(ctx, designation); err == nil {
			return o, nil
		}
	}
	return c.SBDBOrbit(ctx, designation)
}
