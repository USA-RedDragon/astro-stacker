package tnsmirror

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultSite is the production Transient Name Server.
	DefaultSite = "https://www.wis-tns.org"
	// DefaultBaseURL is the directory of the public objects files.
	DefaultBaseURL = DefaultSite + filesPath
	filesPath      = "/system/files/tns_public_objects/"
	// FullFile is the whole list, staged daily around midnight UT.
	FullFile = "tns_public_objects.csv.zip"
	maxZip   = 512 << 20
	maxCSV   = 1 << 30
	// minGap spaces requests; TNS counts them over a running 60 s window.
	minGap = 5 * time.Second
)

// Site is a TNS site's base URL, such as DefaultSite or
// https://sandbox.wis-tns.org; empty is DefaultSite.
type Site string

func (s Site) root() string {
	if t := strings.TrimRight(string(s), "/"); t != "" {
		return t
	}
	return DefaultSite
}

// Files is the directory of the site's public objects files.
func (s Site) Files() string { return s.root() + filesPath }

// API is the site's API root.
func (s Site) API() string { return s.root() + "/api" }

// Page is the site's web page at path.
func (s Site) Page(path string) string { return s.root() + "/" + strings.TrimLeft(path, "/") }

// HourFile is the hourly delta file that ends at hour h (UT): the objects
// added or changed in the hour before.
func HourFile(h int) string { return fmt.Sprintf("tns_public_objects_%02d.csv.zip", h) }

// Bot is a TNS bot's identity and key.
type Bot struct {
	ID, Name, Key string
}

// Enabled says whether all three are set.
func (b Bot) Enabled() bool { return b.ID != "" && b.Name != "" && b.Key != "" }

// Marker is the bot's User-Agent: tns_marker{"tns_id":ID,"type":"bot","name":"NAME"}.
func (b Bot) Marker() string {
	var id any = b.ID
	if n, err := strconv.ParseInt(b.ID, 10, 64); err == nil {
		id = n
	}
	j, err := json.Marshal(struct {
		ID   any    `json:"tns_id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}{id, "bot", b.Name})
	if err != nil {
		return "tns_marker{}"
	}
	return "tns_marker" + string(j)
}

// ErrRateLimited is a 429 that waiting for the reset did not clear.
var ErrRateLimited = errors.New("tnsmirror: rate limited")

// ErrRefused is a 401 or 403: TNS does not take the bot's marker or key.
var ErrRefused = errors.New("tnsmirror: TNS refused the bot's credentials")

// Client downloads the public objects files as a bot, one request at a
// time, at most one per minGap, and waits out TNS's rate limit: when
// x-rate-limit-remaining reaches 0, or on a 429, until x-rate-limit-reset.
type Client struct {
	Bot     Bot
	BaseURL string
	HTTP    *http.Client
	// Gap overrides minGap, for tests.
	Gap time.Duration
	// Progress, if set, hears the bytes of a download read so far and its
	// size, -1 when the server does not say.
	Progress func(done, total int64)

	mu        sync.Mutex
	notBefore time.Time
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/"
	}
	return DefaultBaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Minute}
}

func (c *Client) gap() time.Duration {
	if c.Gap > 0 {
		return c.Gap
	}
	return minGap
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func resetAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("x-rate-limit-reset"))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	if n > 1e9 {
		return time.Until(time.Unix(int64(n), 0)), true
	}
	return time.Duration(n * float64(time.Second)), true
}

// Fetch downloads one file and returns the CSV inside its zip.
func (c *Client) Fetch(ctx context.Context, name string) ([]byte, error) {
	if !c.Bot.Enabled() {
		return nil, errors.New("tnsmirror: no TNS bot credentials")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; ; attempt++ {
		if err := sleepCtx(ctx, time.Until(c.notBefore)); err != nil {
			return nil, err
		}
		body, h, status, err := c.post(ctx, name)
		c.notBefore = time.Now().Add(c.gap())
		if err != nil {
			return nil, err
		}
		if rem := strings.TrimSpace(h.Get("x-rate-limit-remaining")); rem == "0" {
			if d, ok := resetAfter(h); ok {
				c.notBefore = time.Now().Add(d + time.Second)
			}
		}
		switch {
		case status == http.StatusTooManyRequests:
			d, ok := resetAfter(h)
			if !ok {
				d = time.Minute
			}
			c.notBefore = time.Now().Add(d + time.Second)
			if attempt >= 2 {
				return nil, fmt.Errorf("%w on %s", ErrRateLimited, name)
			}
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return nil, fmt.Errorf("%w: %s: HTTP %d", ErrRefused, name, status)
		case status != http.StatusOK:
			return nil, fmt.Errorf("tnsmirror: %s: HTTP %d", name, status)
		}
		return unzipCSV(body)
	}
}

func (c *Client) post(ctx context.Context, name string) ([]byte, http.Header, int, error) {
	form := url.Values{"api_key": {c.Bot.Key}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+name, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.Bot.Marker())
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("tnsmirror: %s: %s", name, strings.ReplaceAll(err.Error(), c.Bot.Key, "<key>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, resp.Header, resp.StatusCode, nil
	}
	var body io.Reader = resp.Body
	if c.Progress != nil {
		body = &progressReader{r: resp.Body, total: resp.ContentLength, tell: c.Progress}
	}
	b, err := io.ReadAll(io.LimitReader(body, maxZip+1))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("tnsmirror: %s: %w", name, err)
	}
	if len(b) > maxZip {
		return nil, nil, 0, fmt.Errorf("tnsmirror: %s is over %d MB", name, maxZip>>20)
	}
	return b, resp.Header, resp.StatusCode, nil
}

type progressReader struct {
	r     io.Reader
	n     int64
	last  int64
	total int64
	tell  func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.n-p.last >= 256<<10 || err != nil {
		p.last = p.n
		p.tell(p.n, p.total)
	}
	return n, err
}

func unzipCSV(b []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a zip: %w", ErrFormat, err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out, err := io.ReadAll(io.LimitReader(rc, maxCSV+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		if len(out) > maxCSV {
			return nil, fmt.Errorf("%w: CSV over %d MB", ErrFormat, maxCSV>>20)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: no CSV in the zip", ErrFormat)
}
