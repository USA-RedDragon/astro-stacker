package ztflc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// CachePrefix is where cached light curves live in the object store.
	CachePrefix = "discoveries/cache/ztf/"
	// CacheTTL is how long a cached light curve is used.
	CacheTTL = 30 * 24 * time.Hour
)

// ErrDown is returned for every check after one query fails, so a
// service that is down costs one timeout per run.
var ErrDown = errors.New("ztf light curves: gave up after an earlier failure")

// Store reads and writes the object store; discoveries.MinioStore fits.
// Any Get error is taken as a cache miss.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
}

// Checker judges claimed brightness changes against ZTF light curves,
// one query at a time, caching each star's light curve. Make one per
// run: after one failed query it stops asking.
type Checker struct {
	Client *Client
	// Store caches light curves; nil disables the cache.
	Store Store
	// Timeout bounds one query; zero is DefaultTimeout.
	Timeout time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu   sync.Mutex
	down bool
}

type cacheEntry struct {
	RA      float64   `json:"ra"`
	Dec     float64   `json:"dec"`
	Fetched time.Time `json:"fetched"`
	Points  []Point   `json:"points"`
}

// CacheKey is the object store key of the light curve at a position,
// rounded to 0.0001 degrees.
func CacheKey(ra, dec float64) string {
	return fmt.Sprintf("%s%.4f%+.4f.json.zst", CachePrefix, math.Mod(ra+360, 360), dec)
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Check says whether ZTF confirms a brightness change of amplitude
// magnitudes on the star at ra, dec (degrees). An error means ZTF was
// not asked or did not answer; treat it as Unknown.
func (c *Checker) Check(ctx context.Context, ra, dec, amplitude float64) (Verdict, error) {
	pts, err := c.Points(ctx, ra, dec)
	if err != nil {
		return Verdict{Kind: Unknown, Reason: err.Error()}, err
	}
	return Judge(pts, amplitude), nil
}

// Points returns the ZTF epochs at a position, from the cache when it is
// fresh.
func (c *Checker) Points(ctx context.Context, ra, dec float64) ([]Point, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := CacheKey(ra, dec)
	if e, ok := c.cached(ctx, key); ok {
		return e.Points, nil
	}
	if c.down {
		return nil, ErrDown
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cl := c.Client
	if cl == nil {
		cl = &Client{}
	}
	pts, err := cl.Fetch(cctx, ra, dec)
	if err != nil {
		c.down = true
		return nil, err
	}
	if c.Store != nil {
		if err := c.put(ctx, key, cacheEntry{RA: ra, Dec: dec, Fetched: c.now(), Points: pts}); err != nil {
			slog.Warn("Could not cache ZTF light curve", "key", key, "error", err)
		}
	}
	return pts, nil
}

func (c *Checker) cached(ctx context.Context, key string) (cacheEntry, bool) {
	var e cacheEntry
	if c.Store == nil {
		return e, false
	}
	b, err := c.Store.Get(ctx, key)
	if err != nil {
		return e, false
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return e, false
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(b, nil)
	if err != nil || json.Unmarshal(raw, &e) != nil {
		return e, false
	}
	if c.now().Sub(e.Fetched) > CacheTTL {
		return e, false
	}
	return e, true
}

func (c *Checker) put(ctx context.Context, key string, e cacheEntry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return err
	}
	defer enc.Close()
	return c.Store.Put(ctx, key, enc.EncodeAll(raw, nil), "application/zstd")
}
