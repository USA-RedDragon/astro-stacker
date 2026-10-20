package vetting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AlerceURL is ALeRCE's ZTF API, and VizierURL VizieR's tab separated
// cone search.
const (
	AlerceURL = "https://api.alerce.online/ztf/v1/"
	VizierURL = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv"
)

// ZTFDetection is one ZTF alert detection: its time, band, magnitude of
// the difference and whether the difference was brighter.
type ZTFDetection struct {
	MJD      float64 `json:"mjd"`
	Band     string  `json:"band"`
	Mag      float64 `json:"mag"`
	Err      float64 `json:"err"`
	Positive bool    `json:"positive"`
}

// ZTFObject is one ZTF alert object near a spot, with its detections.
type ZTFObject struct {
	OID        string         `json:"oid"`
	RA         float64        `json:"ra"`
	Dec        float64        `json:"dec"`
	SepArcsec  float64        `json:"sep_arcsec"`
	Detections []ZTFDetection `json:"detections"`
}

// Alerts lists the ZTF alert objects within radius arcsec.
type Alerts func(ctx context.Context, ra, dec, radius float64) ([]ZTFObject, error)

// PS1Object is one Pan-STARRS source near a spot; a magnitude of 0 is
// one it did not measure.
type PS1Object struct {
	RA        float64 `json:"ra"`
	Dec       float64 `json:"dec"`
	G         float64 `json:"g"`
	R         float64 `json:"r"`
	I         float64 `json:"i"`
	SepArcsec float64 `json:"sep_arcsec"`
}

// PS1 lists the Pan-STARRS sources within radius arcsec.
type PS1 func(ctx context.Context, ra, dec, radius float64) ([]PS1Object, error)

type cachedAnswer[T any] struct {
	At   time.Time `json:"at"`
	Data T         `json:"data"`
}

// cached answers from the cache under key while younger than maxAge,
// else asks fetch and keeps its answer.
func cached[T any](ctx context.Context, cache Cache, key string, maxAge time.Duration, now func() time.Time, fetch func() (T, error)) (T, error) {
	if cache != nil {
		if b, err := cache.Get(ctx, key); err == nil {
			var c cachedAnswer[T]
			if raw, err := unzstd(b); err == nil && json.Unmarshal(raw, &c) == nil && now().Sub(c.At) < maxAge {
				return c.Data, nil
			}
		}
	}
	v, err := fetch()
	if err != nil {
		return v, err
	}
	if cache != nil {
		if raw, err := json.Marshal(cachedAnswer[T]{At: now(), Data: v}); err == nil {
			if z, err := zstdBytes(raw); err == nil {
				_ = cache.Put(ctx, key, z, "application/zstd")
			}
		}
	}
	return v, nil
}

func getText(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return b, nil
}

// AlerceZTF asks ALeRCE for the ZTF alert objects near a spot and their
// detections, keeping each answer under prefix for a day.
func AlerceZTF(client *http.Client, base string, cache Cache, prefix string, now func() time.Time) Alerts {
	if base == "" {
		base = AlerceURL
	}
	base = strings.TrimRight(base, "/") + "/"
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return func(ctx context.Context, ra, dec, radius float64) ([]ZTFObject, error) {
		key := fmt.Sprintf("%salerce/%.5f%+.5f_%.0f.json.zst", prefix, ra, dec, radius)
		return cached(ctx, cache, key, 24*time.Hour, now, func() ([]ZTFObject, error) {
			return alerceQuery(ctx, client, base, ra, dec, radius)
		})
	}
}

