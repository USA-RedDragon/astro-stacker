// Package horizons asks JPL Horizons for topocentric ephemerides, one request
// at a time, with an application User-Agent, backing off when Horizons is
// busy, and keeps the tables it fetched in the app database so a target's
// night is fetched once.
package horizons

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	yes        = "'YES'"
	URL        = "https://ssd.jpl.nasa.gov/api/horizons.api"
	Quantities = "1,2,3,9,20,27,36"
	// KeyQuantities is the quantity list the cache key is computed with. It
	// stays the list before quantity 27 was added so the tables cached then
	// keep their keys and are fetched again only when they age out.
	KeyQuantities = "1,2,3,9,20,36"
	MaxRows       = 90024
)

var (
	ErrUnknownBody = errors.New("horizons does not know this body")
	ErrAmbiguous   = errors.New("horizons matched more than one body; store a more specific designation")
	ErrNoRows      = errors.New("horizons returned no positions")
)

func UserAgent(version string) string {
	if version == "" {
		version = "dev"
	}
	return "astro-stacker/" + version + " (jacob@mcswain.dev)"
}

// Command is the Horizons COMMAND for a stored designation: a designation
// that already holds a Horizons selector ("DES=2P;CAP;NOFRAG") is used as it
// is, a bare one ("C/2025 R2") is looked up as a comet's current apparition.
func Command(designation string) string {
	d := strings.TrimSpace(designation)
	if d == "" || strings.ContainsAny(d, "=;") {
		return d
	}
	return "DES=" + d + ";CAP;NOFRAG"
}

type Client struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
	Cache     *gorm.DB
	Attempts  int
	Backoff   time.Duration
	// MaxAge is how old a cached table of a night not yet over may be
	// before it is fetched again.
	MaxAge time.Duration
	// FailureTTL is how long a failed table is answered with its error
	// before it is asked for again.
	FailureTTL time.Duration
	Now        func() time.Time

	once     sync.Once
	slot     chan struct{}
	requests atomic.Int64

	failMu   sync.Mutex
	failures map[string]failure
}

type failure struct {
	err   error
	until time.Time
}

const (
	DefaultMaxAge     = 24 * time.Hour
	DefaultFailureTTL = time.Hour
	RequestTimeout    = 5 * time.Minute
	maxRetryWait      = 10 * time.Minute
)

func New(version string, cache *gorm.DB) *Client {
	return &Client{BaseURL: URL, UserAgent: UserAgent(version), Cache: cache}
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return URL
	}
	return c.BaseURL
}

func (c *Client) attempts() int {
	if c.Attempts <= 0 {
		return 5
	}
	return c.Attempts
}

func (c *Client) backoff() time.Duration {
	if c.Backoff <= 0 {
		return 5 * time.Second
	}
	return c.Backoff
}

func (c *Client) maxAge() time.Duration {
	if c.MaxAge <= 0 {
		return DefaultMaxAge
	}
	return c.MaxAge
}

func (c *Client) failureTTL() time.Duration {
	if c.FailureTTL <= 0 {
		return DefaultFailureTTL
	}
	return c.FailureTTL
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: RequestTimeout}
}

// Requests is how many HTTP requests the client has sent.
func (c *Client) Requests() int {
	return int(c.requests.Load())
}

func retryAfter(h string, fallback time.Duration) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, maxRetryWait)
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, maxRetryWait)
		}
	}
	return fallback
}

// acquire waits for the client's one request slot, or for ctx.
func (c *Client) acquire(ctx context.Context) error {
	c.once.Do(func() { c.slot = make(chan struct{}, 1) })
	select {
	case c.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) release() { <-c.slot }

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get sends one request; retry reports whether a failure is worth trying
// again (a 5xx, a timeout or a broken connection).
func (c *Client) get(ctx context.Context, q url.Values) (body []byte, status string, wait time.Duration, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", 0, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	c.requests.Add(1)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, "", 0, ctx.Err() == nil, fmt.Errorf("horizons: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.Status, 0, ctx.Err() == nil, fmt.Errorf("horizons: %w", err)
	}
	if resp.StatusCode >= 500 {
		return nil, resp.Status, retryAfter(resp.Header.Get("Retry-After"), 0), true, fmt.Errorf("horizons: %s", resp.Status)
	}
	return body, resp.Status, 0, false, nil
}

