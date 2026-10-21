// Package claimswatch looks once a week for the user's confirmed stars in
// the places others list or publish variable stars, so a later claim on one
// of them is seen and dated against the user's own record.
package claimswatch

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Star is a confirmed star as the places are searched for it.
type Star struct {
	SourceID int
	Name     string
	GaiaID   int64
	TwoMASS  string
	RA, Dec  float64
}

// GaiaName is "Gaia DR3 <id>", or "" when the id is not known.
func (s Star) GaiaName() string {
	if s.GaiaID == 0 {
		return ""
	}
	return "Gaia DR3 " + strconv.FormatInt(s.GaiaID, 10)
}

// Mention is something a place lists about a star. Key tells it apart from
// the place's other mentions of the same star across checks.
type Mention struct {
	Key    string
	Title  string
	Detail string
	URL    string
	Dated  *time.Time
	Own    bool
}

// Outcome is what one check of a place found for each star. A star in
// Failed could not be checked; a star in neither map had nothing.
type Outcome struct {
	Found  map[int][]Mention
	Failed map[int]string
}

func newOutcome() Outcome { return Outcome{Found: map[int][]Mention{}, Failed: map[int]string{}} }

// State reads and writes what a place keeps between checks.
type State interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Place is one outside source the watch searches.
type Place interface {
	Where() string
	// Check searches for every star; an error means the place could not be
	// reached at all. Off is a plain-words reason the place is not checked.
	Check(ctx context.Context, stars []Star, st State) (Outcome, error)
	Off() string
}

// Places and their labels, in the order the pages show them.
const (
	WhereVSX    = "vsx"
	WhereTNS    = "tns"
	WhereSIMBAD = "simbad"
	WhereADS    = "ads"
	WhereArXiv  = "arxiv"
	WhereAlerts = "alerts"
	WhereVizieR = "vizier"
)

// PlaceInfo is a place's label and what the watch looks for there.
type PlaceInfo struct {
	Where    string `json:"where"`
	Label    string `json:"label"`
	LooksFor string `json:"looks_for"`
}

// Catalogue is every place in display order.
func Catalogue() []PlaceInfo {
	return []PlaceInfo{
		{WhereVSX, "VSX", `any entry within 10"`},
		{WhereTNS, "TNS", `transient reports within 10"`},
		{WhereSIMBAD, "SIMBAD", `object and its papers within 5"`},
		{WhereADS, "ADS", "papers naming the star, full text"},
		{WhereArXiv, "arXiv", "preprints naming the star"},
		{WhereAlerts, "ATels and Gaia alerts", `reports within 10"`},
		{WhereVizieR, "VizieR", `new variable star tables with a star within 5"`},
	}
}

// Label is a place's name on the pages.
func Label(where string) string {
	for _, p := range Catalogue() {
		if p.Where == where {
			return p.Label
		}
	}
	return where
}

const userAgent = "astro-stacker claims watch (+https://github.com/USA-RedDragon/astro-stacker)"

func get(ctx context.Context, c *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return do(c, req, limit)
}

func do(c *http.Client, req *http.Request, limit int64) ([]byte, error) {
	if c == nil {
		c = &http.Client{Timeout: time.Minute}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 160 {
			msg = msg[:160]
		}
		return nil, fmt.Errorf("%s answered %d: %s", req.URL.Host, resp.StatusCode, msg)
	}
	return b, nil
}

// SepArcsec is the angle between two positions in arcseconds.
func SepArcsec(ra1, dec1, ra2, dec2 float64) float64 {
	r := math.Pi / 180
	d1, d2 := dec1*r, dec2*r
	dra := (ra2 - ra1) * r
	s := math.Sin((d2-d1)/2)*math.Sin((d2-d1)/2) + math.Cos(d1)*math.Cos(d2)*math.Sin(dra/2)*math.Sin(dra/2)
	return 2 * math.Asin(math.Min(1, math.Sqrt(s))) * 180 / math.Pi * 3600
}

func day(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}
