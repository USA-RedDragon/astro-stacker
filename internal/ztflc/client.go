package ztflc

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultURL is IRSA's ZTF light curve service.
	DefaultURL = "https://irsa.ipac.caltech.edu/cgi-bin/ZTF/nph_light_curves"
	// MatchRadius is how far, in arcseconds, a ZTF source may be from the star.
	MatchRadius = 1.5
	// BadCatFlags drops epochs IRSA marks unusable (clouds, moonlight).
	BadCatFlags = 32768
	// DefaultTimeout bounds one query; IRSA answered in 4 to 24 seconds
	// in October 2026, and one timeout stops the checks for the run.
	DefaultTimeout = 45 * time.Second
	userAgent      = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"
	maxBody        = 64 << 20
)

// Client queries the IRSA ZTF light curve API.
type Client struct {
	// BaseURL overrides DefaultURL.
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// QueryURL is the light curve query for the g and r epochs within
// MatchRadius of a position.
func (c *Client) QueryURL(ra, dec float64) string {
	q := url.Values{}
	q.Set("POS", fmt.Sprintf("CIRCLE %.6f %.6f %.7f", ra, dec, MatchRadius/3600))
	q.Set("BANDNAME", "g,r")
	q.Set("BAD_CATFLAGS_MASK", strconv.Itoa(BadCatFlags))
	q.Set("FORMAT", "csv")
	return c.base() + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// Fetch returns the ZTF g and r epochs within MatchRadius of a position.
func (c *Client) Fetch(ctx context.Context, ra, dec float64) ([]Point, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.QueryURL(ra, dec), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ztf light curves: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ztf light curves: %s", resp.Status)
	}
	pts, err := Parse(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("ztf light curves: %w", err)
	}
	return pts, nil
}

// Parse reads the API's CSV output.
func Parse(r io.Reader) ([]Point, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	head, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("empty response")
	}
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"oid", "mjd", "mag", "magerr", "catflags", "filtercode", "ra", "dec"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("no %s column in %q", need, strings.Join(head, ","))
		}
	}
	var out []Point
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		p, err := parseRow(rec, col)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
}

func parseRow(rec []string, col map[string]int) (Point, error) {
	var p Point
	var err error
	f := func(name string) float64 {
		if err != nil {
			return 0
		}
		var v float64
		v, err = strconv.ParseFloat(strings.TrimSpace(rec[col[name]]), 64)
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
		return v
	}
	p.MJD, p.Mag, p.MagErr, p.RA, p.Dec = f("mjd"), f("mag"), f("magerr"), f("ra"), f("dec")
	if err != nil {
		return p, err
	}
	if p.OID, err = strconv.ParseInt(strings.TrimSpace(rec[col["oid"]]), 10, 64); err != nil {
		return p, fmt.Errorf("oid: %w", err)
	}
	if p.CatFlags, err = strconv.Atoi(strings.TrimSpace(rec[col["catflags"]])); err != nil {
		return p, fmt.Errorf("catflags: %w", err)
	}
	p.Band = strings.TrimPrefix(strings.TrimSpace(rec[col["filtercode"]]), "z")
	return p, nil
}