// Result sends one query and returns Horizons' result text. One request is
// in flight at a time across the process; a 5xx, a timeout or a broken
// connection is retried after Retry-After, or a growing wait.
func (c *Client) Result(ctx context.Context, q url.Values) (string, error) {
	if err := c.acquire(ctx); err != nil {
		return "", err
	}
	defer c.release()
	q = cloneValues(q)
	q.Set("format", "json")
	var body []byte
	var status string
	for attempt := range c.attempts() {
		b, st, wait, retry, err := c.get(ctx, q)
		if err == nil {
			body, status = b, st
			break
		}
		if !retry || attempt == c.attempts()-1 {
			return "", err
		}
		if wait <= 0 {
			wait = time.Duration((attempt+1)*(attempt+1)) * c.backoff()
		}
		if err := sleep(ctx, wait); err != nil {
			return "", err
		}
	}
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("horizons: %s: %w", status, err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("horizons: %s", r.Error)
	}
	return r.Result, nil
}

func cloneValues(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

type Request struct {
	Command string
	Site    sky.Site
	Start   time.Time
	Stop    time.Time
	Step    time.Duration
}

func (r Request) stepText() string {
	if r.Step%time.Minute == 0 && r.Step >= time.Minute {
		return fmt.Sprintf("%d m", int(r.Step/time.Minute))
	}
	return fmt.Sprintf("%d s", int(r.Step/time.Second))
}

func (r Request) Values() url.Values {
	elev := 0.0
	if r.Site.Elevation != nil && !math.IsNaN(*r.Site.Elevation) {
		elev = *r.Site.Elevation
	}
	q := url.Values{}
	for k, v := range map[string]string{
		"COMMAND": "'" + r.Command + "'", "OBJ_DATA": "'NO'", "MAKE_EPHEM": yes, "EPHEM_TYPE": "'OBSERVER'",
		"CENTER": "'coord@399'", "COORD_TYPE": "'GEODETIC'",
		"SITE_COORD": fmt.Sprintf("'%.6f,%.6f,%.4f'", r.Site.Longitude, r.Site.Latitude, elev/1000),
		"START_TIME": "'" + r.Start.UTC().Format("2006-01-02 15:04") + "'",
		"STOP_TIME":  "'" + r.Stop.UTC().Format("2006-01-02 15:04") + "'",
		"STEP_SIZE":  "'" + r.stepText() + "'", "QUANTITIES": "'" + Quantities + "'",
		"ANG_FORMAT": "'DEG'", "EXTRA_PREC": yes, "CSV_FORMAT": yes,
	} {
		q.Set(k, v)
	}
	return q
}

func (r Request) key() string {
	q := r.Values()
	q.Set("QUANTITIES", "'"+KeyQuantities+"'")
	h := sha256.Sum256([]byte(q.Encode()))
	return hex.EncodeToString(h[:])
}

func (r Request) validate() error {
	if strings.TrimSpace(r.Command) == "" {
		return errors.New("horizons: no body")
	}
	if r.Step <= 0 || !r.Stop.After(r.Start) {
		return errors.New("horizons: empty time span")
	}
	if n := int(r.Stop.Sub(r.Start)/r.Step) + 1; n > MaxRows {
		return fmt.Errorf("horizons: %d rows is over the %d row limit", n, MaxRows)
	}
	return nil
}

func (c *Client) failed(key string) error {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	f, ok := c.failures[key]
	if !ok {
		return nil
	}
	if c.now().After(f.until) {
		delete(c.failures, key)
		return nil
	}
	return f.err
}

func (c *Client) fail(key string, err error) {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	if c.failures == nil {
		c.failures = map[string]failure{}
	}
	now := c.now()
	for k, f := range c.failures {
		if now.After(f.until) {
			delete(c.failures, k)
		}
	}
	c.failures[key] = failure{err: err, until: now.Add(c.failureTTL())}
}

func (c *Client) fetch(ctx context.Context, r Request, key string) (Table, error) {
	if err := c.failed(key); err != nil {
		return Table{}, err
	}
	result, err := c.Result(ctx, r.Values())
	var t Table
	if err == nil {
		t, err = Parse(result)
	}
	if err != nil {
		if ctx.Err() == nil {
			c.fail(key, err)
		}
		return Table{}, err
	}
	t.FetchedAt = c.now().UTC()
	if c.Cache != nil {
		row := app.HorizonsCache{Key: key, Command: r.Command, Start: r.Start.UTC(), Stop: r.Stop.UTC(), Step: r.stepText(), Result: result, FetchedAt: t.FetchedAt}
		if err := c.Cache.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return t, fmt.Errorf("horizons cache: %w", err)
		}
	}
	return t, nil
}

