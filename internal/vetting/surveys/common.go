package surveys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// DefaultIRSAURL is IRSA, home of the ZTF and WISE archives.
	DefaultIRSAURL = "https://irsa.ipac.caltech.edu"
	// DefaultPrefix is where the caches live.
	DefaultPrefix = "vetting/cache/"
	userAgent     = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"
	gaiaEpoch     = 2016.0
	httpTries     = 4
	maxRetryWait  = 2 * time.Minute
)

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (f *Fetcher) irsa() string {
	if f.IRSAURL != "" {
		return strings.TrimRight(f.IRSAURL, "/")
	}
	return DefaultIRSAURL
}

func (f *Fetcher) prefix() string {
	p := f.Prefix
	if p == "" {
		p = DefaultPrefix
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// FieldSlug is the field's cache directory name: Slug, or one made from
// Object when Slug is empty.
func FieldSlug(field Field) string {
	if field.Slug != "" {
		return field.Slug
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(field.Object) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if s == "" {
		s = fmt.Sprintf("field%.3f%+.3f", field.RA, field.Dec)
	}
	return s
}

func (f *Fetcher) dir(field Field, survey string) string {
	return f.prefix() + FieldSlug(field) + "/" + survey + "/"
}

func starKey(t Target) string {
	if t.GaiaID != 0 {
		return strconv.FormatInt(t.GaiaID, 10)
	}
	return fmt.Sprintf("%.5f%+.5f", math.Mod(t.RA+360, 360), t.Dec)
}

// at is the star's position at a Julian year, from its Gaia position and
// proper motion.
func (t Target) at(year float64) (float64, float64) {
	dt := year - gaiaEpoch
	dec := t.Dec + t.PMDec*dt/3.6e6
	c := math.Cos(t.Dec * math.Pi / 180)
	ra := t.RA
	if c > 1e-6 {
		ra += t.PMRA * dt / 3.6e6 / c
	}
	return math.Mod(ra+360, 360), dec
}

// sepArcsec is the angle between two positions in degrees, in arcseconds.
func sepArcsec(ra1, dec1, ra2, dec2 float64) float64 {
	const d = math.Pi / 180
	s1, s2 := math.Sin((dec2-dec1)*d/2), math.Sin((ra2-ra1)*d/2)
	h := s1*s1 + math.Cos(dec1*d)*math.Cos(dec2*d)*s2*s2
	return 2 * math.Asin(math.Min(1, math.Sqrt(h))) / d * 3600
}

// dilution is the share of the light within radius arcsec of the star
// that is the star's own, from Gaia G magnitudes, and how many
// neighbours share it.
func dilution(t Target, radius float64) (float64, int) {
	if t.G == 0 || math.IsNaN(t.G) {
		return 1, 0
	}
	own := math.Pow(10, -0.4*t.G)
	all := own
	n := 0
	for _, nb := range t.Neighbours {
		sep := nb.Sep
		if sep == 0 {
			sep = sepArcsec(t.RA, t.Dec, nb.RA, nb.Dec)
		}
		if sep > radius || nb.G == 0 || math.IsNaN(nb.G) || nb.GaiaID == t.GaiaID {
			continue
		}
		all += math.Pow(10, -0.4*nb.G)
		n++
	}
	return own / all, n
}

func blendNote(d float64, n int, radius float64, survey string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%s blends the star with %d Gaia neighbour(s) within %.0f arcsec; the star gives %.0f%% of the light, from Gaia G", survey, n, radius, 100*d)
}

type httpError struct {
	Status int
	Body   string
	Retry  time.Duration
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

func retryAfter(h http.Header, def time.Duration) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(h.Get("Retry-After")); err == nil {
		return time.Until(t)
	}
	return def
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends a request built by mk, retrying on throttling and server
// errors with backoff, and hands an OK response to read.
func (f *Fetcher) do(ctx context.Context, mk func() (*http.Request, error), read func(*http.Response) error) error {
	var last error
	wait := 5 * time.Second
	for range httpTries {
		req, err := mk()
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := f.client().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = err
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			wait *= 2
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			err := read(resp)
			resp.Body.Close()
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		he := &httpError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body)), Retry: retryAfter(resp.Header, wait)}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return he
		}
		last = he
		if he.Retry > maxRetryWait {
			return he
		}
		if err := sleepCtx(ctx, he.Retry); err != nil {
			return err
		}
		wait *= 2
	}
	return last
}

func (f *Fetcher) getBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	var out []byte
	err := f.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	}, func(r *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			return err
		}
		if int64(len(b)) > limit {
			return fmt.Errorf("%s: answer over %d bytes", url, limit)
		}
		out = b
		return nil
	})
	return out, err
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func failed(survey, format string, args ...any) Fetch {
	return Fetch{Survey: survey, State: Failed, Note: fmt.Sprintf(format, args...)}
}
