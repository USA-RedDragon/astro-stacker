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

// SimbadURL is SIMBAD's TAP service.
const SimbadURL = "https://simbad.cds.unistra.fr/simbad/sim-tap/sync"

const userAgent = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"

// Cache keeps small answers from remote services in the processed bucket.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
}

// SimbadTAP asks SIMBAD's TAP service, keeping each answer under prefix
// for 30 days.
func SimbadTAP(client *http.Client, base string, cache Cache, prefix string, now func() time.Time) Simbad {
	if base == "" {
		base = SimbadURL
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return func(ctx context.Context, ra, dec, radius float64) ([]SimbadObject, error) {
		key := fmt.Sprintf("%ssimbad/%.5f%+.5f_%.0f.json.zst", prefix, ra, dec, radius*3600)
		type cached struct {
			At      time.Time      `json:"at"`
			Objects []SimbadObject `json:"objects"`
		}
		if cache != nil {
			if b, err := cache.Get(ctx, key); err == nil {
				var c cached
				if raw, err := unzstd(b); err == nil && json.Unmarshal(raw, &c) == nil && now().Sub(c.At) < 30*24*time.Hour {
					return c.Objects, nil
				}
			}
		}
		objs, err := simbadQuery(ctx, client, base, ra, dec, radius)
		if err != nil {
			return nil, err
		}
		if cache != nil {
			if raw, err := json.Marshal(cached{At: now(), Objects: objs}); err == nil {
				if z, err := zstdBytes(raw); err == nil {
					_ = cache.Put(ctx, key, z, "application/zstd")
				}
			}
		}
		return objs, nil
	}
}

func simbadQuery(ctx context.Context, client *http.Client, base string, ra, dec, radius float64) ([]SimbadObject, error) {
	form := url.Values{}
	form.Set("REQUEST", "doQuery")
	form.Set("LANG", "ADQL")
	form.Set("FORMAT", "tsv")
	form.Set("QUERY", fmt.Sprintf("SELECT TOP 20 b.main_id, b.ra, b.dec, b.otype, i.ids FROM basic AS b JOIN ids AS i ON i.oidref = b.oid "+
		"WHERE CONTAINS(POINT('ICRS', b.ra, b.dec), CIRCLE('ICRS', %.6f, %.6f, %.6f)) = 1", ra, dec, radius))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simbad: %s", resp.Status)
	}
	return parseSimbad(b)
}

var errSimbad = errors.New("simbad: unexpected response")

func parseSimbad(b []byte) ([]SimbadObject, error) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "main_id\t") {
		return nil, errSimbad
	}
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }
	out := make([]SimbadObject, 0, len(lines)-1)
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		ra, err1 := strconv.ParseFloat(unq(f[1]), 64)
		dec, err2 := strconv.ParseFloat(unq(f[2]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		o := SimbadObject{MainID: strings.Join(strings.Fields(unq(f[0])), " "), RA: ra, Dec: dec, OType: unq(f[3])}
		for _, id := range strings.Split(unq(f[4]), "|") {
			if id = strings.Join(strings.Fields(id), " "); id != "" {
				o.IDs = append(o.IDs, id)
			}
		}
		out = append(out, o)
	}
	return out, nil
}