func alerceQuery(ctx context.Context, client *http.Client, base string, ra, dec, radius float64) ([]ZTFObject, error) {
	q := url.Values{}
	q.Set("ra", strconv.FormatFloat(ra, 'f', 6, 64))
	q.Set("dec", strconv.FormatFloat(dec, 'f', 6, 64))
	q.Set("radius", strconv.FormatFloat(radius, 'f', 1, 64))
	q.Set("page_size", "20")
	b, err := getText(ctx, client, base+"objects/?"+q.Encode(), 8<<20)
	if err != nil {
		return nil, fmt.Errorf("alerce: %w", err)
	}
	var page struct {
		Items []struct {
			OID string  `json:"oid"`
			RA  float64 `json:"meanra"`
			Dec float64 `json:"meandec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, fmt.Errorf("alerce: %w", err)
	}
	out := make([]ZTFObject, 0, len(page.Items))
	for _, it := range page.Items {
		o := ZTFObject{OID: it.OID, RA: it.RA, Dec: it.Dec, SepArcsec: arcsec(ra, dec, it.RA, it.Dec)}
		if o.SepArcsec > radius {
			continue
		}
		db, err := getText(ctx, client, base+"objects/"+url.PathEscape(it.OID)+"/detections", 32<<20)
		if err != nil {
			return nil, fmt.Errorf("alerce: %w", err)
		}
		if o.Detections, err = parseAlerceDetections(db); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func parseAlerceDetections(b []byte) ([]ZTFDetection, error) {
	var rows []struct {
		MJD       float64  `json:"mjd"`
		FID       int      `json:"fid"`
		MagPSF    *float64 `json:"magpsf"`
		SigmaPSF  *float64 `json:"sigmapsf"`
		IsDiffPos int      `json:"isdiffpos"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("alerce detections: %w", err)
	}
	bands := map[int]string{1: "g", 2: "r", 3: "i"}
	out := make([]ZTFDetection, 0, len(rows))
	for _, r := range rows {
		if r.MagPSF == nil {
			continue
		}
		d := ZTFDetection{MJD: r.MJD, Band: bands[r.FID], Mag: *r.MagPSF, Positive: r.IsDiffPos > 0}
		if r.SigmaPSF != nil {
			d.Err = *r.SigmaPSF
		}
		out = append(out, d)
	}
	return out, nil
}

// VizierPS1 asks VizieR for Pan-STARRS DR1 sources near a spot, keeping
// each answer under prefix for 30 days.
func VizierPS1(client *http.Client, base string, cache Cache, prefix string, now func() time.Time) PS1 {
	if base == "" {
		base = VizierURL
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return func(ctx context.Context, ra, dec, radius float64) ([]PS1Object, error) {
		key := fmt.Sprintf("%sps1/%.5f%+.5f_%.0f.json.zst", prefix, ra, dec, radius)
		return cached(ctx, cache, key, 30*24*time.Hour, now, func() ([]PS1Object, error) {
			q := url.Values{}
			q.Set("-source", "II/349/ps1")
			q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
			q.Set("-c.rs", strconv.FormatFloat(radius, 'f', 1, 64))
			q.Set("-out", "RAJ2000,DEJ2000,gmag,rmag,imag")
			q.Set("-out.add", "_r")
			q.Set("-out.max", "50")
			b, err := getText(ctx, client, base+"?"+q.Encode(), 4<<20)
			if err != nil {
				return nil, fmt.Errorf("vizier: %w", err)
			}
			return parsePS1(b)
		})
	}
}

var errPS1 = errors.New("vizier: unexpected response")

func parsePS1(b []byte) ([]PS1Object, error) {
	var head []string
	var out []PS1Object
	dashes := false
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case head == nil:
			head = f
			continue
		case !dashes:
			if strings.HasPrefix(strings.TrimSpace(f[0]), "---") {
				dashes = true
			}
			continue
		}
		get := func(name string) float64 {
			for i, h := range head {
				if strings.TrimSpace(h) == name && i < len(f) {
					v, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
					if err == nil {
						return v
					}
				}
			}
			return 0
		}
		out = append(out, PS1Object{RA: get("RAJ2000"), Dec: get("DEJ2000"), G: get("gmag"), R: get("rmag"), I: get("imag"), SepArcsec: get("_r")})
	}
	if head == nil {
		if strings.Contains(string(b), "#INFO") || len(strings.TrimSpace(string(b))) == 0 {
			return nil, nil
		}
		return nil, errPS1
	}
	return out, nil
}

// GaiaTAPURL is the Gaia archive's synchronous TAP service.
const GaiaTAPURL = "https://gea.esac.esa.int/tap-server/tap/sync"

// Reddening is Gaia DR3 GSP-Phot's reddening of one star: E(BP-RP) with
// its 84th percentile, and its temperature, from the star's BP and RP
// spectra.
type Reddening struct {
	EBPRP      float64 `json:"ebpminrp"`
	EBPRPUpper float64 `json:"ebpminrp_upper"`
	Teff       float64 `json:"teff"`
	Found      bool    `json:"found"`
}

// GaiaReddening gives a Gaia DR3 star's GSP-Phot reddening.
type GaiaReddening func(ctx context.Context, sourceID int64) (Reddening, error)

// GaiaAP asks the Gaia archive for a star's GSP-Phot reddening, keeping
// each answer under prefix for a year: DR3 does not change.
func GaiaAP(client *http.Client, base string, cache Cache, prefix string, now func() time.Time) GaiaReddening {
	if base == "" {
		base = GaiaTAPURL
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return func(ctx context.Context, sourceID int64) (Reddening, error) {
		key := fmt.Sprintf("%sgaia-ap/%d.json.zst", prefix, sourceID)
		return cached(ctx, cache, key, 365*24*time.Hour, now, func() (Reddening, error) {
			form := url.Values{}
			form.Set("REQUEST", "doQuery")
			form.Set("LANG", "ADQL")
			form.Set("FORMAT", "csv")
			form.Set("QUERY", fmt.Sprintf("SELECT ebpminrp_gspphot, ebpminrp_gspphot_upper, teff_gspphot FROM gaiadr3.astrophysical_parameters WHERE source_id = %d", sourceID))
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(form.Encode()))
			if err != nil {
				return Reddening{}, err
			}
			req.Header.Set("User-Agent", userAgent)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := client.Do(req)
			if err != nil {
				return Reddening{}, fmt.Errorf("gaia: %w", err)
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return Reddening{}, fmt.Errorf("gaia: %w", err)
			}
			if resp.StatusCode != http.StatusOK {
				return Reddening{}, fmt.Errorf("gaia: %s", resp.Status)
			}
			return parseReddening(b)
		})
	}
}

var errGaiaAP = errors.New("gaia: unexpected response")

func parseReddening(b []byte) (Reddening, error) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "ebpminrp_gspphot,ebpminrp_gspphot_upper,teff_gspphot" {
		return Reddening{}, errGaiaAP
	}
	if len(lines) < 2 {
		return Reddening{}, nil
	}
	f := strings.Split(strings.TrimSpace(lines[1]), ",")
	if len(f) != 3 {
		return Reddening{}, errGaiaAP
	}
	v := make([]float64, 3)
	for i, s := range f {
		x, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			if strings.TrimSpace(s) == "" {
				return Reddening{}, nil
			}
			return Reddening{}, errGaiaAP
		}
		v[i] = x
	}
	return Reddening{EBPRP: v[0], EBPRPUpper: v[1], Teff: v[2], Found: true}, nil
}