// Ephemeris returns the table for r from the cache, or fetches and caches
// it. A cached table older than MaxAge whose span has not ended is fetched
// again, keeping the cached one if that fails; a failure is answered from
// memory for FailureTTL.
func (c *Client) Ephemeris(ctx context.Context, r Request) (Table, error) {
	if err := r.validate(); err != nil {
		return Table{}, err
	}
	key := r.key()
	var hit app.HorizonsCache
	if c.Cache != nil {
		if err := c.Cache.WithContext(ctx).Where("key = ?", key).Limit(1).Find(&hit).Error; err != nil {
			return Table{}, fmt.Errorf("horizons cache: %w", err)
		}
	}
	if hit.Key != "" {
		cached, err := Parse(hit.Result)
		if err == nil {
			cached.FetchedAt, cached.Cached = hit.FetchedAt, true
			if c.now().Sub(hit.FetchedAt) < c.maxAge() || !c.now().Before(r.Stop) {
				return cached, nil
			}
			if fresh, err := c.fetch(ctx, r, key); err == nil {
				return fresh, nil
			} else if ctx.Err() != nil {
				return Table{}, err
			}
			return cached, nil
		}
	}
	return c.fetch(ctx, r, key)
}

// EphemerisWithAngles is Ephemeris for a caller that needs the position
// angles: a cached table from before quantity 27 was asked for is fetched
// again once, under the same key.
func (c *Client) EphemerisWithAngles(ctx context.Context, r Request) (Table, error) {
	t, err := c.Ephemeris(ctx, r)
	if err != nil || t.Angles || !t.Cached {
		return t, err
	}
	if fresh, err := c.fetch(ctx, r, r.key()); err == nil {
		return fresh, nil
	} else if ctx.Err() != nil {
		return Table{}, err
	}
	return t, nil
}

// Prune drops cached tables that ended before cutoff.
func (c *Client) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	if c.Cache == nil {
		return 0, nil
	}
	res := c.Cache.WithContext(ctx).Where("stop < ?", cutoff.UTC()).Delete(&app.HorizonsCache{})
	return res.RowsAffected, res.Error
}

type Row struct {
	At      time.Time
	RA      float64
	Dec     float64
	RAApp   *float64
	DecApp  *float64
	RARate  float64
	DecRate float64
	Mag     *float64
	Delta   *float64
	UncRA   *float64
	UncDec  *float64
	// PsAng is the position angle of the extended Sun-to-body radius
	// vector, where a comet's ion tail points, and PsAMV that of the
	// negative of the body's heliocentric velocity, the dust trail's side;
	// both are degrees east of north (Horizons quantity 27).
	PsAng *float64
	PsAMV *float64
}

type Table struct {
	Rows      []Row
	FetchedAt time.Time
	Cached    bool
	// Angles reports whether the table has the quantity 27 columns.
	Angles bool
}

// MinDelta is the smallest geocentric range in the table, in au.
func (t Table) MinDelta() (float64, bool) {
	best, ok := math.Inf(1), false
	for _, r := range t.Rows {
		if r.Delta != nil && *r.Delta < best {
			best, ok = *r.Delta, true
		}
	}
	return best, ok
}

const (
	colTime = iota
	colRA
	colDec
	colRAApp
	colDecApp
	colRARate
	colDecRate
	colMag
	colDelta
	colUncRA
	colUncDec
	colPsAng
	colPsAMV
	colCount
)

func columnOf(name string) int {
	switch name {
	case "Date__(UT)__HR:MN", "Date__(UT)__HR:MN:SS", "Date__(UT)__HR:MN:SC.fff":
		return colTime
	case "R.A.___(ICRF)":
		return colRA
	case "DEC____(ICRF)":
		return colDec
	case "R.A.__(a-app)":
		return colRAApp
	case "DEC___(a-app)":
		return colDecApp
	case "dRA*cosD":
		return colRARate
	case "d(DEC)/dt":
		return colDecRate
	case "T-mag", "APmag":
		return colMag
	case "delta":
		return colDelta
	case "RA_3sigma":
		return colUncRA
	case "DEC_3sigma":
		return colUncDec
	case "PsAng":
		return colPsAng
	case "PsAMV":
		return colPsAMV
	}
	return -1
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-Jan-02 15:04", "2006-Jan-02 15:04:05", "2006-Jan-02 15:04:05.000"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("horizons time %q", s)
}

func optional(s string, dst **float64) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "n.a." {
		*dst = nil
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*dst = &v
	return nil
}

// Parse reads an OBSERVER table in CSV form with ANG_FORMAT=DEG, finding the
// columns by the header line above $$SOE.
func Parse(result string) (Table, error) {
	var t Table
	cols := make([]int, colCount)
	for i := range cols {
		cols[i] = -1
	}
	var prev, header string
	in := false
	sc := bufio.NewScanner(strings.NewReader(result))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch line {
		case "$$SOE":
			in = true
			for i, f := range strings.Split(header, ",") {
				if c := columnOf(strings.TrimSpace(f)); c >= 0 && cols[c] < 0 {
					cols[c] = i
				}
			}
			t.Angles = cols[colPsAng] >= 0 && cols[colPsAMV] >= 0
			for _, c := range []int{colTime, colRA, colDec, colRARate, colDecRate} {
				if cols[c] < 0 {
					return t, fmt.Errorf("horizons: the table has no %s column", []string{"time", "RA", "Dec", "", "", "RA rate", "Dec rate"}[c])
				}
			}
			continue
		case "$$EOE":
			in = false
			continue
		}
		if !in {
			if strings.HasPrefix(line, "****") && prev != "" && !strings.HasPrefix(prev, "****") {
				header = prev
			}
			prev = line
			continue
		}
		f := strings.Split(line, ",")
		get := func(c int) string {
			if cols[c] < 0 || cols[c] >= len(f) {
				return ""
			}
			return strings.TrimSpace(f[cols[c]])
		}
		at, err := parseTime(get(colTime))
		if err != nil {
			return t, err
		}
		row := Row{At: at.UTC()}
		var errs [4]error
		row.RA, errs[0] = strconv.ParseFloat(get(colRA), 64)
		row.Dec, errs[1] = strconv.ParseFloat(get(colDec), 64)
		row.RARate, errs[2] = strconv.ParseFloat(get(colRARate), 64)
		row.DecRate, errs[3] = strconv.ParseFloat(get(colDecRate), 64)
		if err := errors.Join(errs[:]...); err != nil {
			return t, fmt.Errorf("horizons row %q: %w", line, err)
		}
		for _, o := range []struct {
			col int
			dst **float64
		}{{colRAApp, &row.RAApp}, {colDecApp, &row.DecApp}, {colMag, &row.Mag}, {colDelta, &row.Delta}, {colUncRA, &row.UncRA}, {colUncDec, &row.UncDec}, {colPsAng, &row.PsAng}, {colPsAMV, &row.PsAMV}} {
			if err := optional(get(o.col), o.dst); err != nil {
				return t, fmt.Errorf("horizons row %q: %w", line, err)
			}
		}
		t.Rows = append(t.Rows, row)
	}
	if err := sc.Err(); err != nil {
		return t, err
	}
	if len(t.Rows) == 0 {
		if strings.Contains(result, "No matches found") || strings.Contains(result, "No such record") {
			return t, ErrUnknownBody
		}
		if strings.Contains(result, "Matching small-bodies") || strings.Contains(result, "Multiple major-bodies match") {
			return t, ErrAmbiguous
		}
		return t, ErrNoRows
	}
	return t, nil
}
